package postgres

import (
	"context"
	"database/sql"
	"fmt"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"envVault/internal/config"
)

func Open(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	_, db, err := OpenGORM(ctx, cfg)
	return db, err
}

func OpenGORM(ctx context.Context, cfg config.DatabaseConfig) (*gorm.DB, *sql.DB, error) {
	gormDB, err := gorm.Open(gormpostgres.Open(cfg.DSN()), &gorm.Config{
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres: %w", err)
	}

	db, err := gormDB.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("get postgres db: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("ping postgres: %w", err)
	}

	return gormDB, db, nil
}
