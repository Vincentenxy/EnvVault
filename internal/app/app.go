package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"envVault/internal/auth"
	"envVault/internal/auth/ratelimit"
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

	// pingCtx 只用于 DB 连接探测:5s ConnectTimeout 仅约束 Ping,ping 完即 cancel,
	// 避免后续启动流程的 ctx 被这个 5s 误伤(切到远程 DB/Redis 后 WarmSecrets 等
	// 长任务会触发 deadline exceeded)。
	pingCtx, pingCancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	gormDB, db, err := postgres.OpenGORM(pingCtx, cfg.Database)
	pingCancel()
	if err != nil {
		return err
	}
	defer db.Close()

	// 启动流程用独立 ctx(无超时),与 server 生命周期对齐;ListenAndServe 退出时
	// 由 defer cancel 释放。EnsureSystemData / userCache.Load 等都走它,不再受
	// ConnectTimeout 影响。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	encryptor, err := secretcrypto.NewAESGCMEncryptorFromBase64(cfg.Security.EncryptionKey)
	if err != nil {
		return err
	}
	userCache := postgres.NewUserCache()
	repository := postgres.NewRepository(db, userCache)
	rbacStore := postgres.NewRBACStore(db, gormDB, userCache)
	authStore := postgres.NewAuthStore(db, gormDB, userCache)
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
	//
	// v6 起,所有 service 入口都注入 auth.Authorizer 做权限判定。
	// 控制器(handler)只做认证拦截(JWT 解析 + 用户身份),
	// 不再调 allowScope,所有 authz.Allow 都下沉到 service。
	authorizer := auth.NewRBACAuthorizer(rbacStore)
	rbacSvc := service.NewRBACService(rbacStore, authorizer)

	var cache *rediscache.Cache
	var secretSvc service.SecretService
	var treeSvc service.TreeService
	if cfg.Redis.Enabled {
		var err error
		cache, err = rediscache.Open(ctx, cfg.Redis, encryptor)
		if err != nil {
			return err
		}
		defer cache.Close()

		if cfg.Redis.WarmUpOnStart {
			// secret 缓存预热放后台跑:warmUpCtx 不挂到 5s pingCtx 上,
			// 与 server 生命周期绑定(Run 退出时 cancel),失败仅 logging.Error 上报,
			// 不阻塞主启动流程。
			warmUpCtx, warmUpCancel := context.WithCancel(context.Background())
			defer warmUpCancel()
			go warmupSecretCache(warmUpCtx, repository, cache)
		}
		secretSvc = service.NewSecretService(repository, encryptor, cache, authorizer)
		treeSvc = service.NewTreeService(repository, cache, authorizer)
	} else {
		secretSvc = service.NewSecretService(repository, encryptor, nil, authorizer)
		treeSvc = service.NewTreeService(repository, nil, authorizer)
	}

	// v9: tokens_valid_after 进程内缓存 + 后台 refresher。
	// 注意:即便 Redis 未启用,进程内 cache 也要建出来 — JWT middleware 强依赖它。
	tokensCache := auth.NewTokensCache(auth.TokensCacheOptions{
		PerUserLoader: func(c context.Context, userId string) (time.Time, error) {
			return authStore.GetTokensValidAfter(c, userId)
		},
		Refresher: cfg.Auth.TokensCacheRefresh,
	})
	// 启动后台 refresher(独立 ctx;主 ctx 取消时随 server.Shutdown 退出)
	refresherCtx, refresherCancel := context.WithCancel(context.Background())
	defer refresherCancel()
	tokensCache.RunRefresher(refresherCtx, func(c context.Context) (map[string]time.Time, error) {
		return authStore.ListUsersWithTokensValidAfter(c)
	})

	// v9: AuthService 装配。Limiter 走 Redis(若启用),否则 noop(开发态)。
	var limiter ratelimit.Limiter
	if cfg.Redis.Enabled && cache != nil {
		limiter = ratelimit.NewRedisLimiter(cache.Client(), ratelimit.Options{
			Window:        cfg.Auth.LoginRateLimitWindow,
			MaxAttempts:   cfg.Auth.LoginRateLimit,
			LockoutPeriod: cfg.Auth.LockoutDuration,
			KeyPrefix:     "ratelimit:login",
		})
	} else {
		limiter = noopLimiter{}
	}
	authSvc := service.NewAuthService(service.AuthServiceOptions{
		AuthRepo:       authStore,
		PasswordHasher: auth.NewArgon2idHasher(auth.DefaultPasswordParams()),
		Limiter:        limiter,
		TokensCache:    tokensCache,
		PrivateKeyPEM:  cfg.Auth.PrivateKey,
		TokenTTL:       cfg.Auth.TokenTTL,
		PasswordMinLen: cfg.Auth.PasswordMinLength,
	})

	router := httpapi.NewRouter(httpapi.Dependencies{
		Config:      cfg,
		Database:    db,
		Repo:        repository,
		Secret:      secretSvc,
		RBAC:        rbacSvc,
		Tree:        treeSvc,
		Auth:        authSvc,
		TokensCache: tokensCache,
		Authorizer:  authorizer,
		Cache:       cache,
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

// noopLimiter 在 Redis 未启用时提供「不限流」的 ratelimit 实现,保留 Login 流程可跑通。
type noopLimiter struct{}

func (noopLimiter) Check(_ context.Context, _ string) error          { return nil }
func (noopLimiter) Record(_ context.Context, _ string, _ bool) error { return nil }

// warmupSecretCache 在独立 goroutine 里跑 ListSecretCacheRecords + WarmSecrets,
// 与主启动流程解耦:
//   - 使用独立的 ctx(无 ConnectTimeout 限制),失败/成功都通过 logging 上报,
//     不返回 error,绝不阻塞主启动;
//   - ctx 由调用方通过 defer warmUpCancel() 释放,server 退出时随之 cancel。
func warmupSecretCache(ctx context.Context, repo *postgres.Repository, cache *rediscache.Cache) {
	records, err := repo.ListSecretCacheRecords(ctx)
	if err != nil {
		logging.Error(ctx, "AppRun.Warmup", "list secret cache records failed",
			logging.F("error", err.Error()))
		return
	}
	if err := cache.WarmSecrets(ctx, records); err != nil {
		logging.Error(ctx, "AppRun.Warmup", "warm secrets failed",
			logging.F("error", err.Error()),
			logging.F("count", len(records)))
		return
	}
	logging.Info(ctx, "AppRun.Warmup", "redis cache warmup completed",
		logging.F("count", len(records)))
}
