package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"

	goredis "github.com/go-redis/redis/v8"

	"envVault/internal/config"
	secretcrypto "envVault/internal/crypto"
	"envVault/internal/domain"
	"envVault/internal/logging"
)

type Cache struct {
	client    goredis.UniversalClient
	prefix    string
	encryptor secretcrypto.Encryptor // 可为 nil:nil 时跳过 value 解密搜索
}

func Open(ctx context.Context, cfg config.RedisConfig, encryptor secretcrypto.Encryptor) (*Cache, error) {
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
		client:    client,
		prefix:    strings.TrimSuffix(cfg.KeyPrefix, ":"),
		encryptor: encryptor,
	}, nil
}

func (c *Cache) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *Cache) WarmSecrets(ctx context.Context, records []domain.SecretCacheRecord) error {
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

func (c *Cache) UpsertSecret(ctx context.Context, record domain.SecretCacheRecord) error {
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

func (c *Cache) SearchSecrets(ctx context.Context, filter domain.ListFilter) ([]domain.Secret, error) {
	if c == nil {
		return nil, nil
	}

	ids, err := c.client.SMembers(ctx, c.idsKey()).Result()
	if err != nil {
		return nil, err
	}

	keyword := strings.ToLower(filter.Keyword)
	items := make([]domain.Secret, 0, len(ids))
	for _, id := range ids {
		values, err := c.client.HGetAll(ctx, c.secretKey(id)).Result()
		if err != nil {
			return nil, err
		}
		if len(values) == 0 || !matches(values, filter, keyword) {
			continue
		}
		items = append(items, domain.Secret{
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

func matches(values map[string]string, filter domain.ListFilter, keyword string) bool {
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

// =============================================================================
// Per-type metadata cache (org / project / env / folder)
// =============================================================================
//
// 与 secret 不同,这 4 类只缓存元数据(无加密载荷),用于支持跨 5 类资源的
// 全局搜索 GlobalSearch。secret 的值(value)只在 GlobalSearch 路径上按需解密。

// resourceType 是 GlobalSearch 关心的 5 类资源。
type resourceType string

const (
	resourceOrg     resourceType = "org"
	resourceProject resourceType = "project"
	resourceEnv     resourceType = "env"
	resourceFolder  resourceType = "folder"
	resourceSecret  resourceType = "secret"
)

func (c *Cache) typeIdsKey(t resourceType) string {
	return c.prefix + ":" + string(t) + ":ids"
}

func (c *Cache) typeKey(t resourceType, id string) string {
	return c.prefix + ":" + string(t) + ":" + id
}

// upsertResource 把资源元数据塞进 Redis 的 (type:ids, type:<id>) 双结构。
// fields 是不定长 map,调用方负责填哪些列。
func (c *Cache) upsertResource(ctx context.Context, t resourceType, id string, fields map[string]any) error {
	if c == nil {
		return nil
	}
	key := c.typeKey(t, id)
	pipe := c.client.Pipeline()
	pipe.HSet(ctx, key, fields)
	pipe.SAdd(ctx, c.typeIdsKey(t), id)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *Cache) deleteResource(ctx context.Context, t resourceType, id string) error {
	if c == nil {
		return nil
	}
	pipe := c.client.Pipeline()
	pipe.Del(ctx, c.typeKey(t, id))
	pipe.SRem(ctx, c.typeIdsKey(t), id)
	_, err := pipe.Exec(ctx)
	return err
}

// UpsertOrg 写 org 元数据到 Redis(无加密载荷)。
func (c *Cache) UpsertOrg(ctx context.Context, org domain.Entity) error {
	return c.upsertResource(ctx, resourceOrg, org.Id, map[string]any{
		"id":         org.Id,
		"code":       org.Code,
		"name":       org.Name,
		"comment":    org.Comment,
		"created_by": org.CreatedBy,
		"updated_by": org.UpdatedBy,
	})
}

func (c *Cache) DeleteOrg(ctx context.Context, id string) error {
	return c.deleteResource(ctx, resourceOrg, id)
}

// UpsertProject 写 project 元数据;同时缓存 org_id 以便 GlobalSearch 后做 RBAC scope 检查。
func (c *Cache) UpsertProject(ctx context.Context, project domain.Entity) error {
	return c.upsertResource(ctx, resourceProject, project.Id, map[string]any{
		"id":         project.Id,
		"org_id":     project.ParentId,
		"code":       project.Code,
		"name":       project.Name,
		"comment":    project.Comment,
		"created_by": project.CreatedBy,
		"updated_by": project.UpdatedBy,
	})
}

func (c *Cache) DeleteProject(ctx context.Context, id string) error {
	return c.deleteResource(ctx, resourceProject, id)
}

// UpsertEnvironment 写 env 元数据;缓存 project_id 供 RBAC scope。
func (c *Cache) UpsertEnvironment(ctx context.Context, env domain.Entity) error {
	return c.upsertResource(ctx, resourceEnv, env.Id, map[string]any{
		"id":         env.Id,
		"project_id": env.ParentId,
		"code":       env.Code,
		"name":       env.Name,
		"comment":    env.Comment,
		"created_by": env.CreatedBy,
		"updated_by": env.UpdatedBy,
	})
}

func (c *Cache) DeleteEnvironment(ctx context.Context, id string) error {
	return c.deleteResource(ctx, resourceEnv, id)
}

// UpsertFolder 写 folder 元数据;缓存 env_id 与 project_id 供 RBAC scope。
// 多级 folder(level=1/2)的 level 与 parent_id 也缓存,便于后续做"按 parent 列子 folder"等。
func (c *Cache) UpsertFolder(ctx context.Context, folder domain.Entity, envId, projectId, parentId string, level int) error {
	return c.upsertResource(ctx, resourceFolder, folder.Id, map[string]any{
		"id":             folder.Id,
		"environment_id": envId,
		"project_id":     projectId,
		"parent_id":      parentId,
		"level":          level,
		"code":           folder.Code,
		"name":           folder.Name,
		"comment":        folder.Comment,
		"created_by":     folder.CreatedBy,
		"updated_by":     folder.UpdatedBy,
	})
}

func (c *Cache) DeleteFolder(ctx context.Context, id string) error {
	return c.deleteResource(ctx, resourceFolder, id)
}

// GlobalSearchHit 是 GlobalSearch 单条命中的展示形态。
type GlobalSearchHit struct {
	Id         string `json:"id"`
	Code       string `json:"code,omitempty"`
	Name       string `json:"name,omitempty"`
	Comment    string `json:"comment,omitempty"`
	MatchField string `json:"matchField"`
	// 仅 secret 类型使用:
	Key       string `json:"key,omitempty"`
	Path      string `json:"path,omitempty"`
	FolderId  string `json:"folderId,omitempty"`
	EnvId     string `json:"envId,omitempty"`
	ProjectId string `json:"projectId,omitempty"`
	OrgId     string `json:"orgId,omitempty"`
}

// GlobalSearchResult 是 GlobalSearch 的聚合结果。
type GlobalSearchResult struct {
	Orgs     []GlobalSearchHit `json:"orgs"`
	Projects []GlobalSearchHit `json:"projects"`
	Envs     []GlobalSearchHit `json:"envs"`
	Folders  []GlobalSearchHit `json:"folders"`
	Secrets  []GlobalSearchHit `json:"secrets"`
}

// GlobalSearch 在 Redis 中并发扫描 5 类资源,聚合返回 keyword 命中的资源。
// 匹配字段:code / name / comment(全 5 类),secret 还额外解密 value 后匹配。
//
// 性能模型:每类一个 goroutine,内部用 Redis pipeline 一次性拉全部 HGetAll;
// N 候选 × M 字段匹配在 Go 内存里做,纯字符串子串,不查 DB。
//
// 失联场景:cache 为 nil → 返回空 result,error=nil,上层 fallback 到 DB。
func (c *Cache) GlobalSearch(ctx context.Context, keyword string, perTypeLimit int) (GlobalSearchResult, error) {
	result := GlobalSearchResult{}
	if c == nil || c.client == nil {
		return result, nil
	}
	keywordLower := strings.ToLower(strings.TrimSpace(keyword))
	if keywordLower == "" {
		return result, nil
	}
	if perTypeLimit <= 0 {
		perTypeLimit = 50
	}

	types := []resourceType{resourceOrg, resourceProject, resourceEnv, resourceFolder, resourceSecret}
	type typeResult struct {
		t    resourceType
		hits []GlobalSearchHit
		err  error
	}
	resultCh := make(chan typeResult, len(types))
	var wg sync.WaitGroup
	for _, t := range types {
		wg.Add(1)
		go func(t resourceType) {
			defer wg.Done()
			hits, err := c.searchType(ctx, t, keywordLower, perTypeLimit)
			resultCh <- typeResult{t: t, hits: hits, err: err}
		}(t)
	}
	wg.Wait()
	close(resultCh)

	for r := range resultCh {
		if r.err != nil {
			// 单类型失败不阻塞其他类型,继续返回
			continue
		}
		switch r.t {
		case resourceOrg:
			result.Orgs = r.hits
		case resourceProject:
			result.Projects = r.hits
		case resourceEnv:
			result.Envs = r.hits
		case resourceFolder:
			result.Folders = r.hits
		case resourceSecret:
			result.Secrets = r.hits
		}
	}
	return result, nil
}

// searchType 单类资源的 keyword 匹配。pipeline 拉全部 HGetAll,Go 内存匹配。
func (c *Cache) searchType(ctx context.Context, t resourceType, keywordLower string, limit int) ([]GlobalSearchHit, error) {
	ids, err := c.client.SMembers(ctx, c.typeIdsKey(t)).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Pipeline 一次性拉所有 hash,1 次 round-trip
	pipe := c.client.Pipeline()
	cmds := make([]*goredis.StringStringMapCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGetAll(ctx, c.typeKey(t, id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != goredis.Nil {
		return nil, err
	}

	hits := make([]GlobalSearchHit, 0, limit)
	for _, cmd := range cmds {
		values, err := cmd.Result()
		if err != nil || len(values) == 0 {
			continue
		}
		field, ok := matchKeywordOnFields(values, t, keywordLower, c)
		if !ok {
			continue
		}
		hits = append(hits, buildHit(t, values, field))
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

// matchKeywordOnFields 在已加载的 hash values 上做 keyword 子串匹配。
// 字段优先级:code > name > comment > value(仅 secret)。返回命中的字段名与是否命中。
func matchKeywordOnFields(values map[string]string, t resourceType, keywordLower string, c *Cache) (string, bool) {
	if containsFold(values["code"], keywordLower) {
		return "code", true
	}
	if containsFold(values["name"], keywordLower) {
		return "name", true
	}
	if containsFold(values["comment"], keywordLower) {
		return "comment", true
	}
	// secret 额外解密 value 后匹配
	if t == resourceSecret && c.encryptor != nil {
		ciphertextB64 := values["value_ciphertext"]
		if ciphertextB64 != "" {
			if plaintext, ok := c.decryptCachedValue(ciphertextB64); ok && containsFold(plaintext, keywordLower) {
				return "value", true
			}
		}
	}
	return "", false
}

// buildHit 把 Redis hash values 装配成 GlobalSearchHit。
// 不同资源类型的"展示字段"不同,这里按 type 分支装配。
func buildHit(t resourceType, values map[string]string, matchField string) GlobalSearchHit {
	h := GlobalSearchHit{
		Id:         values["id"],
		Code:       values["code"],
		Name:       values["name"],
		Comment:    values["comment"],
		MatchField: matchField,
	}
	switch t {
	case resourceProject:
		h.OrgId = values["org_id"]
	case resourceEnv:
		h.ProjectId = values["project_id"]
	case resourceFolder:
		h.EnvId = values["environment_id"]
		h.ProjectId = values["project_id"]
	case resourceSecret:
		h.Key = values["key"]
		h.Path = values["path"]
		h.FolderId = values["folder_id"]
		h.EnvId = values["environment_id"]
		h.ProjectId = values["project_id"]
		h.OrgId = values["org_id"]
	}
	return h
}

func containsFold(haystack, needleLower string) bool {
	if haystack == "" || needleLower == "" {
		return false
	}
	return strings.Contains(strings.ToLower(haystack), needleLower)
}

// decryptCachedValue 从 Redis 取到的 base64 ciphertext 解密为明文。
// 失败返回 ("", false),由调用方继续走其他字段匹配,不阻塞整体搜索。
func (c *Cache) decryptCachedValue(ciphertextB64 string) (string, bool) {
	if c.encryptor == nil {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", false
	}
	var ct secretcrypto.Ciphertext
	if err := json.Unmarshal(raw, &ct); err != nil {
		return "", false
	}
	plain, err := c.encryptor.Decrypt(context.Background(), ct)
	if err != nil {
		return "", false
	}
	return string(plain), true
}
