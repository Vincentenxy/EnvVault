package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/config"
	secretcrypto "envVault/internal/crypto"
	"envVault/internal/http/controller"
	"envVault/internal/logging"
	"envVault/internal/store/postgres"
	rediscache "envVault/internal/store/redis"
)

type Dependencies struct {
	Config     config.Config
	Store      *postgres.Repository
	RBAC       *postgres.RBACStore
	Cache      *rediscache.Cache
	Encryptor  secretcrypto.Encryptor
	Authorizer auth.Authorizer
	Database   interface {
		PingContext(ctx context.Context) error
	}
}

func NewRouter(deps Dependencies) *gin.Engine {
	router := gin.New()
	router.Use(
		logging.RequestIDMiddleware(deps.Config.HTTP.RequestIDHeader),
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
		Store:      deps.Store,
		RBAC:       deps.RBAC,
		Cache:      deps.Cache,
		Encryptor:  deps.Encryptor,
		Authorizer: deps.Authorizer,
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

			protected := v1.Group("")
			{
				if deps.Config.Auth.Enabled {
					protected.Use(auth.JWTMiddleware(auth.JWTConfig{
						PublicKey: deps.Config.Auth.PublicKey,
					}))
				} else {
					protected.Use(auth.StaticUserMiddleware(auth.UserInfo{
						UserId: deps.Config.Auth.DevUserID,
						Name:   deps.Config.Auth.DevUserName,
					}))
				}
				protected.GET("/me", ctrl.Me)

				org := protected.Group("/org")
				{
					org.GET("/list", ctrl.ListOrganizations)
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
				}

				folder := protected.Group("/folder")
				{
					folder.POST("/list", ctrl.ListFolders)
					folder.POST("/create", ctrl.CreateFolder)
					folder.POST("/info", ctrl.GetFolder)
					folder.POST("/update", ctrl.UpdateFolder)
					folder.POST("/delete", ctrl.DeleteFolder)
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
				}

				audit := protected.Group("/audit")
				{
					audit.POST("/list", ctrl.ListAuditRecords)
				}

				rbac := protected.Group("/rbac")
				{
					permission := rbac.Group("/permission")
					{
						permission.GET("/list", ctrl.ListPermissions)
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
						user.GET("/me", ctrl.GetCurrentRBACUser)
						user.POST("/list", ctrl.ListRBACUsers)
						user.POST("/grants", ctrl.ListUserGrants)
						user.POST("/permissions", ctrl.GetUserEffectivePermissions)
					}
				}
			}
		}
	}
}
