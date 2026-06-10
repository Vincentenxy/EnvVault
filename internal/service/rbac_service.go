package service

import (
	"context"
	"fmt"
	"strings"
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
	EnsureBootstrapAdmin(ctx context.Context, userId, name string) error

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
	SyncUser(ctx context.Context, user auth.UserInfo, userId, name, email string) (domain.User, error)

	// Role binding
	ListRoleBindings(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	ListUserGrants(ctx context.Context, user auth.UserInfo, userId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error)
	GrantRole(ctx context.Context, user auth.UserInfo, userId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (domain.RoleBinding, error)
	RevokeRole(ctx context.Context, user auth.UserInfo, userId, roleCode, scopeType, scopeId, actor string) error
	EffectivePermissions(ctx context.Context, user auth.UserInfo, userId, scopeType, scopeId string) (domain.EffectivePermissions, error)
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

// splitCode 把权限码拆成 (resource, action)。envVault 的权限码分两类:
//   - 标准形 "org:read" / "project:update" / "secret:reveal" → ("org", "read") 等
//   - env_template 形 "env:template:read" → ("env_template", "read")
//
// 拆不开 / 空白 → ("", "")。
func splitCode(code string) (string, string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", ""
	}
	if strings.HasPrefix(code, "env:template:") {
		return "env_template", strings.TrimPrefix(code, "env:template:")
	}
	idx := strings.Index(code, ":")
	if idx <= 0 || idx == len(code)-1 {
		return "", ""
	}
	return code[:idx], code[idx+1:]
}

// actionCovers 判断父级 action 是否覆盖子级 action。
//
// 规则(per 设计决策「同动作前缀」+ GitLab 角色等级):
//   - 同 action 覆盖(写权限覆盖自身)
//   - update 覆盖 read(write ≥ read,贴近 GitLab 角色等级)
//   - *:manage 覆盖 *:read(maintainer includes reporter,父级 manage
//     角色包含其下 read 角色;resource 前缀必须相同,例如
//     "role:manage" 覆盖 "role:read",不覆盖 "binding:read")
//
// 其他 action(create / delete / force_delete / reveal / search / list /
// template:read 等)只覆盖自身,不交叉。
func actionCovers(parentAction, childAction string) bool {
	parentAction = strings.TrimSpace(parentAction)
	childAction = strings.TrimSpace(childAction)
	if parentAction == "" {
		return false
	}
	if parentAction == childAction {
		return true
	}
	// update covers read(GitLab 风格 write ≥ read)
	if parentAction == "update" && childAction == "read" {
		return true
	}
	// *:manage 覆盖 *:read(resource 前缀必须相同)
	if strings.HasSuffix(parentAction, ":manage") && strings.HasSuffix(childAction, ":read") {
		parentPrefix := strings.TrimSuffix(parentAction, ":manage")
		childPrefix := strings.TrimSuffix(childAction, ":read")
		if parentPrefix == childPrefix {
			return true
		}
	}
	return false
}

// expandCoveredCodes 把 caller 持有的权限码展开成「能覆盖的 action 集合」。
//
// 设计决策(同动作前缀 + GitLab 角色等级):
//   - 同 action 覆盖(写权限覆盖自身)
//   - update 覆盖 read(GitLab 风格 write ≥ read)
//   - *:manage 覆盖 *:read(maintainer includes reporter)
//
// action 维度展开: caller 持有 "org:update" 时,有效 action 集合 = {"update", "read"};
// caller 持有 "rbac:binding:manage" 时,有效 action 集合 = {"binding:manage", "binding:read"}。
// resource 前缀不参与跨 resource 展开(同 action 在父级 scope 上的码天然覆盖
// 下级 resource 同 action 的码,例如 org:update 在 (organization, X) 涵盖
// project:update / env:update / folder:update / secret:update,以及这些 resource
// 的 read)。这是 GitLab 风格「父级 write 涵盖下级 write + read」的具体实现。
func expandCoveredCodes(ownedCodes []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ownedCodes)*2)
	for _, code := range ownedCodes {
		_, act := splitCode(code)
		if act == "" {
			continue
		}
		set[act] = struct{}{}
	}
	// update covers read
	if _, hasUpdate := set["update"]; hasUpdate {
		set["read"] = struct{}{}
	}
	// *:manage covers *:read(resource 前缀必须相同)
	for act := range set {
		if strings.HasSuffix(act, ":manage") {
			prefix := strings.TrimSuffix(act, ":manage")
			set[prefix+":read"] = struct{}{}
		}
	}
	return set
}

// parentScopeChain 返回 (scopeType, scopeId) 的父级链,从最具体到最一般:
//
//	global         → [global]
//	organization   → [organization, global]
//	project        → [project, organization, global]
//	environment    → [environment, project, organization, global]
//	folder         → [folder, environment, project, organization, global]
//
// 即 ResourceScopes 的语义:任何一层有 binding 都能影响 target 的可见性。
func (s *rbacService) parentScopeChain(ctx context.Context, scopeType, scopeId string) ([]auth.Scope, error) {
	resourceType := strings.TrimSpace(scopeType)
	if resourceType == "" {
		return nil, fmt.Errorf("empty scope type")
	}
	return s.repo.ResourceScopes(ctx, auth.Resource{Type: resourceType, Id: scopeId})
}

