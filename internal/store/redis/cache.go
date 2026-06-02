package redis

import (
	"context"
	"encoding/base64"
	"strings"

	goredis "github.com/go-redis/redis/v8"

	"envVault/internal/config"
	"envVault/internal/logging"
	"envVault/internal/store/postgres"
)

type Cache struct {
	client goredis.UniversalClient
	prefix string
}

func Open(ctx context.Context, cfg config.RedisConfig) (*Cache, error) {
	addrs := cfg.Addrs
	if cfg.Mode == "single" && len(addrs) > 1 {
		addrs = addrs[:1]
	}

	client := goredis.NewUniversalClient(&goredis.UniversalOptions{
		Addrs:        addrs,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
		MaxRetries:   cfg.MaxRetries,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, err
	}

	return &Cache{
		client: client,
		prefix: strings.TrimSuffix(cfg.KeyPrefix, ":"),
	}, nil
}

func (c *Cache) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *Cache) WarmSecrets(ctx context.Context, records []postgres.SecretCacheRecord) error {
	if c == nil {
		return nil
	}

	if err := c.client.Del(ctx, c.idsKey()).Err(); err != nil {
		return err
	}
	if err := c.deletePathKeys(ctx); err != nil {
		return err
	}
	for _, record := range records {
		if err := c.UpsertSecret(ctx, record); err != nil {
			return err
		}
	}
	logging.Info(ctx, "RedisWarmSecrets", "secret cache warmed", logging.F("count", len(records)))
	return nil
}

func (c *Cache) UpsertSecret(ctx context.Context, record postgres.SecretCacheRecord) error {
	if c == nil {
		return nil
	}

	secret := record.Secret
	key := c.secretKey(secret.Id)
	oldPath, err := c.client.HGet(ctx, key, "path").Result()
	if err != nil && err != goredis.Nil {
		return err
	}
	if oldPath != "" && oldPath != secret.Path {
		if err := c.client.Del(ctx, c.pathKey(oldPath)).Err(); err != nil {
			return err
		}
	}
	values := map[string]any{
		"id":               secret.Id,
		"org_id":           secret.OrgId,
		"org_code":         secret.OrgCode,
		"project_id":       secret.ProjectId,
		"project_code":     secret.ProjectCode,
		"environment_id":   secret.EnvironmentId,
		"environment_code": secret.EnvironmentCode,
		"folder_id":        secret.FolderId,
		"folder_code":      secret.FolderCode,
		"key":              secret.Key,
		"path":             secret.Path,
		"value_ciphertext": base64.StdEncoding.EncodeToString(record.ValueCiphertext),
		"comment":          secret.Comment,
		"version":          secret.Version,
		"created_by":       secret.CreatedBy,
		"created_by_label": secret.CreatedByLabel,
		"updated_by":       secret.UpdatedBy,
		"updated_by_label": secret.UpdatedByLabel,
		"created_at":       secret.CreatedAt.Format(timeLayout),
		"updated_at":       secret.UpdatedAt.Format(timeLayout),
	}
	if err := c.client.HSet(ctx, key, values).Err(); err != nil {
		return err
	}
	if secret.Path != "" {
		if err := c.client.Set(ctx, c.pathKey(secret.Path), secret.Id, 0).Err(); err != nil {
			return err
		}
	}
	return c.client.SAdd(ctx, c.idsKey(), secret.Id).Err()
}

func (c *Cache) DeleteSecret(ctx context.Context, id string) error {
	if c == nil {
		return nil
	}
	secretKey := c.secretKey(id)
	path, err := c.client.HGet(ctx, secretKey, "path").Result()
	if err != nil && err != goredis.Nil {
		return err
	}
	keys := []string{secretKey}
	if path != "" {
		keys = append(keys, c.pathKey(path))
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		return err
	}
	return c.client.SRem(ctx, c.idsKey(), id).Err()
}

func (c *Cache) SearchSecrets(ctx context.Context, filter postgres.ListFilter) ([]postgres.Secret, error) {
	if c == nil {
		return nil, nil
	}

	ids, err := c.client.SMembers(ctx, c.idsKey()).Result()
	if err != nil {
		return nil, err
	}

	keyword := strings.ToLower(filter.Keyword)
	items := make([]postgres.Secret, 0, len(ids))
	for _, id := range ids {
		values, err := c.client.HGetAll(ctx, c.secretKey(id)).Result()
		if err != nil {
			return nil, err
		}
		if len(values) == 0 || !matches(values, filter, keyword) {
			continue
		}
		items = append(items, postgres.Secret{
			Id:              values["id"],
			OrgId:           values["org_id"],
			OrgCode:         values["org_code"],
			ProjectId:       values["project_id"],
			ProjectCode:     values["project_code"],
			EnvironmentId:   values["environment_id"],
			EnvironmentCode: values["environment_code"],
			FolderId:        values["folder_id"],
			FolderCode:      values["folder_code"],
			Key:             values["key"],
			Path:            values["path"],
			Comment:         values["comment"],
			Version:         atoi(values["version"]),
			CreatedBy:       values["created_by"],
			CreatedByLabel:  labelOrId(values["created_by_label"], values["created_by"]),
			UpdatedBy:       values["updated_by"],
			UpdatedByLabel:  labelOrId(values["updated_by_label"], values["updated_by"]),
			CreatedAt:       parseTime(values["created_at"]),
			UpdatedAt:       parseTime(values["updated_at"]),
		})
	}
	return items, nil
}

func (c *Cache) idsKey() string {
	return c.prefix + ":secret:ids"
}

func (c *Cache) secretKey(id string) string {
	return c.prefix + ":secret:" + id
}

func (c *Cache) pathKey(path string) string {
	return c.prefix + ":secret:path:" + path
}

func (c *Cache) deletePathKeys(ctx context.Context) error {
	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, c.pathKey("*"), 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := c.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		if nextCursor == 0 {
			return nil
		}
		cursor = nextCursor
	}
}

func matches(values map[string]string, filter postgres.ListFilter, keyword string) bool {
	if filter.OrgId != "" && values["org_id"] != filter.OrgId {
		return false
	}
	if filter.ProjectId != "" && values["project_id"] != filter.ProjectId {
		return false
	}
	if filter.EnvironmentId != "" && values["environment_id"] != filter.EnvironmentId {
		return false
	}
	if filter.FolderId != "" && values["folder_id"] != filter.FolderId {
		return false
	}
	if keyword != "" && !strings.Contains(strings.ToLower(values["key"]), keyword) && !strings.Contains(strings.ToLower(values["path"]), keyword) {
		return false
	}
	return true
}
