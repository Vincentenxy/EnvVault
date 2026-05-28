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

			protected := v1.Group("")
			{
				protected.Use(auth.JWTMiddleware(auth.JWTConfig{
					Secret: []byte(deps.Config.Auth.JWTSecret),
				}))
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
					secret.POST("/update", ctrl.UpdateSecret)
					secret.POST("/delete", ctrl.DeleteSecret)
				}

				audit := protected.Group("/audit")
				{
					audit.POST("/list", ctrl.ListAuditRecords)
				}
			}
		}
	}
}
