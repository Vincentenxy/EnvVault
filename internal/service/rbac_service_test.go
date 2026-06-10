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
	// parentChain 给 ResourceScopes 返回的父级链;nil 时使用默认 chain。
	// 测试可以根据 (scopeType, scopeId) 主动指定 chain,覆盖默认行为。
	parentChain map[string][]auth.Scope
	// effectiveByScope 给 EffectivePermissions 返回的权限码按 (scopeType, scopeId) 分桶;
	// nil 时回退到 effective 字段。允许不同 scope 给出不同权限集。
	effectiveByScope map[string][]string
}

type denyAuthorizer struct {
	called bool
}

func (a *denyAuthorizer) Allow(context.Context, auth.UserInfo, string, auth.Resource) error {
	a.called = true
	return auth.ErrPermissionDenied
}

func (s *stubRBACRepo) ListUserGrants(_ context.Context, _ string, _ domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	return domain.PaginatedResult[domain.RoleBinding]{Items: s.grants, Total: int64(len(s.grants))}, nil
}

func (s *stubRBACRepo) ListRoleBindings(_ context.Context, _, _ string, _ domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	return domain.PaginatedResult[domain.RoleBinding]{Items: s.bindings, Total: int64(len(s.bindings))}, nil
}

func (s *stubRBACRepo) ListRoleBindingsCascading(_ context.Context, _, _ string, _ domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	// 默认行为:把 stub.bindings 当成 cascade 结果直接返。
	// 测试可以覆盖 bindings 字段来控制 ListRoleBindings 拿到的内容。
	return domain.PaginatedResult[domain.RoleBinding]{Items: s.bindings, Total: int64(len(s.bindings))}, nil
}

