package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	secretcrypto "envVault/internal/crypto"
	"envVault/internal/domain"
	"envVault/internal/logging"
	"envVault/internal/store"
)

// SecretService 集中 secret 的业务编排:值加密、Redis 缓存、reveal 审计。
// 持久化(写 DB)由 store.ResourceRepository 完成,本服务只补"业务胶水"。
type SecretService interface {
	Create(ctx context.Context, folderId, key, value, comment, actor string) (domain.Secret, error)
	Update(ctx context.Context, id, key, value, comment, actor string) (domain.Secret, error)
	Delete(ctx context.Context, id, actor string) error

	Get(ctx context.Context, id string) (domain.Secret, error)
	Reveal(ctx context.Context, id, actor string) (domain.Secret, error)

	// 路径访问:解析 "org.proj.env.folder.KEY" 后查 secret metadata。
	// 不返回 value;需要明文走 RevealByPath。
	GetByPath(ctx context.Context, path string) (domain.Secret, error)
	RevealByPath(ctx context.Context, path, actor string) (domain.Secret, error)

	List(ctx context.Context, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error)
	Search(ctx context.Context, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error)
}

type secretService struct {
	repo      store.ResourceRepository
	encryptor secretcrypto.Encryptor
	cache     store.SecretCache // 可为 nil
}

func NewSecretService(repo store.ResourceRepository, encryptor secretcrypto.Encryptor, cache store.SecretCache) SecretService {
	return &secretService{repo: repo, encryptor: encryptor, cache: cache}
}

func (s *secretService) Create(ctx context.Context, folderId, key, value, comment, actor string) (domain.Secret, error) {
	ciphertext, err := s.encrypt(ctx, value)
	if err != nil {
		return domain.Secret{}, err
	}
	secret, err := s.repo.CreateSecret(ctx, folderId, key, comment, actor, ciphertext)
	if err != nil {
		return domain.Secret{}, err
	}
	s.cacheUpsert(ctx, secret, ciphertext)
	return secret, nil
}

func (s *secretService) Update(ctx context.Context, id, key, value, comment, actor string) (domain.Secret, error) {
	ciphertext, err := s.encrypt(ctx, value)
	if err != nil {
		return domain.Secret{}, err
	}
	secret, err := s.repo.UpdateSecret(ctx, id, key, comment, actor, ciphertext)
	if err != nil {
		return domain.Secret{}, err
	}
	s.cacheUpsert(ctx, secret, ciphertext)
	return secret, nil
}

func (s *secretService) Delete(ctx context.Context, id, actor string) error {
	if err := s.repo.DeleteSecret(ctx, id, actor); err != nil {
		return err
	}
	if s.cache != nil {
		if err := s.cache.DeleteSecret(ctx, id); err != nil {
			logging.Warn(ctx, "SecretService.Delete", "redis delete failed", logging.F("error", err), logging.F("id", id))
		}
	}
	return nil
}

func (s *secretService) Get(ctx context.Context, id string) (domain.Secret, error) {
	return s.repo.GetSecret(ctx, id)
}

func (s *secretService) Reveal(ctx context.Context, id, actor string) (domain.Secret, error) {
	secret, ciphertext, err := s.repo.GetSecretCiphertext(ctx, id)
	if err != nil {
		return domain.Secret{}, err
	}
	plaintext, err := s.decrypt(ctx, ciphertext)
	if err != nil {
		return domain.Secret{}, err
	}
	// reveal 审计:不存明文,只记录 id + actor + action,replay 攻击也能定位。
	if err := s.repo.RecordAudit(ctx, actor, "secret", secret.Id, "reveal", nil); err != nil {
		return domain.Secret{}, fmt.Errorf("record reveal audit: %w", err)
	}
	secret.Value = plaintext
	return secret, nil
}

// GetByPath 解析 "org.proj.env.folder.KEY" 后查 secret metadata,不返回 value。
func (s *secretService) GetByPath(ctx context.Context, path string) (domain.Secret, error) {
	orgCode, projectCode, envCode, folderCode, key, err := parseSecretPath(path)
	if err != nil {
		return domain.Secret{}, err
	}
	return s.repo.GetSecretByPath(ctx, orgCode, projectCode, envCode, folderCode, key)
}

