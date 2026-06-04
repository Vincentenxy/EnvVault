package service

import (
	"context"
	"fmt"
	"time"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/store"
)

// RBACService 集中 RBAC 业务编排:角色 CRUD、用户同步、binding 授权/撤销、
// 生效权限计算。底层 auth.PermissionStore 由 RBACRepository 同时实现,
// 供中间件在请求路径上做授权。
//
// v6 起,所有方法(v6 子集)入口加 caller 校验:走 auth.Authorizer.Allow
// 统一接口。写方法叠加 4 条边界:
//   - scope 内 caller 必须持有对应 rbac:* 权限码;
//   - 被授权/创建的角色 permissions 必须 ⊆ caller 有效权限集合;
//   - caller 不是 platform_admin 时不能授予/创建 platform_admin,且不能在 global scope 操作;
//   - revoke 撤销前检查「撤销后是否还有 active org_owner」保护最后一个 owner。
type RBACService interface {
	Bootstrap(ctx context.Context) error
	EnsureBootstrapAdmin(ctx context.Context, externalUserId, name string) error

	// Permission catalog(只读,系统范围,任何已认证用户可调)
	ListPermissions(ctx context.Context) ([]domain.Permission, error)

	// Role
	ListRoles(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Role], error)
	GetRole(ctx context.Context, user auth.UserInfo, id, code string) (domain.Role, error)
	CreateRole(ctx context.Context, user auth.UserInfo, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error)
	UpdateRole(ctx context.Context, user auth.UserInfo, id, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error)
	DeleteRole(ctx context.Context, user auth.UserInfo, id, actor string) error

	// User
	ListUsers(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.User], error)
	// SyncUser 由 GetCurrentRBACUser 触发,仅要求 caller 已认证(JWT 校验过),
	// 不要求特定权限。user 参数用于「caller 是谁」语义对齐。
	SyncUser(ctx context.Context, user auth.UserInfo, externalUserId, name, email string) (domain.User, error)

	// Role binding
	ListRoleBindings(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	ListUserGrants(ctx context.Context, user auth.UserInfo, externalUserId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	GrantRole(ctx context.Context, user auth.UserInfo, externalUserId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (domain.RoleBinding, error)
	RevokeRole(ctx context.Context, user auth.UserInfo, externalUserId, roleCode, scopeType, scopeId, actor string) error
	EffectivePermissions(ctx context.Context, user auth.UserInfo, externalUserId, scopeType, scopeId string) (domain.EffectivePermissions, error)
}

type rbacService struct {
	repo       store.RBACRepository
	authorizer auth.Authorizer
}

func NewRBACService(repo store.RBACRepository, authorizer auth.Authorizer) RBACService {
	return &rbacService{repo: repo, authorizer: authorizer}
}

// platformAdminRoleCode 是系统中唯一的"跨一切"超级角色,grant / create 都
// 受到 caller 限制:只有 platform_admin 自己才能授予 / 创建这个角色。
const platformAdminRoleCode = "platform_admin"

// authzResource 把 controller 传的 (scopeType, scopeId) 翻成 authorizer 用的
// auth.Resource。global 时 id 为空。
func authzResource(scopeType, scopeId string) auth.Resource {
	return auth.Resource{Type: scopeType, Id: scopeId}
}

// isPlatformAdmin 检查 caller 是否拥有 platform_admin 角色。
//
// 走 ListUserGrants 一次取 caller 的所有 active binding(分页大小取一个
// 保守上限,实际中一个 user 持有几十个 binding 已算多),检查是否有
// RoleCode=platform_admin + ScopeType=global 的 binding。
//
// 不走 EffectivePermissions("global") 的原因:EffectivePermissions 只返
// 权限码集合,看不出"持有 platform_admin 角色"这件事;一个 user 即便
// 拥有 platform_admin 的全部权限码,也可能是通过其他 role 叠加得到(理论上
// 不可能,但语义上不严谨)。直接看 role binding 是唯一可靠做法。
func (s *rbacService) isPlatformAdmin(ctx context.Context, user auth.UserInfo) bool {
	grants, err := s.repo.ListUserGrants(ctx, user.UserId, domain.Pagination{PageNum: 1, PageSize: 200})
	if err != nil {
		return false
	}
	for _, g := range grants.Items {
		if g.RoleCode == platformAdminRoleCode && g.ScopeType == "global" {
			return true
		}
	}
	return false
}

// checkCallerPermissionSubset 验证 caller 在目标 scope 内拥有 requestedCodes
// 中的所有权限码。返回 ErrPermissionDenied 或 nil。
func (s *rbacService) checkCallerPermissionSubset(ctx context.Context, user auth.UserInfo, requestedCodes []string, scopeType, scopeId string) error {
	if len(requestedCodes) == 0 {
		return nil
	}
	// 取 caller 的有效权限集合(EffectivePermissions 已经在 store 层
	// 处理完继承链和 active binding 过滤,这里直接拿结果)。
	eff, err := s.repo.EffectivePermissions(ctx, user.UserId, scopeType, scopeId)
	if err != nil {
		return err
	}
	owned := make(map[string]struct{}, len(eff.Permissions))
	for _, p := range eff.Permissions {
		owned[p] = struct{}{}
	}
	for _, code := range requestedCodes {
		if _, ok := owned[code]; !ok {
			return fmt.Errorf("%w: caller lacks permission %q in scope (%s, %s)", auth.ErrPermissionDenied, code, scopeType, scopeId)
		}
	}
	return nil
}

// checkGlobalScopeOnlyPlatform 拒绝非 platform_admin 在 global scope 操作。
func (s *rbacService) checkGlobalScopeOnlyPlatform(ctx context.Context, user auth.UserInfo, scopeType string) error {
	if scopeType != "global" {
		return nil
	}
	if s.isPlatformAdmin(ctx, user) {
		return nil
	}
	return fmt.Errorf("%w: only platform_admin can operate in global scope", auth.ErrPermissionDenied)
}

// checkLastOrgOwner 撤销前调用,防止撤销后该 org 内没有 active org_owner。
//
// 用 ListRoleBindings 在 (organization, scopeId) scope 内分页拉所有
// active binding,统计 roleCode="org_owner" 的条数。
//
// 设计文档 §11.5 第 3 条:最后一个 org_owner 不允许被撤销。
// 在 RevokeRole 入口,如果 caller 正在撤销的是 org_owner,
// 且撤销后剩余 active org_owner 数量 < 1,直接返回 ErrConflict,
// RevokeRole 不进入 store 写。
func (s *rbacService) checkLastOrgOwner(ctx context.Context, scopeType, scopeId, roleCode string, afterCount int) error {
	if roleCode != "org_owner" || scopeType != "organization" {
		return nil
	}
	if afterCount < 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *rbacService) Bootstrap(ctx context.Context) error {
	return s.repo.EnsureSystemData(ctx)
}

func (s *rbacService) EnsureBootstrapAdmin(ctx context.Context, externalUserId, name string) error {
	return s.repo.EnsureBootstrapAdmin(ctx, externalUserId, name)
}

func (s *rbacService) ListPermissions(ctx context.Context) ([]domain.Permission, error) {
	// 系统级只读接口,任何已认证 user 都能列。无 caller 校验。
	return s.repo.ListPermissions(ctx)
}

func (s *rbacService) ListRoles(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.Role], error) {
	if err := s.authorizer.Allow(ctx, user, "rbac:role:read", authzResource(scopeType, scopeId)); err != nil {
		return domain.PaginatedResult[domain.Role]{}, err
	}
	return s.repo.ListRoles(ctx, scopeType, scopeId, pagination)
}

func (s *rbacService) GetRole(ctx context.Context, user auth.UserInfo, id, code string) (domain.Role, error) {
	// 查 role 时 caller 必须先有 role:read,但因为 GetRole 不知道 scope,
	// 这里降级为「caller 在 global 必须有 rbac:role:read」。
	if err := s.authorizer.Allow(ctx, user, "rbac:role:read", authzResource("global", "")); err != nil {
		return domain.Role{}, err
	}
	return s.repo.GetRole(ctx, id, code)
}

func (s *rbacService) CreateRole(ctx context.Context, user auth.UserInfo, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error) {
	// 1. global scope 仅 platform_admin
	if err := s.checkGlobalScopeOnlyPlatform(ctx, user, scopeType); err != nil {
		return domain.Role{}, err
	}
	// 2. caller 必须在目标 scope 有 rbac:role:manage
	if err := s.authorizer.Allow(ctx, user, "rbac:role:manage", authzResource(scopeType, scopeId)); err != nil {
		return domain.Role{}, err
	}
	// 3. 不能创建 platform_admin 角色(非 platform_admin caller)
	if code == platformAdminRoleCode && !s.isPlatformAdmin(ctx, user) {
		return domain.Role{}, fmt.Errorf("%w: only platform_admin can create platform_admin role", auth.ErrPermissionDenied)
	}
	// 4. permissions 必须 ⊆ caller 有效权限
	if err := s.checkCallerPermissionSubset(ctx, user, permissions, scopeType, scopeId); err != nil {
		return domain.Role{}, err
	}
	return s.repo.CreateRole(ctx, code, name, description, scopeType, scopeId, permissions, actor)
}

func (s *rbacService) UpdateRole(ctx context.Context, user auth.UserInfo, id, code, name, description, scopeType, scopeId string, permissions []string, actor string) (domain.Role, error) {
	// 1. global scope 仅 platform_admin
	if err := s.checkGlobalScopeOnlyPlatform(ctx, user, scopeType); err != nil {
		return domain.Role{}, err
	}
	// 2. caller 必须在目标 scope 有 rbac:role:manage
	if err := s.authorizer.Allow(ctx, user, "rbac:role:manage", authzResource(scopeType, scopeId)); err != nil {
		return domain.Role{}, err
	}
	// 3. 改 platform_admin 角色:仅 platform_admin caller
	if code == platformAdminRoleCode && !s.isPlatformAdmin(ctx, user) {
		return domain.Role{}, fmt.Errorf("%w: only platform_admin can update platform_admin role", auth.ErrPermissionDenied)
	}
	// 4. permissions 必须 ⊆ caller 有效权限
	if err := s.checkCallerPermissionSubset(ctx, user, permissions, scopeType, scopeId); err != nil {
		return domain.Role{}, err
	}
	return s.repo.UpdateRole(ctx, id, code, name, description, scopeType, scopeId, permissions, actor)
}

func (s *rbacService) DeleteRole(ctx context.Context, user auth.UserInfo, id, actor string) error {
	// store 层已经做了 system 角色保护。这里只做 caller 范围校验。
	// 因为 DeleteRole 入参没有 scope,降级到 global scope 校验。
	if err := s.authorizer.Allow(ctx, user, "rbac:role:manage", authzResource("global", "")); err != nil {
		return err
	}
	return s.repo.DeleteRole(ctx, id, actor)
}

func (s *rbacService) ListUsers(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.User], error) {
	if err := s.authorizer.Allow(ctx, user, "rbac:binding:read", authzResource(scopeType, scopeId)); err != nil {
		return domain.PaginatedResult[domain.User]{}, err
	}
	return s.repo.ListUsers(ctx, scopeType, scopeId, pagination)
}

