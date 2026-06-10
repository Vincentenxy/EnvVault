package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"envVault/internal/auth"
	secretcrypto "envVault/internal/crypto"
	"envVault/internal/domain"
	"envVault/internal/logging"
	"envVault/internal/store"
)

// SecretService 集中 secret 的业务编排:值加密、Redis 缓存、reveal 审计、
// **权限检查**(v6 起)。持久化(写 DB)由 store.ResourceRepository 完成,本服务
// 只补"业务胶水"。
//
// 权限判定统一走 auth.Authorizer.Allow,scope 取请求体中最深的可用字段:
//
//	secret:create  → folder:{folderId}
//	secret:read    → secret:{id}
//	secret:reveal  → secret:{id}(独立码,与 secret:read 不复用)
//	secret:update  → secret:{id}
//	secret:delete  → secret:{id}
//	secret:list    → folder:{folderId} 或 environment:{environmentId}(FolderId 优先)
//	secret:search  → 同 list
type SecretService interface {
	Create(ctx context.Context, user auth.UserInfo, folderId, key, value, comment, actor string) (domain.Secret, error)
	Update(ctx context.Context, user auth.UserInfo, id, key, value, comment, actor string) (domain.Secret, error)
	Delete(ctx context.Context, user auth.UserInfo, id, actor string) error

	Get(ctx context.Context, user auth.UserInfo, id string) (domain.Secret, error)
	Reveal(ctx context.Context, user auth.UserInfo, id, actor string) (domain.Secret, error)

	// 路径访问:解析 "org.proj.env.folder.KEY" 后查 secret metadata。
	// 不返回 value;需要明文走 RevealByPath。
	GetByPath(ctx context.Context, user auth.UserInfo, path string) (domain.Secret, error)
	RevealByPath(ctx context.Context, user auth.UserInfo, path, actor string) (domain.Secret, error)

	// 批量路径 reveal:解析 "org.proj.env.folder" 4 段路径 + 可选 keys 列表,
	// 一次性返回所有命中 key 的明文。keys 为空/缺省 = 该 folder 下所有 secret。
	// 不分页,整批 1 条 audit(action=reveal_batch, resource_type=folder)。
	BatchRevealByPath(ctx context.Context, user auth.UserInfo, path string, keys []string, actor string) ([]domain.Secret, []string, error)

	// 批量 code 维度 reveal:接收 4 级 code 字段,reveal folder 下所有 secret 明文。
	// 与 BatchRevealByPath 行为等价,仅入参形式不同(结构化 code vs 字符串 path)。
	// 整批 1 条 audit(action=reveal_batch, resource_type=folder),无命中时不写 audit。
	// 返回 (secrets, err):无 notFound 字段(无 keys 对照)。
	BatchRevealByCode(ctx context.Context, user auth.UserInfo, orgCode, projectCode, envCode, folderCode, actor string) ([]domain.Secret, error)

	// 批量创建:接收 secretList,每条 item 含 (key, comment?, dev/test/sim/prod 字段),
	// 每个 env 字段显式指定目标 folderId + value。服务端展开为 (item, env) 二元组
	// 序列,逐条做 secret:create 权限 check + 加密,单事务 N 条 INSERT + 1 条
	// batch audit,任一阶段失败整批 rollback。
	//
	// 业务错误以 sentinel 形式返出(controller 统一翻译为 HTTP 200 + body code=-1):
	//   - auth.ErrPermissionDenied:任一目标 folder 缺 secret:create
	//   - domain.ErrNotFound:目标 folder 不存在
	//   - domain.ErrConflict:任一 (folder, key) 冲突
	//   - 其他 err:走通用 err.Error() 描述
	BatchCreate(ctx context.Context, user auth.UserInfo, req BatchCreateRequest, actor string) error

	List(ctx context.Context, user auth.UserInfo, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error)
	// Search returns secret hits in scope-defined shape:
	//   - filter.ProjectId != "" → items 元素为 *domain.SecretGroup(按 folder,key 聚合,
	//     每个 env 一个完整 Secret,JSON 序列化为 {code, <envCode>: {...}, ...})
	//   - filter.EnvironmentId / FolderId 维度 → items 元素为 domain.Secret(1 行 1 secret,
	//     Values 是单 entry map)
	//   - scope 全空(items 来自 RBAC 收窄后的全量)走 env/folder 维度同形态
	// 用 PaginatedResult[any] 承载两种不同 item 形态,handler 端用 type assertion 区分;
	// JSON 序列化由每种类型各自的 MarshalJSON/Marshal 决定,handler 不需要关心。
	Search(ctx context.Context, user auth.UserInfo, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[any], error)

	// ListAcrossEnvs 按 (projectId, [folderCode], [key]) 跨 envList 一次性 reveal。
	//   - envList 必填(必传,1..32 项,trim+去重+空过滤后)
	//   - key 非空:精确查 (folderCode, key) 跨 envList,folderCode 必传
	//   - key 为空:列出项目下所有 (folderCode, key) 跨 envList,folderCode 被忽略
	// 权限:v12 起 SQL 收窄用 secret:read(原 secret:reveal)。持有 read 即看到
	// 明文;无 read 的 secret 在 SQL 层就被收窄掉,不出现在 result 里。批量浏览
	// 场景用 read 即可,单点 /secret/reveal 仍要求 reveal 保持细粒度控制。
	// 整批 1 条 audit(action=reveal_batch, resource_type=project, resource_id=projectId);
	// 无命中(空 records)时不写 audit,与 BatchRevealByPath 一致。
	// 全空场景 service 返 HTTP 200,data 是空数组(全 key 都查不到)或元素 env 全 null。
	// 响应是 *[]*SecretAcrossEnvs,handler 直接序列化为数组;key 有值时 1 元素、key 为空时 N 元素。
	ListAcrossEnvs(ctx context.Context, user auth.UserInfo, projectId, folderCode, key string, envList []string, actor string) ([]*domain.SecretAcrossEnvs, error)
}

