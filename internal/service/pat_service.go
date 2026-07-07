package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/store"
)

// AccessTokenService PAT 业务层:生成 token、列出、删除。
//
// token 明文仅在 CreatePAT 时返回一次,之后不可再取。
// 格式:envvault-<randomBase64URL>,类似 GitLab 的 glpat-xxxxx。
type AccessTokenService interface {
	// CreatePAT 生成新 token。返回 token 信息和明文(仅此一次)。
	CreatePAT(ctx context.Context, userId, name string, expiresAt *time.Time) (token domain.AccessToken, plain string, err error)
	// ListPATs 列出当前用户的所有未删除 token。
	ListPATs(ctx context.Context, userId string) ([]domain.AccessToken, error)
	// DeletePAT 软删除指定 token(只能删自己的)。
	DeletePAT(ctx context.Context, userId, tokenId string) error
}

type patService struct {
	store store.AccessTokenRepository
}

// NewAccessTokenService 构造 PAT service。
func NewAccessTokenService(store store.AccessTokenRepository) AccessTokenService {
	return &patService{store: store}
}

// tokenDisplayPrefixLen 用于列表展示的 token_prefix 截取长度(envvault- 后面取 8 字符 + "...")。
const tokenDisplayPrefixLen = len(auth.PATPrefix) + 8

// CreatePAT 生成 32 字节随机 token,格式为 envvault-<base64url>。
func (s *patService) CreatePAT(ctx context.Context, userId, name string, expiresAt *time.Time) (domain.AccessToken, string, error) {
	if s == nil {
		return domain.AccessToken{}, "", errors.New("pat service is not configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.AccessToken{}, "", errors.New("token name is required")
	}
	if userId == "" {
		return domain.AccessToken{}, "", errors.New("userId is required")
	}

	plain, err := generateToken()
	if err != nil {
		return domain.AccessToken{}, "", fmt.Errorf("generate token: %w", err)
	}

	hash := auth.HashPAT(plain)
	prefix := displayPrefix(plain)

	tok, err := s.store.CreateToken(ctx, userId, name, hash, prefix, expiresAt)
	if err != nil {
		return domain.AccessToken{}, "", err
	}
	return tok, plain, nil
}

// ListPATs 列出当前用户的所有未删除 token。
func (s *patService) ListPATs(ctx context.Context, userId string) ([]domain.AccessToken, error) {
	if s == nil {
		return nil, errors.New("pat service is not configured")
	}
	if userId == "" {
		return nil, errors.New("userId is required")
	}
	return s.store.ListTokensByUser(ctx, userId)
}

// DeletePAT 软删除指定 token。只能删自己的。
func (s *patService) DeletePAT(ctx context.Context, userId, tokenId string) error {
	if s == nil {
		return errors.New("pat service is not configured")
	}
	if userId == "" || tokenId == "" {
		return errors.New("userId and tokenId are required")
	}
	return s.store.SoftDeleteToken(ctx, tokenId, userId)
}

// ---- 内部 helper ----

// generateToken 生成 32 字节随机 token,格式 envvault-<base64url_no_padding>。
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return auth.PATPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// displayPrefix 截取 token 前 N 字符用于列表展示(如 "envvault-aBcDeFgH...")。
func displayPrefix(plain string) string {
	if len(plain) <= tokenDisplayPrefixLen {
		return plain + "..."
	}
	return plain[:tokenDisplayPrefixLen] + "..."
}
