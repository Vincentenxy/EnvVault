package controller

import (
	"context"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/config"
	"envVault/internal/domain"
	"envVault/internal/logging"
	"envVault/internal/service"
	"envVault/internal/store/postgres"
	"envVault/internal/store/redis"
)

// Dependencies 把 handler 用到的协作方打包注入。
//
// 设计原则:
//   - 透传型的 CRUD(Org / Project / Env / EnvTpl / Folder / Audit)不造 service,
//     handler 直接用 Repo。这是 Go 项目的惯例,避免为了分层而分层。
//   - 只有真需要业务编排(secret 加密/缓存/审计、RBAC 授权计算、tree 组装)才有 service。
//   - Encryptor / Authorizer / Cache 是底层能力的 holder,留在 controller 之外注入。
type Dependencies struct {
	Config     config.Config
	Repo       *postgres.Repository
	Secret     service.SecretService
	RBAC       service.RBACService
	Tree       service.TreeService
	Auth       service.AuthService
	Authorizer auth.Authorizer
	Cache      *redis.Cache
	Database   interface {
		PingContext(ctx context.Context) error
	}
}

// Controller 持有 handler 真正需要的能力。
type Controller struct {
	config     config.Config
	repo       *postgres.Repository
	secret     service.SecretService
	rbac       service.RBACService
	tree       service.TreeService
	auth       service.AuthService
	authorizer auth.Authorizer
	cache      *redis.Cache
	database   interface {
		PingContext(ctx context.Context) error
	}
}

func New(deps Dependencies) *Controller {
	return &Controller{
		config:     deps.Config,
		repo:       deps.Repo,
		secret:     deps.Secret,
		rbac:       deps.RBAC,
		tree:       deps.Tree,
		auth:       deps.Auth,
		authorizer: deps.Authorizer,
		cache:      deps.Cache,
		database:   deps.Database,
	}
}

// cacheUpsert 集中处理 cache 可能为 nil 的边界 + 失败 logging.Warn 不向上抛。
// 16 个 CRUD 端点统一调它,避免在每个 handler 里复制 if ctrl.cache == nil 模板。
//
// 由于 cache.UpsertXxx 系列签名各不相同,本 helper 用闭包方式传入具体的 upsert 调用,
// 例如:ctrl.cacheUpsert(c, func(c *redis.Cache) error { return c.UpsertOrg(ctx, item) })。
func (ctrl *Controller) cacheUpsert(c *gin.Context, fn func(*redis.Cache) error) {
	if ctrl.cache == nil {
		return
	}
	if err := fn(ctrl.cache); err != nil {
		logging.Warn(c.Request.Context(), "cacheUpsert", "redis upsert failed", logging.F("error", err))
	}
}

// cacheDelete 同 cacheUpsert,失败仅 warn 不抛。
func (ctrl *Controller) cacheDelete(c *gin.Context, fn func(*redis.Cache) error) {
	if ctrl.cache == nil {
		return
	}
	if err := fn(ctrl.cache); err != nil {
		logging.Warn(c.Request.Context(), "cacheDelete", "redis delete failed", logging.F("error", err))
	}
}

// cacheInvalidateCascade 根据 DeleteXxx 返回的 CascadeScope 遍历同步失效 Redis
// cache。逐类 delete,失败仅 warn 不抛。secret 缓存同步不在本方法内(走 secret_service 自己的 cache)。
func (ctrl *Controller) cacheInvalidateCascade(c *gin.Context, scope domain.CascadeScope) {
	if ctrl.cache == nil {
		return
	}
	if scope.OrganizationId != "" {
		ctrl.cacheDelete(c, func(rc *redis.Cache) error { return rc.DeleteOrg(c.Request.Context(), scope.OrganizationId) })
	}
	for _, pid := range scope.ProjectIds {
		ctrl.cacheDelete(c, func(rc *redis.Cache) error { return rc.DeleteProject(c.Request.Context(), pid) })
	}
	for _, eid := range scope.EnvironmentIds {
		ctrl.cacheDelete(c, func(rc *redis.Cache) error { return rc.DeleteEnvironment(c.Request.Context(), eid) })
	}
	for _, fid := range scope.FolderIds {
		ctrl.cacheDelete(c, func(rc *redis.Cache) error { return rc.DeleteFolder(c.Request.Context(), fid) })
	}
}
