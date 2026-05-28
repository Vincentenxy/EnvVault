package service

import (
	"context"

	"envVault/internal/domain"
)

type SecretValue struct {
	Secret domain.Secret
	Value  []byte
}

type SecretStore interface {
	Save(ctx context.Context, value SecretValue) error
	Find(ctx context.Context, scope domain.SecretScope, key domain.SecretKey) (SecretValue, error)
}

type AuditRecorder interface {
	RecordSecretChange(ctx context.Context, secret domain.Secret, action string) error
}

type SecretService struct {
	store SecretStore
	audit AuditRecorder
}

func NewSecretService(store SecretStore, audit AuditRecorder) *SecretService {
	return &SecretService{
		store: store,
		audit: audit,
	}
}