// BatchCreateRequest 是 SecretService.BatchCreate 的入参。
//
// 不再有顶层 folderId(template folder 概念已弃用);每条 item 内部自带
// (envCode, folderId, value) 三元组,客户端显式指定每个 env 的目标 folder。
type BatchCreateRequest struct {
	SecretList []BatchCreateSecretSpec
}

// BatchCreateSecretSpec 单条 secret 的批量创建规格:
//
//	Key     string
//	Comment string
//	Envs    []BatchCreateEnvTarget // 按 dev/test/sim/prod 顺序展开
type BatchCreateSecretSpec struct {
	Key     string
	Comment string
	Envs    []BatchCreateEnvTarget
}

// BatchCreateEnvTarget 单个 env 下的「目标 folder + 待写入 value」三元组。
// EnvCode 仅为审计 / 日志 / 缓存键标识使用,不影响 repo 写入(repo 只看 FolderId)。
type BatchCreateEnvTarget struct {
	EnvCode  string
	FolderId string
	Value    string
}

// secretServiceKeyPattern 与 controller 端 secretKeyPattern 同源;service 入口做防御性二次校验,
// 防止 controller 被绕过/直调 service 时漏校验。
var secretServiceKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type secretService struct {
	repo       store.ResourceRepository
	encryptor  secretcrypto.Encryptor
	cache      store.SecretCache // 可为 nil
	authorizer auth.Authorizer
}

func NewSecretService(repo store.ResourceRepository, encryptor secretcrypto.Encryptor, cache store.SecretCache, authorizer auth.Authorizer) SecretService {
	return &secretService{repo: repo, encryptor: encryptor, cache: cache, authorizer: authorizer}
}

// secretFolderScope / secretSecretScope 帮 service 内生成 auth.Resource,
// 避免每个方法都写 `auth.Resource{Type: "secret", Id: id}` 的样板。
func secretFolderScope(folderId string) auth.Resource {
	return auth.Resource{Type: "folder", Id: folderId}
}
func secretEnvironmentScope(envId string) auth.Resource {
	return auth.Resource{Type: "environment", Id: envId}
}
func secretSecretScope(id string) auth.Resource { return auth.Resource{Type: "secret", Id: id} }

