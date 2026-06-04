package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"envVault/internal/auth"
	"envVault/internal/domain"
)

// 最小 RBACRepository stub,只覆盖 rbacService 写方法调到的几个方法:
//   - ListUserGrants (isPlatformAdmin 走这个)
//   - ListRoleBindings (checkLastOrgOwner 走这个)
//   - EffectivePermissions (checkCallerPermissionSubset 走这个)
//   - GetRole (GrantRole 校验角色 permissions)
//   - CreateRole / GrantRole / RevokeRole (实际写)
//
// 测试不写库,只验证「service 入口的 authz/边界校验」这一步是否拦截,
// 校验通过后调到的 repo 方法 panic(预期内,测试用 recover 抓住)。

type stubRBACRepo struct {
	// 给 ListUserGrants 返回的 grants(用于 isPlatformAdmin 判断)
	grants []domain.RoleBinding
	// 给 ListRoleBindings 返回的 bindings(用于最后一个 owner 计数)
	bindings []domain.RoleBinding
	// 给 EffectivePermissions 返回的权限码集合
	effective []string
	// 给 GetRole 返回的角色
	role domain.Role
	// 是否已被某写方法调用(写方法若被调用,测试就 fail,因为前序 authz 应该拦下)
	writeCalled bool
}

func (s *stubRBACRepo) ListUserGrants(_ context.Context, _ string, _ domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	return domain.PaginatedResult[domain.RoleBinding]{Items: s.grants, Total: int64(len(s.grants))}, nil
}

func (s *stubRBACRepo) ListRoleBindings(_ context.Context, _, _ string, _ domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	return domain.PaginatedResult[domain.RoleBinding]{Items: s.bindings, Total: int64(len(s.bindings))}, nil
}

func (s *stubRBACRepo) EffectivePermissions(_ context.Context, _, _, _ string) (domain.EffectivePermissions, error) {
	return domain.EffectivePermissions{Permissions: s.effective}, nil
}

func (s *stubRBACRepo) GetRole(_ context.Context, _, _ string) (domain.Role, error) {
	return s.role, nil
}

func (s *stubRBACRepo) CreateRole(_ context.Context, _, _, _, _, _ string, _ []string, _ string) (domain.Role, error) {
	s.writeCalled = true
	return domain.Role{}, nil
}

func (s *stubRBACRepo) UpdateRole(_ context.Context, _, _, _, _, _, _ string, _ []string, _ string) (domain.Role, error) {
	s.writeCalled = true
	return domain.Role{}, nil
}

func (s *stubRBACRepo) DeleteRole(_ context.Context, _, _ string) error {
	s.writeCalled = true
	return nil
}

func (s *stubRBACRepo) GrantRole(_ context.Context, _, _, _, _, _, _ string, _ *time.Time, _ string) (domain.RoleBinding, error) {
	s.writeCalled = true
	return domain.RoleBinding{}, nil
}

func (s *stubRBACRepo) RevokeRole(_ context.Context, _, _, _, _, _ string) error {
	s.writeCalled = true
	return nil
}

// 其他 auth.PermissionStore 方法在测试中不会被调到,panic 即可。
func (s *stubRBACRepo) ResourceScopes(_ context.Context, _ auth.Resource) ([]auth.Scope, error) {
	panic("ResourceScopes not used in rbac service tests")
}

func (s *stubRBACRepo) UserPermissions(_ context.Context, _ string, _ []auth.Scope) (map[string]struct{}, error) {
	panic("UserPermissions not used in rbac service tests")
}

func (s *stubRBACRepo) EnsureSystemData(_ context.Context) error { return nil }
func (s *stubRBACRepo) EnsureBootstrapAdmin(_ context.Context, _, _ string) error {
	return nil
}
func (s *stubRBACRepo) ListPermissions(_ context.Context) ([]domain.Permission, error) {
	return nil, nil
}
func (s *stubRBACRepo) ListRoles(_ context.Context, _, _ string, _ domain.Pagination) (domain.PaginatedResult[domain.Role], error) {
	return domain.PaginatedResult[domain.Role]{}, nil
}
func (s *stubRBACRepo) ListUsers(_ context.Context, _, _ string, _ domain.Pagination) (domain.PaginatedResult[domain.User], error) {
	return domain.PaginatedResult[domain.User]{}, nil
}
func (s *stubRBACRepo) SyncUser(_ context.Context, _, _, _ string) (domain.User, error) {
	return domain.User{}, nil
}

// TestGrantRole_CallerPermissionSubset_Rejects 验证 caller 权限不含角色
// permissions 中的任一项时,GrantRole 拒绝且不调 repo.GrantRole。
func TestGrantRole_CallerPermissionSubset_Rejects(t *testing.T) {
	repo := &stubRBACRepo{
		// caller 不持有任何权限
		effective: []string{"org:read"},
		// 被授权角色需要 project:create (caller 没有)
		role: domain.Role{Code: "release_operator", Permissions: []string{"project:create"}},
	}
	// 让 isPlatformAdmin 返 false:caller 没有 platform_admin binding
	repo.grants = nil
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	_, err := svc.GrantRole(context.Background(),
		auth.UserInfo{UserId: "alice"},
		"bob", "Bob", "", "release_operator", "project", "p1", nil, "alice",
	)
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("GrantRole should reject with ErrPermissionDenied, got %v", err)
	}
	if repo.writeCalled {
		t.Fatalf("repo.GrantRole should NOT be called when caller lacks subset")
	}
}

