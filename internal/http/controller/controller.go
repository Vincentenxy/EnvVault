package controller

import (
	"context"

	"envVault/internal/auth"
	"envVault/internal/config"
	secretcrypto "envVault/internal/crypto"
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

type Controller struct {
	config     config.Config
	store      *postgres.Repository
	cache      *rediscache.Cache
	encryptor  secretcrypto.Encryptor
	authorizer auth.Authorizer
	database   interface {
		PingContext(ctx context.Context) error
	}
}

func New(deps Dependencies) *Controller {
	return &Controller{
		config:     deps.Config,
		store:      deps.Store,
		cache:      deps.Cache,
		encryptor:  deps.Encryptor,
		authorizer: deps.Authorizer,
		database:   deps.Database,
	}
}
