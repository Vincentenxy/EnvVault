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
	// ListFolders 按 caller 在 (folder, environment, project, organization) 层的 binding 收窄。
	ListFolders(ctx context.Context, callerUserId, envId, parentId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
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

	// ---- Secret ----
	CreateSecret(ctx context.Context, folderId, key, comment, actor string, ciphertext domain.SecretCiphertext) (domain.Secret, error)
	GetSecret(ctx context.Context, id string) (domain.Secret, error)
	GetSecretByKey(ctx context.Context, folderId, key string) (domain.Secret, error)
	GetSecretByPath(ctx context.Context, orgCode, projectCode, envCode, folderCode, key string) (domain.Secret, error)
	GetSecretCiphertext(ctx context.Context, id string) (domain.Secret, domain.SecretCiphertext, error)
	// ListSecrets 按 caller 在 (secret, folder, environment, project, organization) 层的 binding 收窄;
	// action 是 caller 调的权限码("secret:list" 或 "secret:search"),用于 CTE 内匹配 role_permissions.code。
	ListSecrets(ctx context.Context, callerUserId, action string, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error)
	// BatchRevealSecretsByPath 一次性按 folder 路径 + 可选 keys 列表拉取 secret 明文。
	// caller 需持有 secret:reveal 权限(v7 cascade narrowing: secret / folder / env / project / org);
	// keys 为空时返回 folder 下所有 secret(无分页、无上限)。
	// 返回 (Secret, ciphertext json) 对,长度一致;ciphertext 由 service 端解密填 Secret.Value。
	BatchRevealSecretsByPath(ctx context.Context, callerUserId, action, orgCode, projectCode, envCode, folderCode string, keys []string) ([]domain.Secret, [][]byte, error)
	UpdateSecret(ctx context.Context, id, key, comment, actor string, ciphertext domain.SecretCiphertext) (domain.Secret, error)
	DeleteSecret(ctx context.Context, id, actor string) error
	ListSecretCacheRecords(ctx context.Context) ([]domain.SecretCacheRecord, error)

	// ---- Audit ----
	RecordAudit(ctx context.Context, actor, resourceType, resourceId, action string, encryptedValue []byte) error
	ListAuditRecords(ctx context.Context, resourceType, resourceId string, pagination domain.Pagination) (domain.PaginatedResult[domain.AuditRecord], error)

	// ---- User label cache (供 handler 预热) ----
	CacheUserLabel(externalUserId, name string)
}

// RBACRepository 持久化 permissions / roles / users / user_role_bindings。
// 同时实现 auth.PermissionStore,供 RBAC authorizer 在请求路径上做授权检查。
type RBACRepository interface {
	auth.PermissionStore

	EnsureSystemData(ctx context.Context) error
	EnsureBootstrapAdmin(ctx context.Context, externalUserId, name string) error

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
	SyncUser(ctx context.Context, externalUserId, name, email string) (domain.User, error)

	// Role binding
	ListRoleBindings(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	ListUserGrants(ctx context.Context, externalUserId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	GrantRole(ctx context.Context, externalUserId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (domain.RoleBinding, error)
	RevokeRole(ctx context.Context, externalUserId, roleCode, scopeType, scopeId, actor string) error
	EffectivePermissions(ctx context.Context, externalUserId, scopeType, scopeId string) (domain.EffectivePermissions, error)
}

// SecretCache 是 secret 的 Redis 缓存层,加速 SearchSecrets 与 SecretWarmUp。
// nil 实现表示禁用缓存,所有调用方必须做 nil 检查。
type SecretCache interface {
	SearchSecrets(ctx context.Context, filter domain.ListFilter) ([]domain.Secret, error)
	UpsertSecret(ctx context.Context, record domain.SecretCacheRecord) error
	DeleteSecret(ctx context.Context, id string) error
	WarmSecrets(ctx context.Context, records []domain.SecretCacheRecord) error
}