// TestGrantRole_PlatformAdmin_OnlyPlatformAdminCanGrant 验证非 platform_admin
// 试图授予 platform_admin 角色时,被拒绝。
func TestGrantRole_PlatformAdmin_OnlyPlatformAdminCanGrant(t *testing.T) {
	repo := &stubRBACRepo{
		// caller 是 org_owner(权限全)
		effective: allPermissionCodes(),
		// caller 不是 platform_admin
		grants: nil,
		// 目标角色 platform_admin
		role: domain.Role{Code: "platform_admin", Permissions: allPermissionCodes()},
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	_, err := svc.GrantRole(context.Background(),
		auth.UserInfo{UserId: "owner-org1"},
		"new-admin", "X", "", "platform_admin", "global", "", nil, "owner-org1",
	)
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("GrantRole platform_admin should reject non-platform caller, got %v", err)
	}
	if repo.writeCalled {
		t.Fatalf("repo.GrantRole should NOT be called when granting platform_admin as non-platform")
	}
}

// TestGrantRole_GlobalScope_OnlyPlatformAdmin 验证非 platform_admin 在 global
// scope 调 GrantRole 被拒。
func TestGrantRole_GlobalScope_OnlyPlatformAdmin(t *testing.T) {
	repo := &stubRBACRepo{
		effective: allPermissionCodes(),
		// caller 是 org_owner(非 platform_admin)
		grants: []domain.RoleBinding{{RoleCode: "org_owner", ScopeType: "organization"}},
		// 角色不需要 platform_admin
		role: domain.Role{Code: "org_owner", Permissions: allPermissionCodes()},
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	_, err := svc.GrantRole(context.Background(),
		auth.UserInfo{UserId: "owner-org1"},
		"new-org-owner", "X", "", "org_owner", "global", "", nil, "owner-org1",
	)
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("GrantRole in global scope by non-platform_admin should be rejected, got %v", err)
	}
	if repo.writeCalled {
		t.Fatalf("repo.GrantRole should NOT be called when non-platform operates in global scope")
	}
}

// TestRevokeRole_LastOrgOwner 验证撤销最后一个 active org_owner 时返 ErrConflict。
func TestRevokeRole_LastOrgOwner(t *testing.T) {
	repo := &stubRBACRepo{
		// caller 不是 platform_admin(否则 global scope 会先卡;这里 caller 在 organization scope)
		grants: []domain.RoleBinding{{RoleCode: "org_owner", ScopeType: "organization"}},
		// 目标 org 内只有 1 个 active org_owner binding(即将被撤销的就是它)
		bindings: []domain.RoleBinding{
			{User: domain.User{Id: "owner-org1"}, RoleCode: "org_owner", ScopeType: "organization"},
		},
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	err := svc.RevokeRole(context.Background(),
		auth.UserInfo{UserId: "caller"},
		"owner-org1", "org_owner", "organization", "org-uuid-1", "caller",
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RevokeRole of last org_owner should return ErrConflict, got %v", err)
	}
	if repo.writeCalled {
		t.Fatalf("repo.RevokeRole should NOT be called when revoking last org_owner")
	}
}

// TestRevokeRole_NotLastOwner_Allows 验证还有别的 org_owner 时不拦。
func TestRevokeRole_NotLastOwner_Allows(t *testing.T) {
	repo := &stubRBACRepo{
		grants: []domain.RoleBinding{{RoleCode: "org_owner", ScopeType: "organization"}},
		bindings: []domain.RoleBinding{
			{User: domain.User{Id: "owner1"}, RoleCode: "org_owner", ScopeType: "organization"},
			{User: domain.User{Id: "owner2"}, RoleCode: "org_owner", ScopeType: "organization"},
		},
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	err := svc.RevokeRole(context.Background(),
		auth.UserInfo{UserId: "caller"},
		"owner1", "org_owner", "organization", "org-uuid-1", "caller",
	)
	if err != nil {
		t.Fatalf("RevokeRole with multiple org_owners should proceed, got %v", err)
	}
	if !repo.writeCalled {
		t.Fatalf("repo.RevokeRole should be called when there are other org_owners")
	}
}

// allPermissionCodes 返回默认的 27 个权限码,用于模拟 platform_admin / org_owner
// 等「持有全部权限」的角色。这里硬编码,跟 store/postgres/rbac.go 的
// defaultPermissions() 保持一致。
func allPermissionCodes() []string {
	return []string{
		"org:create", "org:read", "org:update", "org:delete", "org:force_delete",
		"project:create", "project:read", "project:update", "project:delete",
		"env:create", "env:read", "env:update", "env:delete", "env:template:read",
		"folder:create", "folder:read", "folder:update", "folder:delete",
		"secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update", "secret:delete",
		"audit:read",
		"rbac:role:read", "rbac:role:manage", "rbac:binding:read", "rbac:binding:manage",
	}
}
