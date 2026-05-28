package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"envVault/internal/auth"
	"envVault/internal/config"
	secretcrypto "envVault/internal/crypto"
	httpapi "envVault/internal/http"
	"envVault/internal/store/postgres"
)

func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	db, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	encryptor, err := secretcrypto.NewAESGCMEncryptorFromBase64(cfg.Security.EncryptionKey)
	if err != nil {
		return err
	}
	repository := postgres.NewRepository(db)

	router := httpapi.NewRouter(httpapi.Dependencies{
		Config:     cfg,
		Database:   db,
		Store:      repository,
		Encryptor:  encryptor,
		Authorizer: auth.AllowAllAuthorizer{},
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
