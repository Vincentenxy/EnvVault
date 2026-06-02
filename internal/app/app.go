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

	var cache *rediscache.Cache
	if cfg.Redis.Enabled {
		cache, err = rediscache.Open(ctx, cfg.Redis)
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
	}

	router := httpapi.NewRouter(httpapi.Dependencies{
		Config:     cfg,
		Database:   db,
		Store:      repository,
		RBAC:       rbacStore,
		Cache:      cache,
		Encryptor:  encryptor,
		Authorizer: auth.NewRBACAuthorizer(rbacStore),
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
