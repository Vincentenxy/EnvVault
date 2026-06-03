package controller

import (
	"context"

	"envVault/internal/auth"
	"envVault/internal/config"
	"envVault/internal/service"
	"envVault/internal/store/postgres"
)

// Dependencies 把 handler 用到的协作方打包注入。
//
// 设计原则:
//   - 透传型的 CRUD(Org / Project / Env / EnvTpl / Folder / Audit)不造 service,
//     handler 直接用 Repo。这是 Go 项目的惯例,避免为了分层而分层。
//   - 只有真需要业务编排(secret 加密/缓存/审计、RBAC 授权计算)才有 service。
//   - Encryptor / Authorizer 是底层能力的 holder,留在 controller 之外注入。
type Dependencies struct {
	Config     config.Config
	Repo       *postgres.Repository
	Secret     service.SecretService
	RBAC       service.RBACService
	Authorizer auth.Authorizer
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
	authorizer auth.Authorizer
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
		authorizer: deps.Authorizer,
		database:   deps.Database,
	}
}