func (s *rbacService) SyncUser(ctx context.Context, user auth.UserInfo, externalUserId, name, email string) (domain.User, error) {
	// 任何已认证 caller 都能 sync 自己的 user 行(对齐 GetCurrentRBACUser 行为,
	// 旧实现是 JWT 中间件过即放行)。不要求特定权限。
	return s.repo.SyncUser(ctx, externalUserId, name, email)
}

func (s *rbacService) ListRoleBindings(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	if err := s.authorizer.Allow(ctx, user, "rbac:binding:read", authzResource(scopeType, scopeId)); err != nil {
		return domain.PaginatedResult[domain.RoleBinding]{}, err
	}
	return s.repo.ListRoleBindings(ctx, scopeType, scopeId, pagination)
}

func (s *rbacService) ListUserGrants(ctx context.Context, user auth.UserInfo, externalUserId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	// 全局视角读:任何一个 user 的 grants 列表,要求 caller 有 global rbac:binding:read
	if err := s.authorizer.Allow(ctx, user, "rbac:binding:read", authzResource("global", "")); err != nil {
		return domain.PaginatedResult[domain.RoleBinding]{}, err
	}
	return s.repo.ListUserGrants(ctx, externalUserId, pagination)
}

func (s *rbacService) GrantRole(ctx context.Context, user auth.UserInfo, externalUserId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (domain.RoleBinding, error) {
	// 1. global scope 仅 platform_admin
	if err := s.checkGlobalScopeOnlyPlatform(ctx, user, scopeType); err != nil {
		return domain.RoleBinding{}, err
	}
	// 2. caller 必须在目标 scope 有 rbac:binding:manage
	if err := s.authorizer.Allow(ctx, user, "rbac:binding:manage", authzResource(scopeType, scopeId)); err != nil {
		return domain.RoleBinding{}, err
	}
	// 3. 不能授予 platform_admin(非 platform_admin caller)
	if roleCode == platformAdminRoleCode && !s.isPlatformAdmin(ctx, user) {
		return domain.RoleBinding{}, fmt.Errorf("%w: only platform_admin can grant platform_admin", auth.ErrPermissionDenied)
	}
	// 4. 被授权角色 permissions 必须 ⊆ caller 有效权限
	role, err := s.repo.GetRole(ctx, "", roleCode)
	if err != nil {
		return domain.RoleBinding{}, err
	}
	if err := s.checkCallerPermissionSubset(ctx, user, role.Permissions, scopeType, scopeId); err != nil {
		return domain.RoleBinding{}, err
	}
	return s.repo.GrantRole(ctx, externalUserId, name, email, roleCode, scopeType, scopeId, expiresAt, actor)
}

func (s *rbacService) RevokeRole(ctx context.Context, user auth.UserInfo, externalUserId, roleCode, scopeType, scopeId, actor string) error {
	// 1. global scope 仅 platform_admin
	if err := s.checkGlobalScopeOnlyPlatform(ctx, user, scopeType); err != nil {
		return err
	}
	// 2. caller 必须在目标 scope 有 rbac:binding:manage
	if err := s.authorizer.Allow(ctx, user, "rbac:binding:manage", authzResource(scopeType, scopeId)); err != nil {
		return err
	}
	// 3. 最后一个 org_owner 保护:撤销前统计该 org 内 active org_owner 总数。
	// 设计文档 §11.5 第 3 条:撤销后剩余 < 1 → 拒绝。
	// 简化:由于 (user_id, role_id, scope_type, scope_id) 上有唯一约束,
	// 一个 user 在一个 org 上对 org_owner 角色至多 1 个 binding,所以
	// "撤销后剩多少"= "撤销前总数 - 1"。如果撤销前总数 <= 1,撤销后归零,拒绝。
	if roleCode == "org_owner" && scopeType == "organization" {
		bindings, err := s.repo.ListRoleBindings(ctx, scopeType, scopeId, domain.Pagination{PageNum: 1, PageSize: 200})
		if err != nil {
			return err
		}
		activeCount := 0
		for _, b := range bindings.Items {
			if b.RoleCode == "org_owner" {
				activeCount++
			}
		}
		if err := s.checkLastOrgOwner(ctx, scopeType, scopeId, roleCode, activeCount-1); err != nil {
			return err
		}
	}
	return s.repo.RevokeRole(ctx, externalUserId, roleCode, scopeType, scopeId, actor)
}

func (s *rbacService) EffectivePermissions(ctx context.Context, user auth.UserInfo, externalUserId, scopeType, scopeId string) (domain.EffectivePermissions, error) {
	if err := s.authorizer.Allow(ctx, user, "rbac:binding:read", authzResource(scopeType, scopeId)); err != nil {
		return domain.EffectivePermissions{}, err
	}
	return s.repo.EffectivePermissions(ctx, externalUserId, scopeType, scopeId)
}
