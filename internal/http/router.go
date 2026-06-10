package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/config"
	"envVault/internal/http/controller"
	"envVault/internal/logging"
	"envVault/internal/service"
	"envVault/internal/store/postgres"
	"envVault/internal/store/redis"
)

type Dependencies struct {
	Config      config.Config
	Repo        *postgres.Repository
	Secret      service.SecretService
	RBAC        service.RBACService
	Tree        service.TreeService
	Auth        service.AuthService
	TokensCache *auth.TokensCache
	Authorizer  auth.Authorizer
	Cache       *redis.Cache
	Database    interface {
		PingContext(ctx context.Context) error
	}
}

func NewRouter(deps Dependencies) *gin.Engine {
	router := gin.New()
	router.Use(
		logging.RequestIdMiddleware(deps.Config.HTTP.RequestIdHeader),
		logging.AccessLogMiddleware(),
		logging.RecoveryMiddleware(),
	)

	LoadApiRoutes(router, deps)

	return router
}

func LoadApiRoutes(r *gin.Engine, deps Dependencies) {
	ctrl := controller.New(controller.Dependencies{
		Config:     deps.Config,
		Database:   deps.Database,
		Repo:       deps.Repo,
		Secret:     deps.Secret,
		RBAC:       deps.RBAC,
		Tree:       deps.Tree,
		Auth:       deps.Auth,
		Authorizer: deps.Authorizer,
		Cache:      deps.Cache,
	})

	pub := r.Group("")
	{
		pub.GET("/healthz", ctrl.Healthy)
	}

	api := r.Group("/api")
	{
		v1 := api.Group("/v1")
		{
			v1.GET("/readyz", ctrl.Ready)
			if deps.Config.Auth.DevTokenEnabled {
				v1.POST("/auth/dev/token", ctrl.CreateDevJWT)
			}

			// v9 auth: register / login 匿名可调
			authPub := v1.Group("/auth")
			{
				authPub.POST("/register", ctrl.Register)
				authPub.POST("/login", ctrl.Login)
			}

			protected := v1.Group("")
			{
				if deps.Config.Auth.Enabled {
					protected.Use(auth.JWTMiddleware(auth.JWTConfig{
						PublicKey:   deps.Config.Auth.PublicKey,
						TokensCache: deps.TokensCache,
					}))
				} else {
					protected.Use(auth.StaticUserMiddleware(auth.UserInfo{
						UserId: deps.Config.Auth.DevUserId,
						Name:   deps.Config.Auth.DevUserName,
					}))
				}
				protected.GET("/me", ctrl.Me)

				// v9 auth: logout / changePassword 需 JWT
				authProtected := protected.Group("/auth")
				{
					authProtected.POST("/logout", ctrl.Logout)
					authProtected.POST("/changePassword", ctrl.ChangePassword)
				}

				org := protected.Group("/org")
				{
					org.POST("/list", ctrl.ListOrganizations)
					org.POST("/create", ctrl.CreateOrganization)
					org.POST("/info", ctrl.GetOrganization)
					org.POST("/update", ctrl.UpdateOrganization)
					org.POST("/delete", ctrl.DeleteOrganization)
				}

				project := protected.Group("/project")
				{
					project.POST("/list", ctrl.ListProjects)
					project.POST("/create", ctrl.CreateProject)
					project.POST("/info", ctrl.GetProject)
					project.POST("/update", ctrl.UpdateProject)
					project.POST("/delete", ctrl.DeleteProject)
				}

				env := protected.Group("/env")
				{
					env.POST("/list", ctrl.ListEnvironments)
					env.POST("/create", ctrl.CreateEnvironment)
					env.POST("/info", ctrl.GetEnvironment)
					env.POST("/update", ctrl.UpdateEnvironment)
					env.POST("/delete", ctrl.DeleteEnvironment)
					template := env.Group("/template")
					{
						template.POST("/list", ctrl.ListEnvironmentTemplates)
						template.POST("/info", ctrl.GetEnvironmentTemplate)
					}
				}

				folder := protected.Group("/folder")
				{
					folder.POST("/list", ctrl.ListFolders)
					folder.POST("/create", ctrl.CreateFolder)
					folder.POST("/info", ctrl.GetFolder)
					folder.POST("/update", ctrl.UpdateFolder)
					folder.POST("/delete", ctrl.DeleteFolder)
					folder.POST("/listByProject", ctrl.ListFoldersByProject)
				}

				secret := protected.Group("/secret")
				{
					secret.POST("/list", ctrl.ListSecrets)
					secret.POST("/search", ctrl.SearchSecrets)
					secret.POST("/create", ctrl.CreateSecret)
					secret.POST("/info", ctrl.GetSecret)
					secret.POST("/reveal", ctrl.RevealSecret)
					secret.POST("/update", ctrl.UpdateSecret)
					secret.POST("/delete", ctrl.DeleteSecret)
					secret.POST("/path/info", ctrl.GetSecretByPath)
					secret.POST("/path/reveal", ctrl.RevealSecretByPath)
					secret.POST("/path/batchReveal", ctrl.BatchRevealSecretByPath)
					secret.POST("/code/batchReveal", ctrl.BatchRevealSecretByCode)
				}

				// v11 batchCreate:复数 /secrets,显式 dev/test/sim/prod 字段。
				// 与单条 /secret/* 区分(单条走 path access,批量走 explicit 字段)。
				secrets := protected.Group("/secrets")
				{
					secrets.POST("/batchCreate", ctrl.BatchCreateSecret)
					secrets.POST("/list", ctrl.ListSecretsAcrossEnvs)
				}

				audit := protected.Group("/audit")
				{
					audit.POST("/list", ctrl.ListAuditRecords)
				}

				rbac := protected.Group("/rbac")
				{
					permission := rbac.Group("/permission")
					{
						permission.POST("/list", ctrl.ListPermissions)
					}

					me := rbac.Group("/me")
					{
						me.POST("/permissions", ctrl.GetMyPermissions)
					}

					role := rbac.Group("/role")
					{
						role.POST("/list", ctrl.ListRoles)
						role.POST("/info", ctrl.GetRole)
						role.POST("/create", ctrl.CreateRole)
						role.POST("/update", ctrl.UpdateRole)
						role.POST("/delete", ctrl.DeleteRole)
					}

					binding := rbac.Group("/binding")
					{
						binding.POST("/list", ctrl.ListRoleBindings)
						binding.POST("/grant", ctrl.GrantRole)
						binding.POST("/revoke", ctrl.RevokeRole)
					}

					user := rbac.Group("/user")
					{
						user.POST("/me", ctrl.GetCurrentRBACUser)
						user.POST("/list", ctrl.ListRBACUsers)
						user.POST("/grants", ctrl.ListUserGrants)
						user.POST("/permissions", ctrl.GetUserEffectivePermissions)
					}
				}

				search := protected.Group("/search")
				{
					search.POST("/global", ctrl.GlobalSearch)
				}

				tree := protected.Group("/tree")
				{
					tree.POST("/get", ctrl.GetResourceTree)
				}
			}
		}
	}
}
