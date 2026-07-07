package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"envVault/internal/domain"
	uuidgen "envVault/internal/id"
	"envVault/internal/store"
)

// PATStore 持久化 Personal Access Token。
//
// 实现 store.AccessTokenRepository,纯 database/sql,不依赖 GORM。
// token 明文仅在 service 层生成并返回一次,DB 只存 SHA256 hash + prefix。
type PATStore struct {
	db *sql.DB
}

// NewPATStore 构造 PATStore。db 必填。
func NewPATStore(db *sql.DB) *PATStore {
	return &PATStore{db: db}
}

// 编译期接口符合性检查。
var _ store.AccessTokenRepository = (*PATStore)(nil)

// CreateToken 插入一条 access_tokens 记录。
func (s *PATStore) CreateToken(
	ctx context.Context,
	userId, name, tokenHash, tokenPrefix string,
	expiresAt *time.Time,
) (domain.AccessToken, error) {
	if s == nil || s.db == nil {
		return domain.AccessToken{}, errors.New("pat store is not configured")
	}
	id, err := uuidgen.NewUUID()
	if err != nil {
		return domain.AccessToken{}, err
	}
	now := time.Now()
	_, err = s.db.ExecContext(ctx, `
insert into access_tokens (id, user_id, name, token_hash, token_prefix, expires_at, created_at)
values ($1, $2, $3, $4, $5, $6, $7)
`, id, userId, name, tokenHash, tokenPrefix, expiresAt, now)
	if err != nil {
		return domain.AccessToken{}, fmt.Errorf("insert access_token: %w", err)
	}
	return domain.AccessToken{
		Id:          id,
		UserId:      userId,
		Name:        name,
		TokenPrefix: tokenPrefix,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}, nil
}

// ListTokensByUser 列出指定用户的所有未删除 token。
func (s *PATStore) ListTokensByUser(ctx context.Context, userId string) ([]domain.AccessToken, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pat store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
select id, user_id, name, token_prefix, expires_at, last_used_at, created_at
from access_tokens
where user_id = $1 and deleted_at is null
order by created_at desc
`, userId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []domain.AccessToken
	for rows.Next() {
		var t domain.AccessToken
		if err := rows.Scan(&t.Id, &t.UserId, &t.Name, &t.TokenPrefix, &t.ExpiresAt, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// SoftDeleteToken 软删除 token。只能删除自己的 token。
func (s *PATStore) SoftDeleteToken(ctx context.Context, tokenId, userId string) error {
	if s == nil || s.db == nil {
		return errors.New("pat store is not configured")
	}
	result, err := s.db.ExecContext(ctx, `
update access_tokens
set deleted_at = now()
where id = $1 and user_id = $2 and deleted_at is null
`, tokenId, userId)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ValidatePAT 按 tokenHash 查找未删除且未过期的 token,验证通过返回关联 userId。
func (s *PATStore) ValidatePAT(ctx context.Context, tokenHash string) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("pat store is not configured")
	}
	var userId string
	var expiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
select user_id, expires_at
from access_tokens
where token_hash = $1 and deleted_at is null
`, tokenHash).Scan(&userId, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	// 检查过期。expires_at 为 NULL 表示永不过期。
	if expiresAt.Valid && expiresAt.Time.Before(time.Now()) {
		return "", domain.ErrNotFound
	}
	// 异步更新 last_used_at:不阻塞认证,失败不影响结果。
	// 用 goroutine 会引入复杂度,这里直接同步更新,开销很小(一次 UPDATE 主键查询)。
	_, _ = s.db.ExecContext(ctx, `
update access_tokens set last_used_at = now() where token_hash = $1 and deleted_at is null
`, tokenHash)
	return userId, nil
}
