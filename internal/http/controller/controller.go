package controller

import (
	"context"

	"envVault/internal/auth"
	"envVault/internal/config"
	secretcrypto "envVault/internal/crypto"
	"envVault/internal/store/postgres"
)

type Dependencies struct {
	Config     config.Config
	Store      *postgres.Repository
	Encryptor  secretcrypto.Encryptor
	Authorizer auth.Authorizer
	Database   interface {
		PingContext(ctx context.Context) error
	}
}

type Controller struct {
	config     config.Config
	store      *postgres.Repository
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
		encryptor:  deps.Encryptor,
		authorizer: deps.Authorizer,
		database:   deps.Database,
	}
}
