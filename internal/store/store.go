// Package store defines the data-access interfaces used by the service
// layer. Concrete implementations live in subpackages (postgres, redis).
//
// Design rules:
//   - every method takes context.Context as the first parameter
//   - returns domain.* types only, never database/sql or driver-specific types
//   - returns domain.ErrNotFound when a row is missing or soft-deleted
//   - implementations own their own transactions; callers must compose higher
//     level transactions via the exposed Tx-scoped methods if/when needed
package store

import (
	"context"
	"time"

	"envVault/internal/auth"
	"envVault/internal/domain"
)

// ResourceRepository 持久化 organization / project / environment /
// environment_template / folder / secret / audit 实体。
//
// 实现细节(SQL、事务)由具体实现负责。本接口仅表达业务所需的最小数据
// 访问能力;业务胶水(加密、缓存、reveal 审计)在 service 层加。
type ResourceRepository interface {
	// ---- Organization ----
	CreateOrganization(ctx context.Context, code, name, comment, actor string) (domain.Entity, error)
	// ListOrganizations 按 caller 的 user_role_bindings 自动收窄可见 org(scope 链:global > organization);
	// 无 binding 的 user 拿到空 list,不返 403(隐式空 list 语义)。
	ListOrganizations(ctx context.Context, callerUserId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	GetOrganization(ctx context.Context, id string) (domain.Entity, error)
	GetOrganizationByCode(ctx context.Context, code string) (domain.Entity, error)
	UpdateOrganization(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	// DeleteOrganization 级联软删 4 级;返回的 CascadeScope 包含所有被软删的下游
	// id,handler 用来同步 Redis cache 失效。force=false 时只删 org 自身,
	// ProjectIds/EnvironmentIds/FolderIds/SecretIds 均为空。
	DeleteOrganization(ctx context.Context, id, actor string, force bool) (domain.CascadeScope, error)

	// ---- Project ----
	CreateProject(ctx context.Context, orgId, code, name, comment, actor string, envs []domain.EnvSpec) (domain.Entity, error)
	// ListProjects 按 caller 在 (project, organization) 层的 binding 收窄;chain 向上 cascade。
	ListProjects(ctx context.Context, callerUserId, orgId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	GetProject(ctx context.Context, id string) (domain.Entity, error)
	GetProjectByCode(ctx context.Context, orgId, code string) (domain.Entity, error)
	UpdateProject(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	// DeleteProject 级联软删其下 env/folder/secret;返回 CascadeScope 供 cache 同步。
	DeleteProject(ctx context.Context, id, actor string) (domain.CascadeScope, error)

	// ---- Environment ----
	CreateEnvironment(ctx context.Context, projectId, code, name, comment, actor string) (domain.Entity, error)
	// ListEnvironments 按 caller 在 (environment, project, organization) 层的 binding 收窄。
	ListEnvironments(ctx context.Context, callerUserId, projectId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	GetEnvironment(ctx context.Context, id string) (domain.Entity, error)
	GetEnvironmentByCode(ctx context.Context, projectId, code string) (domain.Entity, error)
	UpdateEnvironment(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	// DeleteEnvironment 级联软删其下 folder/secret;返回 CascadeScope 供 cache 同步。
	DeleteEnvironment(ctx context.Context, id, actor string) (domain.CascadeScope, error)

	// ---- Environment Template (org 层只读快照) ----
	// ListEnvironmentTemplates 按 caller 在 (env_template, organization) 层的 binding 收窄。
	ListEnvironmentTemplates(ctx context.Context, callerUserId, orgId string, pagination domain.Pagination) (domain.PaginatedResult[domain.EnvironmentTemplate], error)
	GetEnvironmentTemplate(ctx context.Context, id string) (domain.EnvironmentTemplate, error)
	GetEnvironmentTemplateByCode(ctx context.Context, orgId, code string) (domain.EnvironmentTemplate, error)

	// ---- Folder ----
	CreateFolder(ctx context.Context, environmentId, parentFolderId, code, name, comment, actor string, level int) (domain.Entity, error)
	// CreateFoldersAcrossEnvs 批量跨环境创建 level=2 folder(子 folder):
	//   - parentCode 必填,作为参考父 folder 的 code;在每个 envId 下反查同 code 的
	//     level=1 sibling parent folder,挂子 folder 于此
	//   - envIds 是 env id 列表(UUID);任一 env 不存在 → 整批回滚
	//   - 任一 env 下 sibling parent 不存在 / 目标子 code 已存在 → 整批回滚
	//   - 返回所有创建的 Entity,顺序与 envIds 一致
	CreateFoldersAcrossEnvs(ctx context.Context, parentCode, code, name, comment, actor string, envIds []string) ([]domain.Entity, error)

	// CreateTopLevelFoldersInEnvs 批量跨环境创建 level=1 folder(顶层 folder,无父):
	//   - envIds 是 env id 列表(UUID);任一 env 不存在 → 整批回滚
	//   - 目标 code 在 (env, parent_id=NULL) 下未占用(否则 unique 冲突 → ErrConflict)
	//   - 返回所有创建的 Entity,顺序与 envIds 一致
	// 用于前端"在多个 env 下一次性创建同名顶层 folder"的批量场景;
	// parent 字段在请求体里无需提供(顶层 folder 的父是 env 本身,不由 folder 表达)。
	CreateTopLevelFoldersInEnvs(ctx context.Context, code, name, comment, actor string, envIds []string) ([]domain.Entity, error)
	// ListFolders 按 caller 在 (folder, environment, project, organization) 层的 binding 收窄。
	ListFolders(ctx context.Context, callerUserId, envId, parentId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	// ListFolderChildren 批量拉取多个父 folder 下的 level=2 子 folder;按 caller 在
	// (folder, environment, project, organization) 层的 binding 收窄。
	// 空 parentIds 直接返回空 map(不发 SQL);返回 map 始终非 nil(空 key 不出现,
	// 空数组也不会 nil)。handler 在 includeSubfolders=true 时调用,把父→子关系拼到响应里。
	ListFolderChildren(ctx context.Context, callerUserId string, parentIds []string) (map[string][]domain.Entity, error)
	GetFolder(ctx context.Context, id string) (domain.Entity, error)
	GetFolderByCode(ctx context.Context, environmentId, code string) (domain.Entity, error)
	// GetFolderContext 返回 cache 同步所需的 folder 全量上下文(envId/projectId/parentId/level)。
	// 给 handler 在 CreateFolder/UpdateFolder 后立即写入 Redis cache 时反查用。
	// 与 GetFolder 区别:不返回 Entity,只返回 4 个 folder 专属字段,SQL 走窄列,开销小。
	GetFolderContext(ctx context.Context, id string) (envId, projectId, parentId string, level int, err error)
	UpdateFolder(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	// DeleteFolder 级联软删其下 secret;返回 CascadeScope 供 cache 同步(只填 SecretIds)。
	DeleteFolder(ctx context.Context, id, actor string) (domain.CascadeScope, error)

	// ---- Tree 专用(不分页,带 caller narrowing) ----
	// ListAllOrganizationsForTree 给 TreeService 用,列 caller 可见的全量 org(无分页)。
	// 复用 userReadScopeCTE + scopeNarrowingWhere 收窄,SQL 不接 LIMIT/OFFSET。
	ListAllOrganizationsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error)
	ListAllProjectsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error)
	ListAllEnvironmentsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error)
	// ListAllFoldersForTree 返回 FolderTreeEntry 而非 Entity,因为 tree 组装需要
	// level/environmentId/parentId 3 个 folder 专属字段(Entity.ParentId 多态,不够用)。
	ListAllFoldersForTree(ctx context.Context, callerUserId string) ([]domain.FolderTreeEntry, error)

	// ListFoldersInProject 列 caller 在指定 project 下可见的所有 level=1 + level=2 folder,
	// 一次 SQL 同时返回(level=1 必返回,level=2 也返回便于 service 层组装 subFolders)。
	// RBAC narrowing 与 ListFolders 一致:在 (folder, env, project, org) 链收窄。
	// 返回顺序:按 level ASC, code ASC, environment_id ASC,保证前端遍历可重现。
	ListFoldersInProject(ctx context.Context, callerUserId, projectId string) ([]domain.FolderInProject, error)

	// ---- Secret ----
	CreateSecret(ctx context.Context, folderId, key, comment, actor string, ciphertext domain.SecretCiphertext) (domain.Secret, error)
	// BatchCreateSecrets 在单事务里完成 N 条 INSERT + 1 条 batch audit,
	// 全成功 commit;任一条 INSERT 失败(unique violation 等)→ 整批 rollback,
	// 错误透传(service 层翻译为 1409)。每条插入单独 RETURNING id,
	// commit 后用 r.GetSecret 拿完整 metadata(带 path/codes)。
	// 输入 items 长度应等于 N,顺序与输出 secrets 一致。
	BatchCreateSecrets(ctx context.Context, items []BatchCreateSecretItem) ([]domain.Secret, error)
	GetSecret(ctx context.Context, id string) (domain.Secret, error)
	GetSecretByKey(ctx context.Context, folderId, key string) (domain.Secret, error)
	GetSecretByPath(ctx context.Context, orgCode, projectCode, envCode, folderCode, key string) (domain.Secret, error)
	GetSecretCiphertext(ctx context.Context, id string) (domain.Secret, domain.SecretCiphertext, error)
	// ListSecrets 按 caller 在 (secret, folder, environment, project, organization) 层的 binding 收窄;
	// action 是 caller 调的权限码("secret:list" 或 "secret:search"),用于 CTE 内匹配 role_permissions.code。
	ListSecrets(ctx context.Context, callerUserId, action string, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error)
	// ListSecretsWithCiphertext 同 ListSecrets,但额外返回 value_ciphertext(以 SecretCacheRecord
	// 形式),专供 service.Search 填 Values 字段——避免先 list 拿 id 再批量取 ciphertext 的 N+1。
	// 仍按 action(默认 secret:search)做 cascade narrowing;每条 secret 的"是否可解密"
	// 仍由 service 层对每行单独 authorizer.Allow("secret:reveal") 判定。
	ListSecretsWithCiphertext(ctx context.Context, callerUserId, action string, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.SecretCacheRecord], error)
	// BatchRevealSecretsByPath 一次性按 folder 路径 + 可选 keys 列表拉取 secret 明文。
	// caller 需持有 secret:reveal 权限(v7 cascade narrowing: secret / folder / env / project / org);
	// keys 为空时返回 folder 下所有 secret(无分页、无上限)。
	// 返回 (Secret, ciphertext json) 对,长度一致;ciphertext 由 service 端解密填 Secret.Value。
	BatchRevealSecretsByPath(ctx context.Context, callerUserId, action, orgCode, projectCode, envCode, folderCode string, keys []string) ([]domain.Secret, [][]byte, error)
	// ListSecretsByProjectFolderKey 按 (project, folderCode, key) 维度 + env 过滤列表
	// 拉取 secret metadata + ciphertext,跨 env 一次性 reveal 用。
	// envCodes 为空时走"该项目下所有 env"兜底(本接口 controller 层会校验非空,
	// 此处兜底仅防直调 service 的场景)。
	// cascade narrowing 用 caller 传入的 action(secret:reveal)。
	// 返回顺序按 e.code ASC,方便 service 端按 envCode 索引。
	ListSecretsByProjectFolderKey(ctx context.Context, callerUserId, action, projectId, folderCode, key string, envCodes []string) ([]domain.Secret, [][]byte, error)
	// ListSecretsInProjectByEnvs 按 project + (可选 folderCode) + env 列表拉取 secret
	// 的 metadata + ciphertext,用于"key 为空 → 返回项目下 (folder, key)"场景。
	//   - folderCode 为空 → 走"项目下所有 folder"兜底
	//   - folderCode 非空 → SQL 直接限定到该 folder
	// 不做 (folder, key) 聚合,SQL 按 (env, folder, key) 排序返给 service 层
	// 自行 group by;envCodes 为空时走"项目下所有 env"兜底。
	// cascade narrowing 用 caller 传入的 action(secret:reveal)。
	ListSecretsInProjectByEnvs(ctx context.Context, callerUserId, action, projectId, folderCode string, envCodes []string) ([]domain.Secret, [][]byte, error)
	UpdateSecret(ctx context.Context, id, key, comment, actor string, ciphertext domain.SecretCiphertext) (domain.Secret, error)
	DeleteSecret(ctx context.Context, id, actor string) error
	ListSecretCacheRecords(ctx context.Context) ([]domain.SecretCacheRecord, error)

	// ---- Audit ----
	RecordAudit(ctx context.Context, actor, resourceType, resourceId, action string, encryptedValue []byte) error
	ListAuditRecords(ctx context.Context, resourceType, resourceId string, pagination domain.Pagination) (domain.PaginatedResult[domain.AuditRecord], error)

	// ---- User label cache (供 handler 预热,以 users.id 为 key) ----
	CacheUserLabel(userId, name string)
}

// RBACRepository 持久化 permissions / roles / users / user_role_bindings。
// 同时实现 auth.PermissionStore,供 RBAC authorizer 在请求路径上做授权检查。
type RBACRepository interface {
	auth.PermissionStore

	EnsureSystemData(ctx context.Context) error
	EnsureBootstrapAdmin(ctx context.Context, userId, name string) error

	// Permission catalog
	ListPermissions(ctx context.Context) ([]domain.Permission, error)

	// Role
	ListRoles(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Role], error)
	GetRole(ctx context.Context, id, code string) (domain.Role, error)
	CreateRole(ctx context.Context, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error)
	UpdateRole(ctx context.Context, id, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error)
	DeleteRole(ctx context.Context, id, actor string) error

	// User
	ListUsers(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.User], error)
	SyncUser(ctx context.Context, userId, name, email string) (domain.User, error)

	// Role binding
	ListRoleBindings(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	// ListRoleBindingsCascading 列 (scopeType, scopeId) 这一层以及所有下级
	// scope(organization→project→env→folder→secret)上的 active binding。
	// 入口已由 service 层校验 caller 在 (scopeType, scopeId) 或其父级持有
	// rbac:binding:read,store 只负责级联拉取。
	ListRoleBindingsCascading(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	ListUserGrants(ctx context.Context, userId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	GrantRole(ctx context.Context, userId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (domain.RoleBinding, error)
	RevokeRole(ctx context.Context, userId, roleCode, scopeType, scopeId, actor string) error
	EffectivePermissions(ctx context.Context, userId, scopeType, scopeId string) (domain.EffectivePermissions, error)
}

// SecretCache 是 secret 的 Redis 缓存层,加速 SearchSecrets 与 SecretWarmUp。
// nil 实现表示禁用缓存,所有调用方必须做 nil 检查。
type SecretCache interface {
	SearchSecrets(ctx context.Context, filter domain.ListFilter) ([]domain.Secret, error)
	UpsertSecret(ctx context.Context, record domain.SecretCacheRecord) error
	DeleteSecret(ctx context.Context, id string) error
	WarmSecrets(ctx context.Context, records []domain.SecretCacheRecord) error
}

// BatchCreateSecretItem 是 ResourceRepository.BatchCreateSecrets 的输入单元:
// 一条待插入 secret 的全部信息(folder/key/comment/actor + 已加密 ciphertext)。
// 顺序与 BatchCreateSecrets 返回的 []Secret 一一对应。
type BatchCreateSecretItem struct {
	FolderId   string
	Key        string
	Comment    string
	Actor      string
	Ciphertext domain.SecretCiphertext
}

// AuthRepository 持久化 v9 自注册 / 登录 / 强制登出 / 改密 相关数据。
//
// 与 RBACRepository 的边界:
//   - RBACRepository 管「用户是谁、能干啥」 → users.id(UUID) + role binding
//   - AuthRepository 管「用户怎么登录、何时失效」 → email + password_hash +
//     tokens_valid_after + login_attempts
//
// 两边不重叠,但都通过 users.id 关联到同一张 users 行。external_user_id
// 暂时保留为兼容字段,不作为 RBAC 授权主身份。
type AuthRepository interface {
	// ---- User with credentials ----
	// GetUserByEmail 拿 (id, external_user_id, name, password_hash, password_algo,
	// is_disabled, tokens_valid_after, last_seen_at)。
	// password_hash/password_algo 在 domain.User 上是 json:"-"(永不外泄),
	// 真实 hash 始终在 service 层 VerifyPassword 内部消化,handler 看不到。
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	// GetUserById 按内部 id (uuid) 查 user。
	GetUserById(ctx context.Context, id string) (domain.User, error)
	// GetUserByExternalId 按 external_user_id 查 user。兼容保留,新授权路径不使用。
	GetUserByExternalId(ctx context.Context, externalUserId string) (domain.User, error)
	// BumpTokensValidAfterByExternalId 兼容保留。新强制登出路径使用 BumpTokensValidAfter(userId)。
	BumpTokensValidAfterByExternalId(ctx context.Context, externalUserId string) (time.Time, error)
	// UpdatePasswordHashByExternalId 兼容保留。新改密路径使用 UpdatePasswordHash(userId, ...)。
	UpdatePasswordHashByExternalId(ctx context.Context, externalUserId, passwordHash, passwordAlgo string) (domain.User, error)
	// CreatePasswordUser 在事务里 atomic 完成:
	//   1) INSERT users (..., source='password', password_hash, password_algo)
	//   2) 若 users 表当前行数 = 0(本事务内),额外 grant platform_admin(global)
	// 返回的 User 已包含 id / external_user_id(由 repo 内部用 email 生成)。
	CreatePasswordUser(ctx context.Context, email, name, passwordHash, passwordAlgo string) (domain.User, error)
	// UpdatePasswordHash 改密。原子地:UPDATE password_hash / password_algo +
	// tokens_valid_after = NOW()(让旧 token 立即失效)。返回更新后 user。
	UpdatePasswordHash(ctx context.Context, userId, passwordHash, passwordAlgo string) (domain.User, error)
	// BumpTokensValidAfter 强制登出。UPDATE tokens_valid_after = NOW()。
	// 不需要返 user,但需要返新时间戳(给 cache 同步)。
	BumpTokensValidAfter(ctx context.Context, userId string) (time.Time, error)
	// GetTokensValidAfter 拉单个用户的 tokens_valid_after。给 cache 初始化 / 校正用。
	GetTokensValidAfter(ctx context.Context, userId string) (time.Time, error)
	// ListUsersWithTokensValidAfter 拉全量 (userId, tokensValidAfter)。
	// 进程内 tokens_cache 启动时 / 周期 refresher 用。
	ListUsersWithTokensValidAfter(ctx context.Context) (map[string]time.Time, error)
	// TouchLastSeen 登录成功后 UPDATE last_seen_at = NOW()。
	TouchLastSeen(ctx context.Context, userId string) error

	// ---- Login attempts (风控 + 审计) ----
	// RecordLoginAttempt 写一行 login_attempts。
	// userId 可空(login 失败时连 user 都不存在,userId='' 即可)。
	RecordLoginAttempt(ctx context.Context, email, ip string, success bool, userId string) error
	// CountRecentFailedByIP 统计 [now-window, now] 区间内 ip 的失败次数。
	// 给 ratelimit 用;返回的次数含本次(若调用方先 Record 再 Count)。
	CountRecentFailedByIP(ctx context.Context, ip string, window time.Duration) (int, error)
}