func (s *stubRBACRepo) EffectivePermissions(_ context.Context, _, scopeType, scopeId string) (domain.EffectivePermissions, error) {
	if s.effectiveByScope != nil {
		if perms, ok := s.effectiveByScope[scopeType+":"+scopeId]; ok {
			return domain.EffectivePermissions{Permissions: perms}, nil
		}
	}
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

// ResourceScopes 在 rbac_service 测试中要被 checkCallerParentCoverage
// 走到。默认返回「测试最常用的 chain」,有自定义 chain 时优先用 parentChain。
func (s *stubRBACRepo) ResourceScopes(_ context.Context, r auth.Resource) ([]auth.Scope, error) {
	key := r.Type + ":" + r.Id
	if s.parentChain != nil {
		if chain, ok := s.parentChain[key]; ok {
			return chain, nil
		}
	}
	switch r.Type {
	case "global":
		return []auth.Scope{{Type: "global"}}, nil
	case "organization":
		return []auth.Scope{
			{Type: "organization", Id: r.Id},
			{Type: "global"},
		}, nil
	case "project":
		return []auth.Scope{
			{Type: "project", Id: r.Id},
			{Type: "organization", Id: "org-of-" + r.Id},
			{Type: "global"},
		}, nil
	case "environment":
		return []auth.Scope{
			{Type: "environment", Id: r.Id},
			{Type: "project", Id: "proj-of-" + r.Id},
			{Type: "organization", Id: "org-of-env-" + r.Id},
			{Type: "global"},
		}, nil
	case "folder":
		return []auth.Scope{
			{Type: "folder", Id: r.Id},
			{Type: "environment", Id: "env-of-" + r.Id},
			{Type: "project", Id: "proj-of-env-" + r.Id},
			{Type: "organization", Id: "org-of-" + r.Id},
			{Type: "global"},
		}, nil
	case "secret":
		return []auth.Scope{
			{Type: "secret", Id: r.Id},
			{Type: "folder", Id: "folder-of-" + r.Id},
			{Type: "environment", Id: "env-of-folder-" + r.Id},
			{Type: "project", Id: "proj-of-env-" + r.Id},
			{Type: "organization", Id: "org-of-" + r.Id},
			{Type: "global"},
		}, nil
	default:
		return []auth.Scope{{Type: "global"}}, nil
	}
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

func TestEffectivePermissions_SelfDoesNotRequireBindingRead(t *testing.T) {
	repo := &stubRBACRepo{effective: []string{"org:read"}}
	authorizer := &denyAuthorizer{}
	svc := &rbacService{repo: repo, authorizer: authorizer}

	result, err := svc.EffectivePermissions(
		context.Background(),
		auth.UserInfo{UserId: "alice"},
		"alice",
		"global",
		"",
	)
	if err != nil {
		t.Fatalf("EffectivePermissions self query returned error: %v", err)
	}
	if authorizer.called {
		t.Fatal("self query should not require rbac:binding:read")
	}
	if len(result.Permissions) != 1 || result.Permissions[0] != "org:read" {
		t.Fatalf("unexpected permissions: %#v", result.Permissions)
	}
}

func TestEffectivePermissions_OtherUserRequiresBindingRead(t *testing.T) {
	repo := &stubRBACRepo{effective: []string{"org:read"}}
	authorizer := &denyAuthorizer{}
	svc := &rbacService{repo: repo, authorizer: authorizer}

	_, err := svc.EffectivePermissions(
		context.Background(),
		auth.UserInfo{UserId: "alice"},
		"bob",
		"global",
		"",
	)
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("EffectivePermissions other-user query error = %v, want ErrPermissionDenied", err)
	}
	if !authorizer.called {
		t.Fatal("other-user query should require rbac:binding:read")
	}
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

// =============================================================================
// 父级覆盖规则 (parent coverage) 测试
// =============================================================================
//
// 这一组测试覆盖以下场景:
//   - actionCovers: 父级 action 覆盖子级 action 的核心规则
//   - splitCode: 权限码拆分(env_template 特殊处理)
//   - expandCoveredCodes: 把 caller 持有的权限码展开成「能覆盖的码集合」
//   - checkCallerParentCoverage: 端到端的父级覆盖判定
//
// 设计规则(由用户决策「同动作前缀」):
//   - 同 action 覆盖(写权限覆盖自身)
//   - update 覆盖 read(GitLab 风格 write ≥ read)
//   - 其他 action(create / delete / force_delete / reveal / search / list 等)
//     只覆盖自身,不做交叉

func TestActionCovers_SameActionCovers(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		// 同 action 覆盖
		{"read", "read", true},
		{"update", "update", true},
		{"create", "create", true},
		{"delete", "delete", true},
		{"force_delete", "force_delete", true},
		{"reveal", "reveal", true},
		{"search", "search", true},
		{"list", "list", true},
		// update 覆盖 read(write ≥ read)
		{"update", "read", true},
		// 反向不成立:read 不覆盖 update
		{"read", "update", false},
		// 不同的 action 之间不覆盖
		{"create", "read", false},
		{"delete", "read", false},
		{"update", "create", false},
		{"update", "delete", false},
		{"update", "force_delete", false},
		{"force_delete", "update", false},
		{"reveal", "read", false},
		// *:manage 覆盖 *:read(resource 前缀必须相同)
		{"role:manage", "role:read", true},
		{"binding:manage", "binding:read", true},
		// 跨 resource 前缀不覆盖
		{"role:manage", "binding:read", false},
		{"binding:manage", "role:read", false},
		// 反向不成立:*:read 不覆盖 *:manage
		{"role:read", "role:manage", false},
		// 空字符串不覆盖任何东西
		{"", "read", false},
		{"update", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got := actionCovers(c.parent, c.child)
		if got != c.want {
			t.Errorf("actionCovers(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

func TestSplitCode_StandardAndEnvTemplate(t *testing.T) {
	cases := []struct {
		code             string
		wantRes, wantAct string
	}{
		{"org:read", "org", "read"},
		{"project:update", "project", "update"},
		{"secret:reveal", "secret", "reveal"},
		{"rbac:binding:manage", "rbac", "binding:manage"},
		// env_template 特殊拆分
		{"env:template:read", "env_template", "read"},
		// 空白
		{"", "", ""},
		// 没有 : 的
		{"foo", "", ""},
		// 边界
		{":", "", ""},
		{"foo:", "", ""},
		{":bar", "", ""},
	}
	for _, c := range cases {
		gotRes, gotAct := splitCode(c.code)
		if gotRes != c.wantRes || gotAct != c.wantAct {
			t.Errorf("splitCode(%q) = (%q, %q), want (%q, %q)", c.code, gotRes, gotAct, c.wantRes, c.wantAct)
		}
	}
}

func TestExpandCoveredCodes_UpdateCoversRead(t *testing.T) {
	// 单一 update 应该展开成 {update, read}(action 维度,resource 前缀不参与)
	got := expandCoveredCodes([]string{"org:update"})
	if _, ok := got["update"]; !ok {
		t.Error("expandCoveredCodes[org:update] should include action update")
	}
	if _, ok := got["read"]; !ok {
		t.Error("expandCoveredCodes[org:update] should also include read (update covers read)")
	}
	// 单一 read 不展开
	got = expandCoveredCodes([]string{"org:read"})
	if _, ok := got["read"]; !ok {
		t.Error("expandCoveredCodes[org:read] should include action read")
	}
	if _, ok := got["update"]; ok {
		t.Error("expandCoveredCodes[org:read] should NOT include update")
	}
	// create 不展开(同 action 之外不交叉)
	got = expandCoveredCodes([]string{"org:create"})
	if _, ok := got["create"]; !ok {
		t.Error("expandCoveredCodes[org:create] should include action create")
	}
	if _, ok := got["read"]; ok {
		t.Error("expandCoveredCodes[org:create] should NOT include read")
	}
	if _, ok := got["update"]; ok {
		t.Error("expandCoveredCodes[org:create] should NOT include update")
	}
	// *:manage 展开成对应 *:read(resource 前缀保留)
	got = expandCoveredCodes([]string{"rbac:role:manage"})
	if _, ok := got["role:manage"]; !ok {
		t.Error("expandCoveredCodes[rbac:role:manage] should include action role:manage")
	}
	if _, ok := got["role:read"]; !ok {
		t.Error("expandCoveredCodes[rbac:role:manage] should also include role:read (manage covers read)")
	}
	// 跨 resource manage 不交叉
	got = expandCoveredCodes([]string{"rbac:role:manage"})
	if _, ok := got["binding:read"]; ok {
		t.Error("role:manage should NOT cover binding:read (different resource prefix)")
	}
	// 多码混合:不同 resource 的 update 都贡献同一个 update action,
	// 最终集合里只有一份。
	got = expandCoveredCodes([]string{"project:update", "env:read"})
	if _, ok := got["update"]; !ok {
		t.Error("missing action update (from project:update)")
	}
	if _, ok := got["read"]; !ok {
		t.Error("missing action read (covered by project:update and from env:read)")
	}
	if _, ok := got["create"]; ok {
		t.Error("action create should not be covered")
	}
	// 资源前缀不会出现在 key 中(只保留 action)
	if _, ok := got["project:update"]; ok {
		t.Error("expandCoveredCodes should NOT keep resource prefix in keys")
	}
}

func TestExpandCoveredCodes_EmptyAndGarbage(t *testing.T) {
	// 空切片
	got := expandCoveredCodes(nil)
	if len(got) != 0 {
		t.Errorf("expandCoveredCodes(nil) should be empty, got %v", got)
	}
	// 包含垃圾码不影响其他码
	got = expandCoveredCodes([]string{"", ":", "foo:", "org:read"})
	if _, ok := got["read"]; !ok {
		t.Error("action read should still be in result (from org:read)")
	}
	if len(got) != 1 {
		t.Errorf("only action read should be present, got %v", got)
	}
	// action 集合:resource 前缀不会出现在 key 中(只保留 action)
	got = expandCoveredCodes([]string{"org:update"})
	if _, ok := got["project:read"]; ok {
		t.Error("expandCoveredCodes should not keep 'project:read' as a key (resource prefix stripped)")
	}
	if _, ok := got["secret:read"]; ok {
		t.Error("expandCoveredCodes should not keep 'secret:read' as a key (resource prefix stripped)")
	}
	// 但 call site 用 action 维度比较时:secret:read 拆出 action=read,
	// ownedActions 里有 read → 命中。跨资源覆盖由 callerCoversAtScope 的
	// action 维度判断保证,不由 expandCoveredCodes 的 key 表达。
}

// =============================================================================
// GrantRole 父级覆盖测试
// =============================================================================

// grantCoverageCase 描述一个 GrantRole 父级覆盖的测试用例。
// callerHasInScope:caller 在 (inScopeType, inScopeId) 上的有效权限集合。
// grantTarget:要 grant 的目标 (scopeType, scopeId)。
// grantRole:被授权的 role code(对应 role.Permissions)。
// wantPass:true 表示预期通过(repo.GrantRole 被调用);false 表示预期被拒。
type grantCoverageCase struct {
	name             string
	callerHasInScope []string
	inScopeType      string
	inScopeId        string
	grantTargetType  string
	grantTargetId    string
	grantRoleCode    string
	grantRolePerms   []string
	wantPass         bool
}

func runGrantRoleCoverageCase(t *testing.T, c grantCoverageCase) {
	t.Helper()
	repo := &stubRBACRepo{
		// caller 不是 platform_admin(避免 global scope 短路)
		grants: []domain.RoleBinding{{RoleCode: "org_admin", ScopeType: "organization"}},
		role:   domain.Role{Code: c.grantRoleCode, Permissions: c.grantRolePerms},
		// 让 ResourceScopes 返回 (caller's scope) → (target scope) → global 的链
		parentChain: map[string][]auth.Scope{
			c.grantTargetType + ":" + c.grantTargetId: {
				{Type: c.grantTargetType, Id: c.grantTargetId},
				{Type: c.inScopeType, Id: c.inScopeId},
				{Type: "global"},
			},
		},
		// 不同 scope 给不同 effective 权限
		effectiveByScope: map[string][]string{
			c.inScopeType + ":" + c.inScopeId: c.callerHasInScope,
			// target scope 上 caller 没有权限(强制走父级链)
			c.grantTargetType + ":" + c.grantTargetId: nil,
		},
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	_, err := svc.GrantRole(context.Background(),
		auth.UserInfo{UserId: "caller"},
		"target-user", "T", "", c.grantRoleCode,
		c.grantTargetType, c.grantTargetId, nil, "caller",
	)
	if c.wantPass {
		if err != nil {
			t.Fatalf("[%s] expected pass, got error: %v", c.name, err)
		}
		if !repo.writeCalled {
			t.Fatalf("[%s] expected repo.GrantRole to be called", c.name)
		}
	} else {
		if err == nil {
			t.Fatalf("[%s] expected reject, got nil error (writeCalled=%v)", c.name, repo.writeCalled)
		}
		if !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatalf("[%s] expected ErrPermissionDenied, got %v", c.name, err)
		}
		if repo.writeCalled {
			t.Fatalf("[%s] expected repo.GrantRole NOT to be called", c.name)
		}
	}
}

func TestGrantRole_ParentCoverage_Matrix(t *testing.T) {
	cases := []grantCoverageCase{
		// 1. 父级 org:read 覆盖子级 project:read(同 action)
		{
			name:             "org:read grants project:read",
			callerHasInScope: []string{"org:read"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_viewer", grantRolePerms: []string{"project:read"},
			wantPass: true,
		},
		// 2. 父级 org:read 不覆盖子级 project:update
		{
			name:             "org:read denies project:update",
			callerHasInScope: []string{"org:read"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_dev", grantRolePerms: []string{"project:read", "project:update"},
			wantPass: false,
		},
		// 3. 父级 org:update 覆盖子级 project:update
		{
			name:             "org:update grants project:update",
			callerHasInScope: []string{"org:update"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_dev", grantRolePerms: []string{"project:read", "project:update"},
			wantPass: true,
		},
		// 4. 父级 org:update 覆盖子级 project:read(向下覆盖)
		{
			name:             "org:update grants project:read",
			callerHasInScope: []string{"org:update"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_viewer", grantRolePerms: []string{"project:read"},
			wantPass: true,
		},
		// 5. 父级 org:read 不覆盖子级 project:create(同 action 是 create,不是 read)
		{
			name:             "org:read denies project:create",
			callerHasInScope: []string{"org:read"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_creator", grantRolePerms: []string{"project:create"},
			wantPass: false,
		},
		// 6. 父级 org:update 也不覆盖子级 project:create(update ≠ create)
		{
			name:             "org:update denies project:create",
			callerHasInScope: []string{"org:update"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_creator", grantRolePerms: []string{"project:create"},
			wantPass: false,
		},
		// 7. 父级 project:update 覆盖子级 env:update
		{
			name:             "project:update grants env:update",
			callerHasInScope: []string{"project:update"},
			inScopeType:      "project", inScopeId: "proj-1",
			grantTargetType: "environment", grantTargetId: "env-1",
			grantRoleCode: "env_writer", grantRolePerms: []string{"env:read", "env:update"},
			wantPass: true,
		},
		// 8. 父级 project:read 不覆盖子级 env:update
		{
			name:             "project:read denies env:update",
			callerHasInScope: []string{"project:read"},
			inScopeType:      "project", inScopeId: "proj-1",
			grantTargetType: "environment", grantTargetId: "env-1",
			grantRoleCode: "env_writer", grantRolePerms: []string{"env:read", "env:update"},
			wantPass: false,
		},
		// 9. 父级 org:read 覆盖子级 folder:read
		{
			name:             "org:read grants folder:read",
			callerHasInScope: []string{"org:read"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "folder", grantTargetId: "folder-1",
			grantRoleCode: "folder_viewer", grantRolePerms: []string{"folder:read"},
			wantPass: true,
		},
		// 10. 父级 org:read 不覆盖子级 folder:update
		{
			name:             "org:read denies folder:update",
			callerHasInScope: []string{"org:read"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "folder", grantTargetId: "folder-1",
			grantRoleCode: "folder_writer", grantRolePerms: []string{"folder:read", "folder:update"},
			wantPass: false,
		},
		// 11. 父级 org:update 覆盖子级 secret:create(同 action,虽然没交叉)
		//     secret:create 不在 update 覆盖集合里 → 拒
		{
			name:             "org:update denies secret:create",
			callerHasInScope: []string{"org:update"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "secret", grantTargetId: "sec-1",
			grantRoleCode: "secret_writer", grantRolePerms: []string{"secret:create"},
			wantPass: false,
		},
		// 12. 父级 org:update 覆盖 secret:read(GitLab 风格:父级 write 涵盖下级 read)
		//     action 维度:secret:read 的 action=read,ownedActions={update, read},命中。
		{
			name:             "org:update grants secret:read (cross-resource action coverage)",
			callerHasInScope: []string{"org:update"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "secret", grantTargetId: "sec-1",
			grantRoleCode: "secret_viewer", grantRolePerms: []string{"secret:read"},
			wantPass: true,
		},
		// 13. 父级 org:delete 覆盖子级 project:delete
		{
			name:             "org:delete grants project:delete",
			callerHasInScope: []string{"org:delete"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_deleter", grantRolePerms: []string{"project:delete"},
			wantPass: true,
		},
		// 14. 父级 org:update 不覆盖子级 project:delete(update ≠ delete)
		{
			name:             "org:update denies project:delete",
			callerHasInScope: []string{"org:update"},
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_deleter", grantRolePerms: []string{"project:delete"},
			wantPass: false,
		},
		// 15. 角色要求多个权限,父级必须覆盖全部
		{
			name:             "multi-perm partial coverage denied",
			callerHasInScope: []string{"org:update"}, // covers project:update + project:read
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_full", grantRolePerms: []string{"project:read", "project:update", "project:create"},
			wantPass: false, // 没 project:create
		},
		// 16. 完全没 binding → 拒
		{
			name:             "no binding at any scope denied",
			callerHasInScope: nil,
			inScopeType:      "organization", inScopeId: "org-1",
			grantTargetType: "project", grantTargetId: "proj-1",
			grantRoleCode: "project_viewer", grantRolePerms: []string{"project:read"},
			wantPass: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runGrantRoleCoverageCase(t, c)
		})
	}
}

// =============================================================================
// 自授拒绝测试
// =============================================================================

func TestGrantRole_SelfGrantDenied(t *testing.T) {
	repo := &stubRBACRepo{
		// caller 是 platform_admin(避免 global scope 短路先报错)
		grants:    []domain.RoleBinding{{RoleCode: "platform_admin", ScopeType: "global"}},
		role:      domain.Role{Code: "org_viewer", Permissions: []string{"org:read"}},
		effective: allPermissionCodes(), // 有所有权限但仍然 self-grant
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	_, err := svc.GrantRole(context.Background(),
		auth.UserInfo{UserId: "self-caller"},
		"self-caller", "Me", "", "org_viewer", "organization", "org-1", nil, "self-caller",
	)
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("self-grant should be denied, got %v", err)
	}
	if repo.writeCalled {
		t.Fatal("repo.GrantRole should NOT be called on self-grant")
	}
}

func TestRevokeRole_SelfRevokeDenied(t *testing.T) {
	repo := &stubRBACRepo{
		grants:    []domain.RoleBinding{{RoleCode: "platform_admin", ScopeType: "global"}},
		effective: allPermissionCodes(),
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	err := svc.RevokeRole(context.Background(),
		auth.UserInfo{UserId: "self-caller"},
		"self-caller", "org_viewer", "organization", "org-1", "self-caller",
	)
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("self-revoke should be denied, got %v", err)
	}
	if repo.writeCalled {
		t.Fatal("repo.RevokeRole should NOT be called on self-revoke")
	}
}

// =============================================================================
// RevokeRole 父级覆盖测试
// =============================================================================

func TestRevokeRole_ParentCoverage(t *testing.T) {
	// 父级 org:read 试图撤销 project_admin(需要 project:update):
	//  父级 org:read 不覆盖 project:update → 拒
	t.Run("org:read denies revoking project_admin", func(t *testing.T) {
		repo := &stubRBACRepo{
			grants: []domain.RoleBinding{{RoleCode: "org_admin", ScopeType: "organization"}},
			// effective=nil:让 global scope 默认无权限,避免兜底导致测试无效
			bindings: []domain.RoleBinding{{RoleCode: "project_admin", ScopeType: "project"}},
			// project_admin 角色需要 project:update;org:read 不覆盖 → 拒
			role: domain.Role{Code: "project_admin", Permissions: []string{"project:read", "project:update"}},
		}
		// effectiveByScope:让 caller 在 (project, proj-1) 上权限被剥光,
		// 只能依赖父级 (organization, org-1) 上的 org:read。
		repo.effectiveByScope = map[string][]string{
			"project:proj-1":     nil,
			"organization:org-1": {"org:read"},
			"global:":            nil,
		}
		repo.parentChain = map[string][]auth.Scope{
			"project:proj-1": {
				{Type: "project", Id: "proj-1"},
				{Type: "organization", Id: "org-1"},
				{Type: "global"},
			},
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		err := svc.RevokeRole(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"target-user", "project_admin", "project", "proj-1", "caller",
		)
		if !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatalf("org:read should not be able to revoke project_admin, got %v", err)
		}
		if repo.writeCalled {
			t.Fatal("repo.RevokeRole should NOT be called")
		}
	})

	// 父级 org:update 撤销 project_admin(需要 project:read + project:update):
	//  org:update 覆盖 update+read → 放行
	t.Run("org:update revokes project_admin", func(t *testing.T) {
		repo := &stubRBACRepo{
			grants:   []domain.RoleBinding{{RoleCode: "org_admin", ScopeType: "organization"}},
			bindings: []domain.RoleBinding{{RoleCode: "project_admin", ScopeType: "project"}},
			role:     domain.Role{Code: "project_admin", Permissions: []string{"project:read", "project:update"}},
		}
		repo.effectiveByScope = map[string][]string{
			"project:proj-1":     nil,
			"organization:org-1": {"org:update"},
			"global:":            nil,
		}
		repo.parentChain = map[string][]auth.Scope{
			"project:proj-1": {
				{Type: "project", Id: "proj-1"},
				{Type: "organization", Id: "org-1"},
				{Type: "global"},
			},
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		err := svc.RevokeRole(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"target-user", "project_admin", "project", "proj-1", "caller",
		)
		if err != nil {
			t.Fatalf("org:update should revoke project_admin, got %v", err)
		}
		if !repo.writeCalled {
			t.Fatal("repo.RevokeRole should be called")
		}
	})
}

// =============================================================================
// CreateRole / UpdateRole 父级覆盖测试(简版:验证 callsite 走新逻辑)
// =============================================================================

func TestCreateRole_ParentCoverage(t *testing.T) {
	// 父级 org:read 创建 folder_viewer(需要 folder:read): 成功
	t.Run("org:read creates folder_viewer", func(t *testing.T) {
		repo := &stubRBACRepo{
			grants: []domain.RoleBinding{{RoleCode: "org_admin", ScopeType: "organization"}},
			// effective=nil:让 global scope 默认无权限,避免兜底导致测试无效
		}
		repo.effectiveByScope = map[string][]string{
			"folder:folder-1":    nil,
			"environment:env-1":  nil,
			"project:proj-1":     nil,
			"organization:org-1": {"org:read"},
			"global:":            nil,
		}
		repo.parentChain = map[string][]auth.Scope{
			"folder:folder-1": {
				{Type: "folder", Id: "folder-1"},
				{Type: "environment", Id: "env-1"},
				{Type: "project", Id: "proj-1"},
				{Type: "organization", Id: "org-1"},
				{Type: "global"},
			},
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		_, err := svc.CreateRole(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"folder_viewer_x", "Folder Viewer X", "test", "folder", "folder-1",
			[]string{"folder:read"}, "caller",
		)
		if err != nil {
			t.Fatalf("org:read should create folder_viewer (needs folder:read), got %v", err)
		}
	})

	// 父级 org:read 创建 env_writer(需要 env:update): 拒
	t.Run("org:read denies creating env_writer", func(t *testing.T) {
		repo := &stubRBACRepo{
			grants: []domain.RoleBinding{{RoleCode: "org_admin", ScopeType: "organization"}},
		}
		repo.effectiveByScope = map[string][]string{
			"environment:env-1":  nil,
			"project:proj-1":     nil,
			"organization:org-1": {"org:read"},
			"global:":            nil,
		}
		repo.parentChain = map[string][]auth.Scope{
			"environment:env-1": {
				{Type: "environment", Id: "env-1"},
				{Type: "project", Id: "proj-1"},
				{Type: "organization", Id: "org-1"},
				{Type: "global"},
			},
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		_, err := svc.CreateRole(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"env_writer_x", "Env Writer X", "test", "environment", "env-1",
			[]string{"env:read", "env:update"}, "caller",
		)
		if !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatalf("org:read should not create env_writer, got %v", err)
		}
		if repo.writeCalled {
			t.Fatal("repo.CreateRole should NOT be called")
		}
	})
}

func TestUpdateRole_ParentCoverage(t *testing.T) {
	// 父级 org:update 更新 env_writer(需要 env:update): 成功
	t.Run("org:update updates env_writer", func(t *testing.T) {
		repo := &stubRBACRepo{
			grants:    []domain.RoleBinding{{RoleCode: "org_admin", ScopeType: "organization"}},
			effective: allPermissionCodes(),
		}
		repo.effectiveByScope = map[string][]string{
			"environment:env-1":  nil,
			"organization:org-1": {"org:update"},
		}
		repo.parentChain = map[string][]auth.Scope{
			"environment:env-1": {
				{Type: "environment", Id: "env-1"},
				{Type: "project", Id: "proj-1"},
				{Type: "organization", Id: "org-1"},
				{Type: "global"},
			},
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		_, err := svc.UpdateRole(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"role-id", "env_writer_x", "Env Writer X", "test", "environment", "env-1",
			[]string{"env:read", "env:update"}, "caller",
		)
		if err != nil {
			t.Fatalf("org:update should update env_writer, got %v", err)
		}
	})
}

// =============================================================================
// ListRoleBindings / ListUserGrants 父级收窄测试
// =============================================================================

// listBindingsCoverageCase 描述一个 ListRoleBindings/ListUserGrants 的检查用例。
// scopeType/scopeId:查询的 scope。
// callerHasInScope:caller 在 (inScopeType, inScopeId) 的有效权限集合。
// wantPass:是否预期通过。
type listBindingsCoverageCase struct {
	name             string
	scopeType        string
	scopeId          string
	callerHasInScope []string
	inScopeType      string
	inScopeId        string
	wantPass         bool
}

func runListRoleBindingsCase(t *testing.T, c listBindingsCoverageCase) {
	t.Helper()
	repo := &stubRBACRepo{
		// 给 cascade 一个空结果(不重要,只关心是否进到 repo 这一步)
		bindings: nil,
	}
	repo.parentChain = map[string][]auth.Scope{
		c.scopeType + ":" + c.scopeId: {
			{Type: c.scopeType, Id: c.scopeId},
			{Type: c.inScopeType, Id: c.inScopeId},
			{Type: "global"},
		},
	}
	repo.effectiveByScope = map[string][]string{
		c.inScopeType + ":" + c.inScopeId: c.callerHasInScope,
		// 让 target scope 上没权限,走父级链
		c.scopeType + ":" + c.scopeId: nil,
	}
	svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
	_, err := svc.ListRoleBindings(context.Background(),
		auth.UserInfo{UserId: "caller"},
		c.scopeType, c.scopeId, domain.Pagination{PageNum: 1, PageSize: 10},
	)
	if c.wantPass {
		if err != nil {
			t.Fatalf("[%s] expected pass, got %v", c.name, err)
		}
	} else {
		if !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatalf("[%s] expected ErrPermissionDenied, got %v", c.name, err)
		}
	}
}

func TestListRoleBindings_ParentScopeNarrowing(t *testing.T) {
	cases := []listBindingsCoverageCase{
		// 1. caller 在 (org, org-1) 有 rbac:binding:read,查 (project, proj-1) 命中
		{
			name:      "org binding:read allows project list",
			scopeType: "project", scopeId: "proj-1",
			callerHasInScope: []string{"rbac:binding:read"},
			inScopeType:      "organization", inScopeId: "org-1",
			wantPass: true,
		},
		// 2. caller 在 (org, org-1) 有 rbac:binding:manage(由 update-covers-read)
		{
			name:      "org binding:manage covers project list",
			scopeType: "project", scopeId: "proj-1",
			callerHasInScope: []string{"rbac:binding:manage"},
			inScopeType:      "organization", inScopeId: "org-1",
			wantPass: true,
		},
		// 3. caller 在 (project, proj-1) 自身有 rbac:binding:read,查 (env, env-1) 命中
		{
			name:      "project binding:read allows env list",
			scopeType: "environment", scopeId: "env-1",
			callerHasInScope: []string{"rbac:binding:read"},
			inScopeType:      "project", inScopeId: "proj-1",
			wantPass: true,
		},
		// 4. caller 没有任何 rbac:binding:read 权限
		{
			name:      "no binding:read denied",
			scopeType: "project", scopeId: "proj-1",
			callerHasInScope: []string{"org:read"}, // 只有 org:read,没 rbac
			inScopeType:      "organization", inScopeId: "org-1",
			wantPass: false,
		},
		// 5. caller 有 rbac:role:read 但没 rbac:binding:read
		{
			name:      "binding:role:read alone denied",
			scopeType: "project", scopeId: "proj-1",
			callerHasInScope: []string{"rbac:role:read"},
			inScopeType:      "organization", inScopeId: "org-1",
			wantPass: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runListRoleBindingsCase(t, c)
		})
	}
}

func TestListUserGrants_GlobalCheck(t *testing.T) {
	// ListUserGrants 走 global rbac:binding:read
	t.Run("caller has global binding:read passes", func(t *testing.T) {
		repo := &stubRBACRepo{
			bindings: nil,
		}
		repo.effectiveByScope = map[string][]string{
			"global:": {"rbac:binding:read"},
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		_, err := svc.ListUserGrants(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"target-user", domain.Pagination{PageNum: 1, PageSize: 10},
		)
		if err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
	})

	t.Run("caller has global binding:manage covers (update→read)", func(t *testing.T) {
		repo := &stubRBACRepo{
			bindings: nil,
		}
		repo.effectiveByScope = map[string][]string{
			"global:": {"rbac:binding:manage"},
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		_, err := svc.ListUserGrants(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"target-user", domain.Pagination{PageNum: 1, PageSize: 10},
		)
		if err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
	})

	t.Run("caller without global binding:read denied", func(t *testing.T) {
		repo := &stubRBACRepo{
			bindings: nil,
		}
		repo.effectiveByScope = map[string][]string{
			"global:": {"org:read"}, // 没 rbac
		}
		svc := &rbacService{repo: repo, authorizer: auth.AllowAllAuthorizer{}}
		_, err := svc.ListUserGrants(context.Background(),
			auth.UserInfo{UserId: "caller"},
			"target-user", domain.Pagination{PageNum: 1, PageSize: 10},
		)
		if !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatalf("expected ErrPermissionDenied, got %v", err)
		}
	})
}

// =============================================================================
// rejectSelfGrant 单元测试
// =============================================================================

func TestRejectSelfGrant(t *testing.T) {
	cases := []struct {
		name         string
		caller       auth.UserInfo
		targetUserId string
		wantErr      bool
	}{
		{"caller empty userId denied", auth.UserInfo{UserId: ""}, "alice", true},
		{"self grant denied", auth.UserInfo{UserId: "alice"}, "alice", true},
		{"self grant with whitespace denied", auth.UserInfo{UserId: "alice"}, "  alice  ", true},
		{"different user allowed", auth.UserInfo{UserId: "alice"}, "bob", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := rejectSelfGrant(c.caller, c.targetUserId)
			if c.wantErr {
				if !errors.Is(err, auth.ErrPermissionDenied) {
					t.Errorf("expected ErrPermissionDenied, got %v", err)
				}
			} else if err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

func TestValidateRoleBindingScope(t *testing.T) {
	cases := []struct {
		name      string
		role      domain.Role
		scopeType string
		wantErr   bool
	}{
		{
			name:      "same scope allowed",
			role:      domain.Role{Code: "environment_viewer", ScopeType: "environment"},
			scopeType: "environment",
		},
		{
			name:      "empty role scope kept compatible",
			role:      domain.Role{Code: "legacy_role", ScopeType: ""},
			scopeType: "environment",
		},
		{
			name:      "project role cannot bind environment",
			role:      domain.Role{Code: "project_viewer", ScopeType: "project"},
			scopeType: "environment",
			wantErr:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateRoleBindingScope(c.role, c.scopeType)
			if c.wantErr {
				if !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("expected ErrConflict, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
		})
	}
}
