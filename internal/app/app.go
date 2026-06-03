package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"envVault/internal/auth"
	"envVault/internal/config"
	secretcrypto "envVault/internal/crypto"
	httpapi "envVault/internal/http"
	"envVault/internal/logging"
	"envVault/internal/service"
	"envVault/internal/store/postgres"
	rediscache "envVault/internal/store/redis"
)

func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	gormDB, db, err := postgres.OpenGORM(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	encryptor, err := secretcrypto.NewAESGCMEncryptorFromBase64(cfg.Security.EncryptionKey)
	if err != nil {
		return err
	}
	userCache := postgres.NewUserCache()
	repository := postgres.NewRepository(db, userCache)
	rbacStore := postgres.NewRBACStore(db, gormDB, userCache)
	if err := rbacStore.EnsureSystemData(ctx); err != nil {
		return err
	}
	if err := userCache.Load(ctx, db); err != nil {
		return err
	}
	if adminUserId := os.Getenv("ENVVAULT_BOOTSTRAP_ADMIN_USER_ID"); adminUserId != "" {
		if err := rbacStore.EnsureBootstrapAdmin(ctx, adminUserId, os.Getenv("ENVVAULT_BOOTSTRAP_ADMIN_NAME")); err != nil {
			return err
		}
	}

	// service 层:仅 secret 编排与 RBAC 业务两处需要 service。
	// 透传型 CRUD(Org/Project/Env/EnvTpl/Folder/Audit)直接 handler→repo。
	rbacSvc := service.NewRBACService(rbacStore)

	var cache *rediscache.Cache
	var secretSvc service.SecretService
	if cfg.Redis.Enabled {
		var err error
		cache, err = rediscache.Open(ctx, cfg.Redis, encryptor)
		if err != nil {
			return err
		}
		defer cache.Close()

		if cfg.Redis.WarmUpOnStart {
			records, err := repository.ListSecretCacheRecords(ctx)
			if err != nil {
				return err
			}
			if err := cache.WarmSecrets(ctx, records); err != nil {
				return err
			}
			logging.Info(ctx, "AppRun", "redis cache warmup completed", logging.F("count", len(records)))
		}
		secretSvc = service.NewSecretService(repository, encryptor, cache)
	} else {
		secretSvc = service.NewSecretService(repository, encryptor, nil)
	}

	router := httpapi.NewRouter(httpapi.Dependencies{
		Config:     cfg,
		Database:   db,
		Repo:       repository,
		Secret:     secretSvc,
		RBAC:       rbacSvc,
		Authorizer: auth.NewRBACAuthorizer(rbacStore),
		Cache:      cache,
	})

	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