// callerCoversAtScope 判断 caller 在 (scopeType, scopeId) 这一个 scope 上,
// effective permissions 能否覆盖 requestedCodes 的全部。
//
// 覆盖规则(action 维度):
//   - requestedCodes 拆 action 后,每个 action 必须落在 caller 的 effective
//     action 集合(由 expandCoveredCodes 展开,含 update→read)里
//   - resource 前缀不参与匹配:父级 scope 上的 "org:update" 同样能覆盖子级
//     resource 的 "secret:read"(GitLab 风格「org write 涵盖下级 write+read」)
func (s *rbacService) callerCoversAtScope(ctx context.Context, user auth.UserInfo, requestedCodes []string, scopeType, scopeId string) (bool, error) {
	if len(requestedCodes) == 0 {
		return true, nil
	}
	eff, err := s.repo.EffectivePermissions(ctx, user.UserId, scopeType, scopeId)
	if err != nil {
		return false, err
	}
	ownedActions := expandCoveredCodes(eff.Permissions)
	for _, req := range requestedCodes {
		_, reqAct := splitCode(req)
		if reqAct == "" {
			return false, nil
		}
		if _, ok := ownedActions[reqAct]; !ok {
			return false, nil
		}
	}
	return true, nil
}

// checkCallerParentCoverage 验证 caller 在 (scopeType, scopeId) 沿父级链
// 的某一级 scope 上,能覆盖 requestedCodes 的全部。
//
// 覆盖规则(actionCovers):
//   - 同 action 覆盖
//   - update 覆盖 read
//
// 任意一层满足即放行;都不满足 → ErrPermissionDenied。
//
// 这是 GitLab 风格「上级 write 含下级 write+read」的具体实现:若 caller 在
// (organization, X) 有 org:update,则 caller 在 (project, Y.org_id = X) 上
// 也能 grant project:update / project:read。
func (s *rbacService) checkCallerParentCoverage(ctx context.Context, user auth.UserInfo, requestedCodes []string, scopeType, scopeId string) error {
	if len(requestedCodes) == 0 {
		return nil
	}
	scopes, err := s.parentScopeChain(ctx, scopeType, scopeId)
	if err != nil {
		return err
	}
	for _, sc := range scopes {
		ok, err := s.callerCoversAtScope(ctx, user, requestedCodes, sc.Type, sc.Id)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("%w: caller lacks parent coverage for %v at (%s, %s)", auth.ErrPermissionDenied, requestedCodes, scopeType, scopeId)
}

// rejectSelfGrant 拒绝 caller 把角色授予 / 撤销给自己(自授 / 自撤)。
// 防止越权:即使 caller 有 rbac:binding:manage + 父级覆盖,也禁止自授。
func rejectSelfGrant(caller auth.UserInfo, targetUserId string) error {
	if strings.TrimSpace(caller.UserId) == "" {
		return fmt.Errorf("%w: empty caller user id", auth.ErrPermissionDenied)
	}
	if strings.TrimSpace(caller.UserId) == strings.TrimSpace(targetUserId) {
		return fmt.Errorf("%w: cannot grant or revoke roles to self", auth.ErrPermissionDenied)
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

func validateRoleBindingScope(role domain.Role, scopeType string) error {
	roleScope := strings.TrimSpace(role.ScopeType)
	bindingScope := strings.TrimSpace(scopeType)
	if roleScope == "" || bindingScope == "" || roleScope == bindingScope {
		return nil
	}
	return fmt.Errorf("%w: role %q scope is %q, cannot bind to %q", domain.ErrConflict, role.Code, roleScope, bindingScope)
}

func (s *rbacService) Bootstrap(ctx context.Context) error {
	return s.repo.EnsureSystemData(ctx)
}

func (s *rbacService) EnsureBootstrapAdmin(ctx context.Context, userId, name string) error {
	return s.repo.EnsureBootstrapAdmin(ctx, userId, name)
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
	// 4. permissions 必须 ⊆ caller 父级链覆盖(write ≥ read 同 action 覆盖)
	if err := s.checkCallerParentCoverage(ctx, user, permissions, scopeType, scopeId); err != nil {
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
	// 4. permissions 必须 ⊆ caller 父级链覆盖(write ≥ read 同 action 覆盖)
	if err := s.checkCallerParentCoverage(ctx, user, permissions, scopeType, scopeId); err != nil {
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

func (s *rbacService) SyncUser(ctx context.Context, user auth.UserInfo, userId, name, email string) (domain.User, error) {
	// 任何已认证 caller 都能 sync 自己的 user 行(对齐 GetCurrentRBACUser 行为,
	// 旧实现是 JWT 中间件过即放行)。不要求特定权限。
	return s.repo.SyncUser(ctx, userId, name, email)
}

func (s *rbacService) ListRoleBindings(ctx context.Context, user auth.UserInfo, scopeType, scopeId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	// 父级收窄:caller 在 (scopeType, scopeId) 或其任一父级 scope 上持有
	// rbac:binding:read(rbac:binding:manage 通过 update-covers-read 同样放行)即可;
	// 返回结果会由 store 层按 cascade 收窄到 (scopeType, scopeId) 及所有下级。
	if err := s.checkCallerParentCoverage(ctx, user, []string{"rbac:binding:read"}, scopeType, scopeId); err != nil {
		return domain.PaginatedResult[domain.RoleBinding]{}, err
	}
	return s.repo.ListRoleBindingsCascading(ctx, scopeType, scopeId, pagination)
}

func (s *rbacService) ListUserGrants(ctx context.Context, user auth.UserInfo, userId string, pagination domain.Pagination) (domain.PaginatedResult[domain.RoleBinding], error) {
	// ListUserGrants 没有 scope 入参,caller 看的是「某个 user 的所有 grants」全集。
	// 这里按 caller 是否有 global rbac:binding:read 决定:有 → 放行(返回 target 全集);
	// 无 → 拒绝。本期不进一步做"按 caller's 子 scope 过滤 target 的 grants",
	// 保留 global 这一道门(若以后要细化,可让 ListUserGrants 接收一个 scope
	// 收窄参数,把 caller 在该 scope 上的 rbac:binding:read 转换成 target 的 binding
	// 子集过滤)。
	if err := s.checkCallerParentCoverage(ctx, user, []string{"rbac:binding:read"}, "global", ""); err != nil {
		return domain.PaginatedResult[domain.RoleBinding]{}, err
	}
	return s.repo.ListUserGrants(ctx, userId, pagination)
}

func (s *rbacService) GrantRole(ctx context.Context, user auth.UserInfo, userId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (domain.RoleBinding, error) {
	// 0. 禁止自授:即使 caller 是 platform_admin,也不允许把角色授给自己
	if err := rejectSelfGrant(user, userId); err != nil {
		return domain.RoleBinding{}, err
	}
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
	// 4. 被授权角色 permissions 必须 ⊆ caller 父级链覆盖(GitLab 风格:
	//    org write 涵盖 project write + read;但 caller 的角色必须真有这些 permission)
	role, err := s.repo.GetRole(ctx, "", roleCode)
	if err != nil {
		return domain.RoleBinding{}, err
	}
	if err := validateRoleBindingScope(role, scopeType); err != nil {
		return domain.RoleBinding{}, err
	}
	if err := s.checkCallerParentCoverage(ctx, user, role.Permissions, scopeType, scopeId); err != nil {
		return domain.RoleBinding{}, err
	}
	return s.repo.GrantRole(ctx, userId, name, email, roleCode, scopeType, scopeId, expiresAt, actor)
}

func (s *rbacService) RevokeRole(ctx context.Context, user auth.UserInfo, userId, roleCode, scopeType, scopeId, actor string) error {
	// 0. 禁止自撤:即使 caller 有 manage + 父级覆盖,也不允许撤销自己
	if err := rejectSelfGrant(user, userId); err != nil {
		return err
	}
	// 1. global scope 仅 platform_admin
	if err := s.checkGlobalScopeOnlyPlatform(ctx, user, scopeType); err != nil {
		return err
	}
	// 2. caller 必须在目标 scope 有 rbac:binding:manage
	if err := s.authorizer.Allow(ctx, user, "rbac:binding:manage", authzResource(scopeType, scopeId)); err != nil {
		return err
	}
	// 3. 父级覆盖:被撤销角色 permissions 必须 ⊆ caller 父级链覆盖。
	//    与 GrantRole 对称:撤销也是「让一个 user 失去权限」,caller 必须
	//    自己持有对应权限,不能「以小欺大」。
	role, err := s.repo.GetRole(ctx, "", roleCode)
	if err != nil {
		return err
	}
	if err := validateRoleBindingScope(role, scopeType); err != nil {
		return err
	}
	if len(role.Permissions) > 0 {
		if err := s.checkCallerParentCoverage(ctx, user, role.Permissions, scopeType, scopeId); err != nil {
			return err
		}
	}
	// 4. 最后一个 org_owner 保护:撤销前统计该 org 内 active org_owner 总数。
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
	return s.repo.RevokeRole(ctx, userId, roleCode, scopeType, scopeId, actor)
}

func (s *rbacService) EffectivePermissions(ctx context.Context, user auth.UserInfo, userId, scopeType, scopeId string) (domain.EffectivePermissions, error) {
	callerUserId := strings.TrimSpace(user.UserId)
	targetUserId := strings.TrimSpace(userId)
	if callerUserId == "" {
		return domain.EffectivePermissions{}, auth.ErrPermissionDenied
	}
	if callerUserId != targetUserId {
		if err := s.authorizer.Allow(ctx, user, "rbac:binding:read", authzResource(scopeType, scopeId)); err != nil {
			return domain.EffectivePermissions{}, err
		}
	}
	return s.repo.EffectivePermissions(ctx, targetUserId, scopeType, scopeId)
}
