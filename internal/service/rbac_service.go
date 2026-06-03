package service

import (
	"context"
	"time"

	"envVault/internal/domain"
	"envVault/internal/store"
)

// RBACService 集中 RBAC 业务编排:角色 CRUD、用户同步、binding 授权/撤销、
// 生效权限计算。底层 auth.PermissionStore 由 RBACRepository 同时实现,
// 供中间件在请求路径上做授权。
type RBACService interface {
	Bootstrap(ctx context.Context) error
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

type rbacService struct {
	repo store.RBACRepository
}

func NewRBACService(repo store.RBACRepository) RBACService {
	return &rbacService{repo: repo}
}

func (s *rbacService) Bootstrap(ctx context.Context) error {
	return s.repo.EnsureSystemData(ctx)
}

func (s *rbacService) EnsureBootstrapAdmin(ctx context.Context, externalUserId, name string) error {
	return s.repo.EnsureBootstrapAdmin(ctx, externalUserId, name)
}

func (s *rbacService) ListPermissions(ctx context.Context) ([]domain.Permission, error) {
	return s.repo.ListPermissions(ctx)
}

func (s *rbacService) ListRoles(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Role], error) {
	return s.repo.ListRoles(ctx, scopeType, scopeId, pagination)
}

func (s *rbacService) GetRole(ctx context.Context, id, code string) (domain.Role, error) {
	return s.repo.GetRole(ctx, id, code)
}

func (s *rbacService) CreateRole(ctx context.Context, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error) {
	return s.repo.CreateRole(ctx, code, name, description, scopeType, scopeId, permissions, actor)
}

func (s *rbacService) UpdateRole(ctx context.Context, id, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error) {
	return s.repo.UpdateRole(ctx, id, code, name, description, scopeType, scopeId, permissions, actor)
}

func (s *rbacService) DeleteRole(ctx context.Context, id, actor string) error {
	return s.repo.DeleteRole(ctx, id, actor)
}

func (s *rbacService) ListUsers(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.User], error) {
	return s.repo.ListUsers(ctx, scopeType, scopeId, pagination)
}

func (s *rbacService) SyncUser(ctx context.Context, externalUserId, name, email string) (domain.User, error) {
	return s.repo.SyncUser(ctx, externalUserId, name, email)
}

func (s *rbacService) ListRoleBindings(ctx context.Context, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	return s.repo.ListRoleBindings(ctx, scopeType, scopeId, pagination)
}

func (s *rbacService) ListUserGrants(ctx context.Context, externalUserId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	return s.repo.ListUserGrants(ctx, externalUserId, pagination)
}

func (s *rbacService) GrantRole(ctx context.Context, externalUserId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (domain.RoleBinding, error) {
	return s.repo.GrantRole(ctx, externalUserId, name, email, roleCode, scopeType, scopeId, expiresAt, actor)
}

func (s *rbacService) RevokeRole(ctx context.Context, externalUserId, roleCode, scopeType, scopeId, actor string) error {
	return s.repo.RevokeRole(ctx, externalUserId, roleCode, scopeType, scopeId, actor)
}

func (s *rbacService) EffectivePermissions(ctx context.Context, externalUserId, scopeType, scopeId string) (domain.EffectivePermissions, error) {
	return s.repo.EffectivePermissions(ctx, externalUserId, scopeType, scopeId)
}