func (s *secretService) Create(ctx context.Context, user auth.UserInfo, folderId, key, value, comment, actor string) (domain.Secret, error) {
	if err := s.authorizer.Allow(ctx, user, "secret:create", secretFolderScope(folderId)); err != nil {
		return domain.Secret{}, err
	}
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

func (s *secretService) Update(ctx context.Context, user auth.UserInfo, id, key, value, comment, actor string) (domain.Secret, error) {
	if err := s.authorizer.Allow(ctx, user, "secret:update", secretSecretScope(id)); err != nil {
		return domain.Secret{}, err
	}
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

func (s *secretService) Delete(ctx context.Context, user auth.UserInfo, id, actor string) error {
	if err := s.authorizer.Allow(ctx, user, "secret:delete", secretSecretScope(id)); err != nil {
		return err
	}
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

func (s *secretService) Get(ctx context.Context, user auth.UserInfo, id string) (domain.Secret, error) {
	if err := s.authorizer.Allow(ctx, user, "secret:read", secretSecretScope(id)); err != nil {
		return domain.Secret{}, err
	}
	return s.repo.GetSecret(ctx, id)
}

func (s *secretService) Reveal(ctx context.Context, user auth.UserInfo, id, actor string) (domain.Secret, error) {
	if err := s.authorizer.Allow(ctx, user, "secret:reveal", secretSecretScope(id)); err != nil {
		return domain.Secret{}, err
	}
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
//
// 注意:先 GetByPath 拿到 secret.id,再走 secret:read 校验。
// 侧信道:无权限用户可以通过 200/403 时延差推断存在性(与 /secret/info 一致),
// 后续若要严格防探测,可在 allowScope 失败时把 403 改 404。
func (s *secretService) GetByPath(ctx context.Context, user auth.UserInfo, path string) (domain.Secret, error) {
	orgCode, projectCode, envCode, folderCode, key, err := parseSecretPath(path)
	if err != nil {
		return domain.Secret{}, err
	}
	secret, err := s.repo.GetSecretByPath(ctx, orgCode, projectCode, envCode, folderCode, key)
	if err != nil {
		return domain.Secret{}, err
	}
	if err := s.authorizer.Allow(ctx, user, "secret:read", secretSecretScope(secret.Id)); err != nil {
		return domain.Secret{}, err
	}
	return secret, nil
}

// RevealByPath 先按路径解析拿 id,再走 secret:reveal 校验 + 现有 Reveal 加密 + 审计链路。
func (s *secretService) RevealByPath(ctx context.Context, user auth.UserInfo, path, actor string) (domain.Secret, error) {
	orgCode, projectCode, envCode, folderCode, key, err := parseSecretPath(path)
	if err != nil {
		return domain.Secret{}, err
	}
	secret, err := s.repo.GetSecretByPath(ctx, orgCode, projectCode, envCode, folderCode, key)
	if err != nil {
		return domain.Secret{}, err
	}
	if err := s.authorizer.Allow(ctx, user, "secret:reveal", secretSecretScope(secret.Id)); err != nil {
		return domain.Secret{}, err
	}
	return s.Reveal(ctx, user, secret.Id, actor)
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

// parseFolderPath 把 "org.project.env.folder" 拆成 4 段(无 KEY),任一段为空或段数不对都报错。
// 与 parseSecretPath 共享同样的校验规则(非空 / trim / 全段必填)。
func parseFolderPath(path string) (org, proj, env, folder string, err error) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	if len(parts) != 4 {
		return "", "", "", "", fmt.Errorf("invalid folder path: expected org.project.env.folder, got %q", path)
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return "", "", "", "", fmt.Errorf("invalid folder path: empty segment in %q", path)
		}
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}

// BatchRevealByPath 按 folder 路径 + 可选 keys 列表批量 reveal。
//
// - 空 user.UserId → ErrPermissionDenied(service 防御,同 List/Search);
// - 空 path / 段数错 → parseFolderPath 错误;
// - 走 v7 cascade narrowing(secret:reveal):repo SQL 一次性按 (secret, folder, env, project, org) 链收窄;
// - 解密后填 Secret.Value;
// - 整批 1 条 audit(resource_type=folder, action=reveal_batch, encrypted_value=keys 列表);
//
// 返回 (secrets, notFound, err):
//   - secrets: 命中并解密的 secret 列表(按 key ASC);
//   - notFound: 当 request keys 非空时,request keys ∖ 命中 keys(顺序按 request 出现顺序);
//
// request keys 为空时 notFound 一定为 nil(无对照,避免误导调用方)。
func (s *secretService) BatchRevealByPath(ctx context.Context, user auth.UserInfo, path string, keys []string, actor string) ([]domain.Secret, []string, error) {
	if strings.TrimSpace(user.UserId) == "" {
		return nil, nil, auth.ErrPermissionDenied
	}
	orgCode, projectCode, envCode, folderCode, err := parseFolderPath(path)
	if err != nil {
		return nil, nil, err
	}
	// 兜底:keys 为 nil 时传空 slice 给 repo,避免 SQL 中 cardinality(NULL::text[]) 返 NULL 导致整批空返。
	// 空 slice 走 `cardinality('{}'::text[]) = 0` 真值,SQL 走「不限」分支。
	if keys == nil {
		keys = []string{}
	}
	secrets, ciphertexts, err := s.repo.BatchRevealSecretsByPath(ctx, user.UserId, "secret:reveal", orgCode, projectCode, envCode, folderCode, keys)
	if err != nil {
		return nil, nil, err
	}
	// 解密 + 填 Value
	results := make([]domain.Secret, 0, len(secrets))
	for i, secret := range secrets {
		var ciphertext domain.SecretCiphertext
		if err := json.Unmarshal(ciphertexts[i], &ciphertext); err != nil {
			return nil, nil, fmt.Errorf("decode secret ciphertext: %w", err)
		}
		plaintext, err := s.decrypt(ctx, ciphertext)
		if err != nil {
			return nil, nil, err
		}
		secret.Value = plaintext
		results = append(results, secret)
	}

	// 整批 1 条 audit;无命中(空 results)时不写。
	if len(results) > 0 {
		// 记录「实际命中的 keys」,keys 请求为空时填实际命中的 keys 列表(便于审计反查)。
		auditKeys := keys
		if len(auditKeys) == 0 {
			auditKeys = make([]string, 0, len(results))
			for _, sec := range results {
				auditKeys = append(auditKeys, sec.Key)
			}
		}
		// 任意一个命中 secret 共享同一个 folder,取第一个的 FolderId 作为 audit resource。
		payload, err := json.Marshal(auditKeys)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal audit keys: %w", err)
		}
		if err := s.repo.RecordAudit(ctx, actor, "folder", results[0].FolderId, "reveal_batch", payload); err != nil {
			return nil, nil, fmt.Errorf("record reveal_batch audit: %w", err)
		}
	}

	// 计算 notFound:request keys ∖ 命中 keys(只在 request keys 非空时计算)
	var notFound []string
	if len(keys) > 0 {
		hit := make(map[string]struct{}, len(results))
		for _, sec := range results {
			hit[sec.Key] = struct{}{}
		}
		for _, k := range keys {
			if _, ok := hit[k]; !ok {
				notFound = append(notFound, k)
			}
		}
	}
	return results, notFound, nil
}

// BatchRevealByCode 编排流程(与 BatchRevealByPath 等价):
//  1. 空 user.UserId → auth.ErrPermissionDenied
//  2. 4 个 code 任一为空 → 客户端应在 controller 层挡掉,service 层仍做 trim 防御
//  3. 走 v7 cascade narrowing(secret:reveal):repo SQL 一次性按 (secret, folder, env, project, org) 链收窄
//  4. 解密后填 Secret.Value
//  5. 整批 1 条 audit(action=reveal_batch);无命中时不写
//
// 与 BatchRevealByPath 的唯一区别:keys=nil 永远传 nil(无 keys 过滤),不计算 notFound。
func (s *secretService) BatchRevealByCode(ctx context.Context, user auth.UserInfo, orgCode, projectCode, envCode, folderCode, actor string) ([]domain.Secret, error) {
	if strings.TrimSpace(user.UserId) == "" {
		return nil, auth.ErrPermissionDenied
	}
	if strings.TrimSpace(orgCode) == "" || strings.TrimSpace(projectCode) == "" ||
		strings.TrimSpace(envCode) == "" || strings.TrimSpace(folderCode) == "" {
		return nil, fmt.Errorf("orgCode, projectCode, environmentCode, folderCode are all required")
	}
	// 复用 BatchRevealSecretsByPath,keys=nil → 走「不限」分支,返回 folder 下所有 secret
	secrets, ciphertexts, err := s.repo.BatchRevealSecretsByPath(ctx, user.UserId, "secret:reveal", orgCode, projectCode, envCode, folderCode, nil)
	if err != nil {
		return nil, err
	}
	results := make([]domain.Secret, 0, len(secrets))
	for i, secret := range secrets {
		var ciphertext domain.SecretCiphertext
		if err := json.Unmarshal(ciphertexts[i], &ciphertext); err != nil {
			return nil, fmt.Errorf("decode secret ciphertext: %w", err)
		}
		plaintext, err := s.decrypt(ctx, ciphertext)
		if err != nil {
			return nil, err
		}
		secret.Value = plaintext
		results = append(results, secret)
	}
	if len(results) > 0 {
		keys := make([]string, 0, len(results))
		for _, sec := range results {
			keys = append(keys, sec.Key)
		}
		payload, err := json.Marshal(keys)
		if err != nil {
			return nil, fmt.Errorf("marshal audit keys: %w", err)
		}
		if err := s.repo.RecordAudit(ctx, actor, "folder", results[0].FolderId, "reveal_batch", payload); err != nil {
			return nil, fmt.Errorf("record reveal_batch audit: %w", err)
		}
	}
	return results, nil
}

// ListAcrossEnvs 编排流程:
//  1. 空 user.UserId → auth.ErrPermissionDenied(同 BatchRevealByPath)
//  2. 入参防御性二次校验:
//     - projectId 必填;
//     - key 非空时 folderCode 必填,且 key 需匹配 secretServiceKeyPattern;
//     - envList trim+去重+空过滤,长度 1..32
//  3. 走 v7 cascade narrowing(action=secret:read,v12 起):repo SQL 一次性按
//     (secret, folder, env, project, org) 链收窄;持有 read 即可看到明文
//     (无 read 在 SQL 层被收窄掉,不进 result)
//     - key 非空:走 ListSecretsByProjectFolderKey(精确 (folderCode, key))
//     - key 为空:走 ListSecretsInProjectByEnvs(项目下全量,service 端按 (folderCode, key) 聚合)
//  4. 解密后按 envCode 索引填到每组 SecretAcrossEnvs.Envs;未命中的 env 填 nil
//     (自定义 MarshalJSON 序列化为 null),保证响应里上送 envList 的所有 code 都有字段位
//  5. 整批 1 条 audit(action=reveal_batch, resource_type=project, resource_id=projectId);
//     无命中(空 records)时不写 audit,与 BatchRevealByPath 一致
//  6. 顶层 comment 取自该 (folder, key) 组第一个命中 env 的 comment(无命中时为空)
func (s *secretService) ListAcrossEnvs(
	ctx context.Context, user auth.UserInfo, projectId, folderCode, key string, envList []string, actor string,
) ([]*domain.SecretAcrossEnvs, error) {
	if strings.TrimSpace(user.UserId) == "" {
		return nil, auth.ErrPermissionDenied
	}
	projectId = strings.TrimSpace(projectId)
	folderCode = strings.TrimSpace(folderCode)
	key = strings.TrimSpace(key)
	if projectId == "" {
		return nil, errors.New("projectId is required")
	}
	// key 非空 → 校验格式 + folderCode 必填(单点查询需要唯一标识)
	if key != "" {
		if !secretServiceKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("key must match ^[A-Z][A-Z0-9_]*$")
		}
		if folderCode == "" {
			return nil, errors.New("folderCode is required when key is provided")
		}
	}
	// envList 防御性 trim + 去重 + 过滤空串
	seen := make(map[string]struct{}, len(envList))
	cleaned := make([]string, 0, len(envList))
	for _, e := range envList {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		cleaned = append(cleaned, e)
	}
	if len(cleaned) == 0 {
		return nil, errors.New("envList must contain at least one non-empty env code")
	}
	if len(cleaned) > 32 {
		return nil, errors.New("envList exceeds max length 32")
	}

	// 拉取 secret rows;key 为空走「全量」分支,key 非空走「精确」分支。
	// folderCode 是独立的过滤维度:空时不限 folder,非空时 SQL 限定到该 folder。
	//
	// 权限:v12 起 action 改为 secret:read(原 secret:reveal)。
	// 语义:有 read 权限即有资格看到 secret 的明文;无 read 权限的 secret 在
	// SQL 层就被收窄掉(不出现在 result 里),service 不需要再单独 check reveal。
	// 这把"是否能看到明文"从细粒度的 reveal 降级为粗粒度的 read——前端
	// /secrets/list 是浏览场景,持有 read 即可直接看到 plaintext,不需要先
	// grant 一次 reveal;单点 /secret/reveal 接口仍走 reveal 权限,保持细粒度
	// 控制。
	var (
		secrets     []domain.Secret
		ciphertexts [][]byte
		err         error
	)
	if key == "" {
		secrets, ciphertexts, err = s.repo.ListSecretsInProjectByEnvs(
			ctx, user.UserId, "secret:read", projectId, folderCode, cleaned,
		)
	} else {
		secrets, ciphertexts, err = s.repo.ListSecretsByProjectFolderKey(
			ctx, user.UserId, "secret:read", projectId, folderCode, key, cleaned,
		)
	}
	if err != nil {
		return nil, err
	}

	// 按 (folderCode, key) 聚合为多组 SecretAcrossEnvs。
	// 每组内部按 envCode 索引填 Value;envelope 用上送 cleaned 初始化所有 env code,
	// 缺失为 nil → JSON null,前端可按固定下标访问。
	type groupKey struct {
		folderCode string
		key        string
	}
	type groupState struct {
		projectCode string
		comment     string
		envs        map[string]*domain.EnvSecretValue
		hasHit      bool
	}
	groups := make(map[groupKey]*groupState)
	order := make([]groupKey, 0)
	projectCode := ""
	for i, secret := range secrets {
		if projectCode == "" {
			projectCode = secret.ProjectCode
		}
		gk := groupKey{folderCode: secret.FolderCode, key: secret.Key}
		g, ok := groups[gk]
		if !ok {
			g = &groupState{
				projectCode: secret.ProjectCode,
				envs:        make(map[string]*domain.EnvSecretValue, len(cleaned)),
				hasHit:      false,
			}
			// 预填上送 envList 的所有 code → nil(未命中位置)
			for _, code := range cleaned {
				g.envs[code] = nil
			}
			groups[gk] = g
			order = append(order, gk)
		}
		if !g.hasHit {
			g.comment = secret.Comment
			g.hasHit = true
		}
		var ct domain.SecretCiphertext
		if err := json.Unmarshal(ciphertexts[i], &ct); err != nil {
			return nil, fmt.Errorf("decode secret ciphertext: %w", err)
		}
		plaintext, err := s.decrypt(ctx, ct)
		if err != nil {
			return nil, err
		}
		g.envs[secret.EnvironmentCode] = &domain.EnvSecretValue{
			FolderId:  secret.FolderId,
			Value:     plaintext,
			Version:   secret.Version,
			Comment:   secret.Comment,
			UpdatedAt: secret.UpdatedAt,
		}
	}

	// 整批 1 条 audit;无命中(空 groups)时不写。
	if len(order) > 0 {
		// totalHits 统计所有 (folder, key) 组里的 env 命中数之和(用于反查这次 reveal 的「实际值数」)
		totalHits := 0
		for _, gk := range order {
			for _, v := range groups[gk].envs {
				if v != nil {
					totalHits++
				}
			}
		}
		var payload map[string]any
		if key == "" {
			// key 为空:列出命中的 (folderCode, key) 列表,便于审计反查
			items := make([]map[string]any, 0, len(order))
			for _, gk := range order {
				items = append(items, map[string]any{
					"folderCode": gk.folderCode,
					"key":        gk.key,
				})
			}
			payload = map[string]any{
				"key":       "",
				"envList":   cleaned,
				"keyCount":  len(order),
				"totalHits": totalHits,
				"items":     items,
			}
		} else {
			payload = map[string]any{
				"folderCode": folderCode,
				"key":        key,
				"envList":    cleaned,
				"hits":       totalHits,
			}
		}
		raw, _ := json.Marshal(payload)
		if err := s.repo.RecordAudit(ctx, actor, "project", projectId, "reveal_batch", raw); err != nil {
			return nil, fmt.Errorf("record reveal_batch audit: %w", err)
		}
	}

	// 序列化结果
	out := make([]*domain.SecretAcrossEnvs, 0, len(order))
	for _, gk := range order {
		g := groups[gk]
		out = append(out, &domain.SecretAcrossEnvs{
			ProjectCode: g.projectCode,
			Key:         gk.key,
			Comment:     g.comment,
			Envs:        g.envs,
		})
	}
	// key 非空但无命中时,也要返 1 元素(顶层 key/comment/全部 env=null),便于前端占位
	if len(out) == 0 && key != "" {
		envs := make(map[string]*domain.EnvSecretValue, len(cleaned))
		for _, code := range cleaned {
			envs[code] = nil
		}
		out = append(out, &domain.SecretAcrossEnvs{
			ProjectCode: projectCode,
			Key:         key,
			Comment:     "",
			Envs:        envs,
		})
	}
	return out, nil
}

// BatchCreate 编排流程:
//
//  1. 空 user.UserId → auth.ErrPermissionDenied(controller 翻译为权限不足)
//  2. 入参防御性二次校验(secretList 非空、每条 key 合法、每条 env 非空)
//  3. 展开 secretList 为 (envCode, folderId, value, key, comment) 序列;
//     对每条 (target) 做 secret:create 权限 check(走 v7 cascade narrowing)。
//     任一权限不足 → 整批拒绝,不写库。
//  4. 对每条 target 加密 value;
//  5. 调 repo.BatchCreateSecrets 单事务批量 INSERT + 1 条 batch audit;
//     unique violation(23505)由 translatePgErr 翻译为 domain.ErrConflict 透传;
//  6. commit 后逐条 cacheUpsert(同 Create);
//
// 所有错误一律以 err 形式返出(controller 统一翻译为 HTTP 200 + body code=-1 +
// msg 描述);成功时只返 nil。
func (s *secretService) BatchCreate(ctx context.Context, user auth.UserInfo, req BatchCreateRequest, actor string) error {
	if strings.TrimSpace(user.UserId) == "" {
		return auth.ErrPermissionDenied
	}
	// 1. 入参防御性二次校验(controller 已做,这里兜底直调 service 的场景)。
	if len(req.SecretList) == 0 {
		return errors.New("secretList 不能为空")
	}
	for i, item := range req.SecretList {
		if !secretServiceKeyPattern.MatchString(item.Key) {
			return fmt.Errorf("secretList[%d].key 格式错误,必须匹配 ^[A-Z][A-Z0-9_]*$", i)
		}
		if len(item.Envs) == 0 {
			return fmt.Errorf("secretList[%d] 至少需要指定一个 env", i)
		}
		for j, e := range item.Envs {
			if strings.TrimSpace(e.FolderId) == "" {
				return fmt.Errorf("secretList[%d].envs[%d].folderId 不能为空", i, j)
			}
		}
	}

	// 2. 展开为有序的 (envCode, folderId, value, key, comment) target 列表。
	type target struct {
		envCode    string
		folderId   string
		key        string
		comment    string
		ciphertext domain.SecretCiphertext
	}
	var targets []target
	for _, item := range req.SecretList {
		for _, e := range item.Envs {
			ct, err := s.encrypt(ctx, e.Value)
			if err != nil {
				return err
			}
			targets = append(targets, target{
				envCode:    e.EnvCode,
				folderId:   e.FolderId,
				key:        item.Key,
				comment:    item.Comment,
				ciphertext: ct,
			})
		}
	}

	// 3. 权限 check:对每个 target folder 单独 secret:create 判定。
	// 任一缺失即整批拒绝(严控,避免半写库)。
	for _, t := range targets {
		if err := s.authorizer.Allow(ctx, user, "secret:create", secretFolderScope(t.folderId)); err != nil {
			return fmt.Errorf("对 folder %s (env=%s) 没有 secret:create 权限: %w", t.folderId, t.envCode, err)
		}
	}

	// 4. 构造 store 输入,调 repo.BatchCreateSecrets。
	items := make([]store.BatchCreateSecretItem, 0, len(targets))
	for _, t := range targets {
		items = append(items, store.BatchCreateSecretItem{
			FolderId:   t.folderId,
			Key:        t.key,
			Comment:    t.comment,
			Actor:      actor,
			Ciphertext: t.ciphertext,
		})
	}
	created, err := s.repo.BatchCreateSecrets(ctx, items)
	if err != nil {
		// 透传 ErrConflict / ErrNotFound / 其他 err;translatePgErr 已做 unique violation → ErrConflict。
		return err
	}

	// 5. 同步 cache:targets 顺序与 created 顺序一致,逐对 upsert。
	for i, t := range targets {
		s.cacheUpsert(ctx, created[i], t.ciphertext)
	}
	return nil
}

// listScope 实现 List / Search 共享的"最深 scope"策略:
// FolderId 优先 → folder scope;否则 EnvironmentId → environment scope。
// RBAC 走 folder/secret 继承链(folder → env → project → org),由 ResourceScopes 自行展开。
// "search" 权限码比 "list" 权限码高一档(跨 folder 检索,语义更宽),
// 所以由 caller 显式选择。
//
// v7 起,list/search 的"入口"权限判定已下沉到 repo SQL narrowing 自身;
// 这里仍保留 helper 给其他单点路径(目前没有 caller),若未来需要可重新启用。
// 已不再被 List / Search 调用。
func (s *secretService) listScope(ctx context.Context, user auth.UserInfo, action string, filter domain.ListFilter) error {
	if filter.FolderId != "" {
		return s.authorizer.Allow(ctx, user, action, secretFolderScope(filter.FolderId))
	}
	if filter.EnvironmentId != "" {
		return s.authorizer.Allow(ctx, user, action, secretEnvironmentScope(filter.EnvironmentId))
	}
	return errors.New("listScope: either FolderId or EnvironmentId is required")
}

// List 按 caller 的 user_role_bindings 自动收窄可见 secret(scope 链:
// secret > folder > env > project > org);caller 无 binding → 返空 list。
// cache 不感知 user,无法收窄,List 走 repo 直查。
//
// v11 起和 Search 一样复用 narrowSecretSearchScope:folder > env > project 三选一,
// 全空走 RBAC 收窄后的全量。响应只填 metadata(无 Values 字段),与 Search 的区别仅
// 在 action("secret:list" vs "secret:search")。
func (s *secretService) List(ctx context.Context, user auth.UserInfo, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error) {
	if strings.TrimSpace(user.UserId) == "" {
		return domain.PaginatedResult[domain.Secret]{}, auth.ErrPermissionDenied
	}
	filter = narrowSecretSearchScope(filter)
	return s.repo.ListSecrets(ctx, user.UserId, "secret:list", filter, pagination)
}

// Search 同 List,差别只在 action("secret:search")。
// 同样不走 cache(cache 不感知 user);keyword 模糊搜索本就走 DB 索引,影响可控。
//
// 优先级收敛(folder > env > project):caller 一次性传多个 scope id 时,只取最细
// 的一个,其他忽略。空 keyword 表示"该 scope 内全量"——repo 的 `($5 = ” or ...)`
// 已经天然支持,这里不用特判。
//
// v12:scope=project 时,响应按 (folderCode, key) 聚合为 *domain.SecretGroup(顶层
// key 字段 = secret key,与数据库 secrets.key 字段名对齐,后续每个 envCode 展
// 开为一个完整 Secret 元数据,**内层不填 values / value 字段**——project 维
// 度的 search 是「跨 env 浏览」语义,明文走 reveal 单点接口);env/folder scope
// 时,响应保持 []domain.Secret(Values 单 entry map,沿用 v11 形态)。返回类型
// 用 PaginatedResult[any] 统一承载,JSON 由各 item 类型的 MarshalJSON/Marshal
// 决定。env/folder 维度的 value 填充仍受 secret:reveal 权限约束——无权限或
// 无 ciphertext 时填空串。
func (s *secretService) Search(ctx context.Context, user auth.UserInfo, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[any], error) {
	if strings.TrimSpace(user.UserId) == "" {
		return domain.PaginatedResult[any]{}, auth.ErrPermissionDenied
	}
	filter = narrowSecretSearchScope(filter)

	records, err := s.repo.ListSecretsWithCiphertext(ctx, user.UserId, "secret:search", filter, pagination)
	if err != nil {
		return domain.PaginatedResult[any]{}, err
	}

	if filter.ProjectId != "" {
		groups, total, err := s.aggregateSecretsByProject(records, pagination)
		if err != nil {
			return domain.PaginatedResult[any]{}, err
		}
		items := make([]any, 0, len(groups))
		for i := range groups {
			items = append(items, groups[i])
		}
		return domain.PaginatedResult[any]{Items: items, Total: total}, nil
	}

	secrets, err := s.fillValuesPerSecret(ctx, user, records)
	if err != nil {
		return domain.PaginatedResult[any]{}, err
	}
	items := make([]any, 0, len(secrets.Items))
	for i := range secrets.Items {
		items = append(items, secrets.Items[i])
	}
	return domain.PaginatedResult[any]{Items: items, Total: secrets.Total}, nil
}

// narrowSecretSearchScope 按 folder > env > project 优先级收敛三选一,空 scope
// 全保留为兜底(全量走 RBAC narrowing)。先 trim 全部 scope 字段,再按优先级收敛,
// 避免 repo 收到纯空白字符串去撞 UUID 列。
func narrowSecretSearchScope(filter domain.ListFilter) domain.ListFilter {
	filter.FolderId = strings.TrimSpace(filter.FolderId)
	filter.EnvironmentId = strings.TrimSpace(filter.EnvironmentId)
	filter.ProjectId = strings.TrimSpace(filter.ProjectId)
	switch {
	case filter.FolderId != "":
		filter.EnvironmentId = ""
		filter.ProjectId = ""
	case filter.EnvironmentId != "":
		filter.ProjectId = ""
	}
	return filter
}

// fillValuesPerSecret 用于 env/folder 维度的 search:响应保持 1 行 1 secret(原
// 状),Values 是单 entry map {envCode: value}。每条 secret 的值受 secret:reveal
// 权限约束,无权限时填 ""。
func (s *secretService) fillValuesPerSecret(
	ctx context.Context,
	user auth.UserInfo,
	records domain.PaginatedResult[domain.SecretCacheRecord],
) (domain.PaginatedResult[domain.Secret], error) {
	items := make([]domain.Secret, 0, len(records.Items))
	for i := range records.Items {
		rec := records.Items[i]
		plaintext, err := s.revealIfPermitted(ctx, user, rec.Secret.Id, rec.ValueCiphertext)
		if err != nil {
			return domain.PaginatedResult[domain.Secret]{}, err
		}
		if plaintext != "" || rec.Secret.EnvironmentCode != "" {
			rec.Secret.Values = map[string]string{rec.Secret.EnvironmentCode: plaintext}
		}
		items = append(items, rec.Secret)
	}
	return domain.PaginatedResult[domain.Secret]{Items: items, Total: records.Total}, nil
}

// aggregateSecretsByProject 用于 project 维度的 search:repo 已经把匹配 secret
// 按 (key asc, env code asc) 排好,我们按 (folderCode, key) 滚动聚合,每组
// 生成一个 *domain.SecretGroup,Envs 累积为 {envCode: <Secret metadata>, ...}。
//
// 关键设计:分组 key 用 (folderCode, key) 而非 (folderId, key)。原因是
// level-1 folder 在每个 env 下都有一个独立的 folderId(env 是 folder 的父,
// folder 行同时持 environment_id 字段,跨 env 不复用 id),所以同一「逻辑
// folder」在不同 env 是 4 个不同 folderId 行;只有 folderCode 在「同一
// folder 跨 env」语义下是稳定的。grouping by folderCode 才能把
// dev/test/sim/prod 下 folderCode="ana-svc"、key="URL" 的 4 条 secret
// 聚合成 1 个 group,响应形如 {key: "URL", dev: {...}, test: {...}, ...}。
//
// 如果将来需要支持 level=2 folder(其 folderCode 跨 env 也可能相同),分
// 组 key 仍能正确工作,因为 level=2 folder 的 folderCode 在「同一逻辑
// folder 跨 env」下也是稳定标识;若是同 env 下两个 level=2 folder 同
// code,那是数据模型冲突(folder code 唯一约束在 (env, parent_id, code)
// 上,同 env 下同 code 是 unique violation),不会出现在结果集里。
//
// 输出 JSON 形如 { "key": "<key>", "dev": {<Secret>}, "test": {<Secret>}, ... }
// (envCode 由 SecretGroup.MarshalJSON 展平到顶层);顶层 "key" 字段名与数据库
// secrets.key 对齐。每个 env 一条完整 Secret 元数据(包含 path / 4 级 codes /
// version / audit 字段)。**不在内层 Secret 上填 values / value 字段**——
// project 维度的 search 是「跨 env 浏览」语义,前端想要的是「同一 key 在不
// 同 env 的存在性 + 元数据对比」,不是「明文值」;明文走 reveal 单点接口
// (同 id)或 batchRevealByPath(同 folder)。所以这里不做 reveal 权限 check
// 也不解密,ValueCiphertext 字段直接忽略(repo 仍走 ListSecretsWithCiphertext
// 一次拿全 metadata + ciphertext,主要是为了不引入第二条 SQL 路径,牺牲一点
// 点 IO 换实现简单;如要优化可改成 ListSecrets,见 List 方法)。
//
// 分页:repo 返回的 records 已经是 paginated rows;聚合后的"组"数量会少于
// 或等于 records 数量,这里直接在 Go 里截断 limit / offset,牺牲一点对超
// 大项目的可扩展性换实现简单。后续如要支持 10k+ secret 的项目,可以把分组
// 下推到 SQL(GROUP BY + json_object_agg 等)。
func (s *secretService) aggregateSecretsByProject(
	records domain.PaginatedResult[domain.SecretCacheRecord],
	pagination domain.Pagination,
) ([]*domain.SecretGroup, int64, error) {
	type groupKey struct {
		folderCode string
		key        string
	}

	groups := make(map[groupKey]*domain.SecretGroup, len(records.Items))
	order := make([]groupKey, 0, len(records.Items))

	for i := range records.Items {
		rec := records.Items[i]
		// 关键:用 folderCode 而非 folderId 分组,这样「同一逻辑 folder 跨 env」的
		// 4 条 secret 才能聚合成 1 个 group(folderId 跨 env 一定不同,会拆成 4 组)。
		k := groupKey{folderCode: rec.Secret.FolderCode, key: rec.Secret.Key}
		g, ok := groups[k]
		if !ok {
			// 同组内所有 env 共享同一个 key,顶层 key 字段就用这个 secret key(与
			// 数据库 secrets.key 字段名对齐,前端无歧义对应)。
			g = &domain.SecretGroup{
				Key:  rec.Secret.Key,
				Envs: map[string]domain.Secret{},
			}
			groups[k] = g
			order = append(order, k)
		}
		if rec.Secret.EnvironmentCode != "" {
			// 内层 Secret 保留 repo 返回的 metadata 原样:不填 Values、不解密密文。
			// 上一层 caller 不会用到 envSecret.Value / Values(omitempty 也会让
			// 这两个空字段不出现于 JSON),所以无后续副作用。
			g.Envs[rec.Secret.EnvironmentCode] = rec.Secret
		}
	}

	all := make([]*domain.SecretGroup, 0, len(order))
	for _, k := range order {
		all = append(all, groups[k])
	}

	total := records.Total
	if total == 0 {
		total = int64(len(all))
	}

	limit := pagination.Limit()
	offset := pagination.Offset()
	if offset > len(all) {
		offset = len(all)
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

// revealIfPermitted 对单条 secret 做 secret:reveal 判定 + 解密,封装 N+1
// 的小循环:无权限 / 无 ciphertext / 解密失败 一律返回空串(不阻断搜索)。
func (s *secretService) revealIfPermitted(
	ctx context.Context,
	user auth.UserInfo,
	secretId string,
	ciphertext []byte,
) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if err := s.authorizer.Allow(ctx, user, "secret:reveal", secretSecretScope(secretId)); err != nil {
		if errors.Is(err, auth.ErrPermissionDenied) {
			return "", nil
		}
		return "", err
	}
	var ct domain.SecretCiphertext
	if err := json.Unmarshal(ciphertext, &ct); err != nil {
		return "", nil
	}
	plain, err := s.decrypt(ctx, ct)
	if err != nil {
		return "", nil
	}
	return string(plain), nil
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