// RevealByPath 先按路径解析拿 id,再走现有 Reveal 加密 + 审计链路。
func (s *secretService) RevealByPath(ctx context.Context, path, actor string) (domain.Secret, error) {
	secret, err := s.GetByPath(ctx, path)
	if err != nil {
		return domain.Secret{}, err
	}
	return s.Reveal(ctx, secret.Id, actor)
}

// parseSecretPath 把 "org.project.env.folder.KEY" 拆成 5 段,任一段为空或段数不对都报错。
func parseSecretPath(path string) (org, proj, env, folder, key string, err error) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	if len(parts) != 5 {
		return "", "", "", "", "", fmt.Errorf("invalid secret path: expected org.project.env.folder.KEY, got %q", path)
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return "", "", "", "", "", fmt.Errorf("invalid secret path: empty segment in %q", path)
		}
	}
	return parts[0], parts[1], parts[2], parts[3], parts[4], nil
}

func (s *secretService) List(ctx context.Context, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error) {
	return s.repo.ListSecrets(ctx, filter, pagination)
}

func (s *secretService) Search(ctx context.Context, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error) {
	// 优先走 Redis 缓存;失败回落到 DB,避免缓存抖动阻塞业务。
	if s.cache != nil {
		items, err := s.cache.SearchSecrets(ctx, filter)
		if err == nil {
			return paginateSecrets(items, pagination), nil
		}
		logging.Warn(ctx, "SecretService.Search", "redis search failed, fallback to postgres", logging.F("error", err))
	}
	return s.repo.ListSecrets(ctx, filter, pagination)
}

func (s *secretService) encrypt(ctx context.Context, value string) (domain.SecretCiphertext, error) {
	if s.encryptor == nil {
		return domain.SecretCiphertext{}, errors.New("encryptor is not configured")
	}
	c, err := s.encryptor.Encrypt(ctx, []byte(value))
	if err != nil {
		return domain.SecretCiphertext{}, err
	}
	return domain.SecretCiphertext{Algorithm: c.Algorithm, Nonce: c.Nonce, Data: c.Data}, nil
}

func (s *secretService) decrypt(ctx context.Context, ciphertext domain.SecretCiphertext) (string, error) {
	if s.encryptor == nil {
		return "", errors.New("encryptor is not configured")
	}
	plaintext, err := s.encryptor.Decrypt(ctx, secretcrypto.Ciphertext{
		Algorithm: ciphertext.Algorithm,
		Nonce:     ciphertext.Nonce,
		Data:      ciphertext.Data,
	})
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s *secretService) cacheUpsert(ctx context.Context, secret domain.Secret, ciphertext domain.SecretCiphertext) {
	if s.cache == nil {
		return
	}
	payload, err := json.Marshal(ciphertext)
	if err != nil {
		logging.Warn(ctx, "SecretService.cacheUpsert", "marshal ciphertext failed", logging.F("error", err), logging.F("id", secret.Id))
		return
	}
	if err := s.cache.UpsertSecret(ctx, domain.SecretCacheRecord{Secret: secret, ValueCiphertext: payload}); err != nil {
		logging.Warn(ctx, "SecretService.cacheUpsert", "redis upsert failed", logging.F("error", err), logging.F("id", secret.Id))
	}
}

// paginateSecrets 内存分页 helper,缓存路径走这里(缓存已经过滤完成)。
func paginateSecrets(items []domain.Secret, pagination domain.Pagination) domain.PaginatedResult[domain.Secret] {
	pagination = pagination.Normalize()
	total := int64(len(items))
	start := pagination.Offset()
	if start >= len(items) {
		return domain.PaginatedResult[domain.Secret]{Items: []domain.Secret{}, Total: total}
	}
	end := start + pagination.Limit()
	if end > len(items) {
		end = len(items)
	}
	return domain.PaginatedResult[domain.Secret]{Items: items[start:end], Total: total}
}
