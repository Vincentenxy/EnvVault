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
	ListOrganizations(ctx context.Context, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	GetOrganization(ctx context.Context, id string) (domain.Entity, error)
	GetOrganizationByCode(ctx context.Context, code string) (domain.Entity, error)
	UpdateOrganization(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	DeleteOrganization(ctx context.Context, id, actor string, force bool) error

	// ---- Project ----
	CreateProject(ctx context.Context, orgId, code, name, comment, actor string, envs []domain.EnvSpec) (domain.Entity, error)
	ListProjects(ctx context.Context, orgId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	GetProject(ctx context.Context, id string) (domain.Entity, error)
	GetProjectByCode(ctx context.Context, orgId, code string) (domain.Entity, error)
	UpdateProject(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	DeleteProject(ctx context.Context, id, actor string) error

	// ---- Environment ----
	CreateEnvironment(ctx context.Context, projectId, code, name, comment, actor string) (domain.Entity, error)
	ListEnvironments(ctx context.Context, projectId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	GetEnvironment(ctx context.Context, id string) (domain.Entity, error)
	GetEnvironmentByCode(ctx context.Context, projectId, code string) (domain.Entity, error)
	UpdateEnvironment(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	DeleteEnvironment(ctx context.Context, id, actor string) error

	// ---- Environment Template (org 层只读快照) ----
	ListEnvironmentTemplates(ctx context.Context, orgId string, pagination domain.Pagination) (domain.PaginatedResult[domain.EnvironmentTemplate], error)
	GetEnvironmentTemplate(ctx context.Context, id string) (domain.EnvironmentTemplate, error)
	GetEnvironmentTemplateByCode(ctx context.Context, orgId, code string) (domain.EnvironmentTemplate, error)

	// ---- Folder ----
	CreateFolder(ctx context.Context, environmentId, parentFolderId, code, name, comment, actor string, level int) (domain.Entity, error)
	ListFolders(ctx context.Context, envId, parentId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Entity], error)
	GetFolder(ctx context.Context, id string) (domain.Entity, error)
	GetFolderByCode(ctx context.Context, environmentId, code string) (domain.Entity, error)
	UpdateFolder(ctx context.Context, id, name, comment, actor string) (domain.Entity, error)
	DeleteFolder(ctx context.Context, id, actor string) error

	// ---- Secret ----
	CreateSecret(ctx context.Context, folderId, key, comment, actor string, ciphertext domain.SecretCiphertext) (domain.Secret, error)
	GetSecret(ctx context.Context, id string) (domain.Secret, error)
	GetSecretByKey(ctx context.Context, folderId, key string) (domain.Secret, error)
	GetSecretByPath(ctx context.Context, orgCode, projectCode, envCode, folderCode, key string) (domain.Secret, error)
	GetSecretCiphertext(ctx context.Context, id string) (domain.Secret, domain.SecretCiphertext, error)
	ListSecrets(ctx context.Context, filter domain.ListFilter, pagination domain.Pagination) (domain.PaginatedResult[domain.Secret], error)
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
