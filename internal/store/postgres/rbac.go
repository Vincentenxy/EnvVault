package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"envVault/internal/auth"
	"envVault/internal/domain"
	uuidgen "envVault/internal/id"
	"envVault/internal/store"
	"gorm.io/gorm"
)

type RBACStore struct {
	db        *sql.DB
	gormDB    *gorm.DB
	userCache *UserCache
}

// Domain aliases:RBAC 业务类型从 internal/domain 反向 import。
// 新代码应直接使用 domain.*。
type (
	Permission           = domain.Permission
	Role                 = domain.Role
	User                 = domain.User
	RoleBinding          = domain.RoleBinding
	EffectivePermissions = domain.EffectivePermissions
)

type systemPermission struct {
	Code        string
	Resource    string
	Action      string
	Description string
}

type systemRole struct {
	Code        string
	Name        string
	Description string
	ScopeType   string
	Permissions []string
}

func NewRBACStore(db *sql.DB, gormDB *gorm.DB, userCache ...*UserCache) *RBACStore {
	var cache *UserCache
	if len(userCache) > 0 {
		cache = userCache[0]
	}
	return &RBACStore{db: db, gormDB: gormDB, userCache: cache}
}

func (s *RBACStore) EnsureSystemData(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("rbac store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	permissionIds := make(map[string]string)
	for _, permission := range defaultPermissions() {
		id, err := upsertPermissionTx(ctx, tx, permission)
		if err != nil {
			return err
		}
		permissionIds[permission.Code] = id
	}

	for _, role := range defaultRoles() {
		roleId, err := upsertSystemRoleTx(ctx, tx, role)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "delete from role_permissions where role_id = $1", roleId); err != nil {
			return err
		}
		for _, permissionCode := range role.Permissions {
			permissionId, ok := permissionIds[permissionCode]
			if !ok {
				return fmt.Errorf("unknown permission code: %s", permissionCode)
			}
			if _, err := tx.ExecContext(ctx, `
insert into role_permissions (role_id, permission_id)
values ($1, $2)
on conflict do nothing
`, roleId, permissionId); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (s *RBACStore) EnsureBootstrapAdmin(ctx context.Context, userId, name string) error {
	if strings.TrimSpace(userId) == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	storedUserId, err := upsertUserByIdTx(ctx, tx, userId, name, "")
	if err != nil {
		return err
	}
	roleId, err := roleIdByCodeTx(ctx, tx, "platform_admin")
	if err != nil {
		return err
	}
	existingId, err := activeBindingIdTx(ctx, tx, storedUserId, roleId, "global", "")
	if err != nil {
		return err
	}
	if existingId != "" {
		if err := tx.Commit(); err != nil {
			return err
		}
		s.cacheUserLabel(storedUserId, name)
		return nil
	}
	bindingId, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
values ($1, $2, $3, 'global', null, 'bootstrap')
`, bindingId, storedUserId, roleId)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cacheUserLabel(storedUserId, name)
	return nil
}

func (s *RBACStore) ResourceScopes(ctx context.Context, resource auth.Resource) ([]auth.Scope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("rbac store is not configured")
	}

	resourceType := normalizeResourceType(resource.Type)
	switch resourceType {
	case "global":
		return []auth.Scope{{Type: "global"}}, nil
	case "organization":
		return s.organizationScopes(ctx, resource.Id)
	case "project":
		return s.projectScopes(ctx, resource.Id)
	case "environment":
		return s.environmentScopes(ctx, resource.Id)
	case "folder":
		return s.folderScopes(ctx, resource.Id)
	case "secret":
		return s.secretScopes(ctx, resource.Id)
	case "env_template":
		return s.envTemplateScopes(ctx, resource.Id)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resource.Type)
	}
}

func (s *RBACStore) UserPermissions(ctx context.Context, userId string, scopes []auth.Scope) (map[string]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("rbac store is not configured")
	}
	if strings.TrimSpace(userId) == "" || len(scopes) == 0 {
		return map[string]struct{}{}, nil
	}

	query, args := buildUserPermissionsQuery(userId, scopes)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	permissions := make(map[string]struct{})
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		permissions[code] = struct{}{}
	}
	return permissions, rows.Err()
}

func (s *RBACStore) ListPermissions(ctx context.Context) ([]Permission, error) {
	var items []Permission
	if err := s.gormDB.WithContext(ctx).
		Order("resource_type asc, action asc").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (s *RBACStore) SyncUser(ctx context.Context, userId, name, email string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if _, err := upsertUserByIdTx(ctx, tx, userId, name, email); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	user, err := s.GetUserById(ctx, userId)
	if err != nil {
		return User{}, err
	}
	s.cacheUserLabel(user.Id, user.Name)
	return user, nil
}

func (s *RBACStore) ListRoles(ctx context.Context, scopeType, scopeId string, pagination Pagination) (domain.PaginatedResult[Role], error) {
	scopeType = normalizeScopeType(scopeType)
	query := s.gormDB.WithContext(ctx).Where("is_deleted = false")
	if scopeType != "" {
		query = query.Where("scope_type = ? or is_system = true", scopeType)
	}
	if strings.TrimSpace(scopeId) != "" {
		query = query.Where("is_system = true or org_id::text = ? or project_id::text = ?", scopeId, scopeId)
	}

	var items []Role
	var total int64
	if err := query.Model(&Role{}).Count(&total).Error; err != nil {
		return domain.PaginatedResult[Role]{}, err
	}
	if err := query.Order("is_system desc, code asc").
		Limit(pagination.Limit()).
		Offset(pagination.Offset()).
		Find(&items).Error; err != nil {
		return domain.PaginatedResult[Role]{}, err
	}
	for i := range items {
		permissions, err := s.rolePermissionCodes(ctx, items[i].Id)
		if err != nil {
			return domain.PaginatedResult[Role]{}, err
		}
		items[i].Permissions = permissions
	}
	return domain.PaginatedResult[Role]{Items: items, Total: total}, nil
}

func (s *RBACStore) GetRole(ctx context.Context, id, code string) (Role, error) {
	var role Role
	query := s.gormDB.WithContext(ctx).Where("is_deleted = false")
	switch {
	case strings.TrimSpace(id) != "":
		query = query.Where("id = ?", id)
	case strings.TrimSpace(code) != "":
		query = query.Where("code = ?", code)
	default:
		return Role{}, ErrNotFound
	}
	err := query.Order("is_system desc").First(&role).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, err
	}
	permissions, err := s.rolePermissionCodes(ctx, role.Id)
	if err != nil {
		return Role{}, err
	}
	role.Permissions = permissions
	return role, nil
}

func (s *RBACStore) CreateRole(ctx context.Context, code, name, description, scopeType, scopeId string, permissions []string, actor string) (Role, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Role{}, err
	}
	defer tx.Rollback()

	id, err := uuidgen.NewUUID()
	if err != nil {
		return Role{}, err
	}
	orgId, projectId := roleOwnerColumns(scopeType, scopeId)
	var role Role
	err = tx.QueryRowContext(ctx, `
insert into roles (id, code, name, description, scope_type, org_id, project_id, is_system, created_by, updated_by)
values ($1, $2, $3, $4, $5, nullif($6, '')::uuid, nullif($7, '')::uuid, false, $8, $8)
returning id, code, name, description, scope_type, coalesce(org_id::text, ''), coalesce(project_id::text, ''), is_system
`, id, code, name, description, scopeType, orgId, projectId, actor).Scan(
		&role.Id, &role.Code, &role.Name, &role.Description, &role.ScopeType, &role.OrgId, &role.ProjectId, &role.IsSystem,
	)
	if err != nil {
		return Role{}, err
	}
	if err := replaceRolePermissionsTx(ctx, tx, role.Id, permissions); err != nil {
		return Role{}, err
	}
	if err := tx.Commit(); err != nil {
		return Role{}, err
	}
	role.Permissions = permissions
	return role, nil
}

func (s *RBACStore) UpdateRole(ctx context.Context, id, code, name, description, scopeType, scopeId string, permissions []string, actor string) (Role, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Role{}, err
	}
	defer tx.Rollback()

	var isSystem bool
	if err := tx.QueryRowContext(ctx, "select is_system from roles where id = $1 and is_deleted = false", id).Scan(&isSystem); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Role{}, ErrNotFound
		}
		return Role{}, err
	}
	if isSystem {
		return Role{}, errors.New("system role cannot be updated")
	}

	orgId, projectId := roleOwnerColumns(scopeType, scopeId)
	var role Role
	err = tx.QueryRowContext(ctx, `
update roles
set code = $2, name = $3, description = $4, scope_type = $5, org_id = nullif($6, '')::uuid, project_id = nullif($7, '')::uuid, updated_by = $8, updated_at = now()
where id = $1 and is_deleted = false
returning id, code, name, description, scope_type, coalesce(org_id::text, ''), coalesce(project_id::text, ''), is_system
`, id, code, name, description, scopeType, orgId, projectId, actor).Scan(
		&role.Id, &role.Code, &role.Name, &role.Description, &role.ScopeType, &role.OrgId, &role.ProjectId, &role.IsSystem,
	)
	if err != nil {
		return Role{}, err
	}
	if err := replaceRolePermissionsTx(ctx, tx, role.Id, permissions); err != nil {
		return Role{}, err
	}
	if err := tx.Commit(); err != nil {
		return Role{}, err
	}
	role.Permissions = permissions
	return role, nil
}

func (s *RBACStore) DeleteRole(ctx context.Context, id, actor string) error {
	result := s.gormDB.WithContext(ctx).
		Table("roles").
		Where("id = ? and is_deleted = false and is_system = false", id).
		Updates(map[string]any{
			"is_deleted": true,
			"deleted_at": time.Now(),
			"deleted_by": actor,
			"updated_by": actor,
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *RBACStore) ListRoleBindings(ctx context.Context, scopeType, scopeId string, pagination Pagination) (domain.PaginatedResult[RoleBinding], error) {
	return s.listRoleBindings(ctx, "", []auth.Scope{{Type: scopeType, Id: scopeId}}, pagination)
}

// ListRoleBindingsCascading 列 (scopeType, scopeId) 自身 + 全部下级 scope 上的
// active binding。service 层在调用前已经校验 caller 在该 scope 或其父级持有
// rbac:binding:read,store 不再做 caller 校验。
//
// 用一个递归 CTE(scope_descendants)把目标 scope 的整棵子树展开成
// (scope_type, scope_id) 集合,然后跟 user_role_bindings 做 (scope_type,
// scope_id) OR 匹配(包含 global 这种 scope_id IS NULL 的情况)。
//
// 层级链:global → organization → project → environment → folder → secret。
// (global, "") 命中所有 scope_type='global' 且 scope_id IS NULL 的行;
// (organization, X) 自身 + projects(X) + envs(X 下) + folders(X 下) + secrets(X 下);
// 其余 scope 同理向下展开。
func (s *RBACStore) ListRoleBindingsCascading(ctx context.Context, scopeType, scopeId string, pagination Pagination) (domain.PaginatedResult[RoleBinding], error) {
	scopeType = strings.TrimSpace(scopeType)
	scopeId = strings.TrimSpace(scopeId)
	if scopeType == "" {
		return domain.PaginatedResult[RoleBinding]{}, fmt.Errorf("empty scope type")
	}
	if scopeType != "global" && scopeId == "" {
		return domain.PaginatedResult[RoleBinding]{}, fmt.Errorf("non-global scope requires scopeId")
	}

	cte := `
with recursive scope_descendants(scope_type, scope_id) as (
  -- anchor: target scope 自身
  select $1::text, case when $1 = 'global' then null::uuid else $2::uuid end

  union all

  -- organization → projects
  select 'project'::text, p.id
  from projects p
  join scope_descendants sd on p.org_id = sd.scope_id
  where sd.scope_type = 'organization'
    and p.is_deleted = false

  union all

  -- project → environments
  select 'environment'::text, e.id
  from environments e
  join scope_descendants sd on e.project_id = sd.scope_id
  where sd.scope_type = 'project'
    and e.is_deleted = false

  union all

  -- environment → folders
  select 'folder'::text, f.id
  from folders f
  join scope_descendants sd on f.environment_id = sd.scope_id
  where sd.scope_type = 'environment'
    and f.is_deleted = false

  union all

  -- folder → secrets
  select 'secret'::text, sec.id
  from secrets sec
  join scope_descendants sd on sec.folder_id = sd.scope_id
  where sd.scope_type = 'folder'
    and sec.is_deleted = false
)
`
	var total int64
	countQuery := cte + `
select count(*)
from user_role_bindings urb
where urb.is_deleted = false
  and exists (
    select 1 from scope_descendants sd
    where (
      (sd.scope_type = 'global' and urb.scope_type = 'global' and urb.scope_id is null)
      or (urb.scope_type = sd.scope_type and urb.scope_id = sd.scope_id)
    )
  )
`
	if err := s.db.QueryRowContext(ctx, countQuery, scopeType, scopeId).Scan(&total); err != nil {
		return domain.PaginatedResult[RoleBinding]{}, err
	}

	rows, err := s.db.QueryContext(ctx, cte+`
select
  urb.id,
  u.id,
  u.external_user_id,
  u.name,
  u.email,
  u.source,
  u.is_disabled,
  u.last_seen_at,
  r.id,
  r.code,
  urb.scope_type,
  coalesce(urb.scope_id::text, ''),
  urb.granted_by,
  urb.expires_at,
  urb.created_at
from user_role_bindings urb
join users u on u.id = urb.user_id
join roles r on r.id = urb.role_id
where urb.is_deleted = false
  and exists (
    select 1 from scope_descendants sd
    where (
      (sd.scope_type = 'global' and urb.scope_type = 'global' and urb.scope_id is null)
      or (urb.scope_type = sd.scope_type and urb.scope_id = sd.scope_id)
    )
  )
order by urb.created_at desc
limit $3 offset $4
`, scopeType, scopeId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[RoleBinding]{}, err
	}
	defer rows.Close()

	var items []RoleBinding
	for rows.Next() {
		var binding RoleBinding
		if err := rows.Scan(
			&binding.Id,
			&binding.User.Id,
			&binding.User.ExternalUserId,
			&binding.User.Name,
			&binding.User.Email,
			&binding.User.Source,
			&binding.User.IsDisabled,
			&binding.User.LastSeenAt,
			&binding.RoleId,
			&binding.RoleCode,
			&binding.ScopeType,
			&binding.ScopeId,
			&binding.GrantedBy,
			&binding.ExpiresAt,
			&binding.CreatedAt,
		); err != nil {
			return domain.PaginatedResult[RoleBinding]{}, err
		}
		items = append(items, binding)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[RoleBinding]{}, err
	}
	return domain.PaginatedResult[RoleBinding]{Items: items, Total: total}, nil
}

func (s *RBACStore) GrantRole(ctx context.Context, userId, name, email, roleCode, scopeType, scopeId string, expiresAt *time.Time, actor string) (RoleBinding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RoleBinding{}, err
	}
	defer tx.Rollback()

	storedUserId, err := upsertUserByIdTx(ctx, tx, userId, name, email)
	if err != nil {
		return RoleBinding{}, err
	}
	roleId, err := roleIdByCodeTx(ctx, tx, roleCode)
	if err != nil {
		return RoleBinding{}, err
	}
	existingId, err := activeBindingIdTx(ctx, tx, storedUserId, roleId, scopeType, scopeId)
	if err != nil {
		return RoleBinding{}, err
	}
	if existingId != "" {
		if err := tx.Commit(); err != nil {
			return RoleBinding{}, err
		}
		s.cacheUserLabel(storedUserId, name)
		return s.GetRoleBinding(ctx, existingId)
	}

	id, err := uuidgen.NewUUID()
	if err != nil {
		return RoleBinding{}, err
	}
	_, err = tx.ExecContext(ctx, `
insert into user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by, expires_at)
values ($1, $2, $3, $4, nullif($5, '')::uuid, $6, $7)
`, id, storedUserId, roleId, scopeType, scopeId, actor, expiresAt)
	if err != nil {
		return RoleBinding{}, err
	}
	if err := recordRoleBindingAuditTx(ctx, tx, actor, "grant_role", storedUserId, roleId, scopeType, scopeId, nil); err != nil {
		return RoleBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoleBinding{}, err
	}
	s.cacheUserLabel(storedUserId, name)
	return s.GetRoleBinding(ctx, id)
}

func (s *RBACStore) RevokeRole(ctx context.Context, targetUserId, roleCode, scopeType, scopeId, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var bindingId, userId, roleId string
	err = tx.QueryRowContext(ctx, `
select urb.id, u.id, r.id
from user_role_bindings urb
join users u on u.id = urb.user_id
join roles r on r.id = urb.role_id
where u.id = $1
  and r.code = $2
  and urb.scope_type = $3
  and (($4 = '' and urb.scope_id is null) or urb.scope_id = nullif($4, '')::uuid)
  and urb.is_deleted = false
`, targetUserId, roleCode, scopeType, scopeId).Scan(&bindingId, &userId, &roleId)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
update user_role_bindings
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_at = now()
where id = $1
`, bindingId, actor)
	if err != nil {
		return err
	}
	if err := recordRoleBindingAuditTx(ctx, tx, actor, "revoke_role", userId, roleId, scopeType, scopeId, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *RBACStore) GetRoleBinding(ctx context.Context, id string) (RoleBinding, error) {
	result, err := s.queryRoleBindings(ctx, `
where urb.id = $1 and urb.is_deleted = false
`, Pagination{PageNum: 1, PageSize: 1}, id)
	if err != nil {
		return RoleBinding{}, err
	}
	if len(result.Items) == 0 {
		return RoleBinding{}, ErrNotFound
	}
	return result.Items[0], nil
}

func (s *RBACStore) GetUserByExternalId(ctx context.Context, externalUserId string) (User, error) {
	var user User
	err := s.gormDB.WithContext(ctx).Where("external_user_id = ?", externalUserId).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *RBACStore) GetUserById(ctx context.Context, userId string) (User, error) {
	var user User
	err := s.gormDB.WithContext(ctx).Where("id = ?", userId).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *RBACStore) ListUsers(ctx context.Context, scopeType, scopeId string, pagination Pagination) (domain.PaginatedResult[User], error) {
	var items []User
	baseQuery := s.gormDB.WithContext(ctx).
		Table("users u").
		Joins("join user_role_bindings urb on urb.user_id = u.id").
		Where("urb.is_deleted = false")
	if normalizeScopeType(scopeType) != "" {
		baseQuery = baseQuery.Where("urb.scope_type = ?", normalizeScopeType(scopeType))
	}
	if strings.TrimSpace(scopeId) != "" {
		baseQuery = baseQuery.Where("urb.scope_id = ?::uuid", scopeId)
	}
	var total int64
	if err := baseQuery.Distinct("u.id").Count(&total).Error; err != nil {
		return domain.PaginatedResult[User]{}, err
	}
	err := baseQuery.
		Select("distinct u.id, u.external_user_id, u.name, u.email, u.source, u.is_disabled, u.last_seen_at").
		Order("u.name asc, u.external_user_id asc").
		Limit(pagination.Limit()).
		Offset(pagination.Offset()).
		Find(&items).Error
	return domain.PaginatedResult[User]{Items: items, Total: total}, err
}

func (s *RBACStore) ListUserGrants(ctx context.Context, userId string, pagination Pagination) (domain.PaginatedResult[RoleBinding], error) {
	return s.listRoleBindings(ctx, userId, nil, pagination)
}

func (s *RBACStore) EffectivePermissions(ctx context.Context, userId, scopeType, scopeId string) (EffectivePermissions, error) {
	scopes := []auth.Scope{{Type: "global"}}
	if scopeType != "global" {
		resourceScopes, err := s.ResourceScopes(ctx, auth.Resource{Type: scopeType, Id: scopeId})
		if err != nil {
			return EffectivePermissions{}, err
		}
		scopes = resourceScopes
	}
	permissions, err := s.UserPermissions(ctx, userId, scopes)
	if err != nil {
		return EffectivePermissions{}, err
	}
	codes := make([]string, 0, len(permissions))
	for code := range permissions {
		codes = append(codes, code)
	}
	sourceGrantResult, err := s.listRoleBindings(ctx, userId, scopes, Pagination{PageNum: 1, PageSize: 1000})
	if err != nil {
		return EffectivePermissions{}, err
	}
	return EffectivePermissions{Permissions: codes, SourceGrants: sourceGrantResult.Items}, nil
}

func (s *RBACStore) organizationScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgId string
	err := s.db.QueryRowContext(ctx, `
select id
from organizations
where id = $1 and is_deleted = false
`, id).Scan(&orgId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", Id: orgId},
	}, nil
}

func (s *RBACStore) projectScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgId, projectId string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id
from projects p
join organizations o on o.id = p.org_id
where p.id = $1 and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgId, &projectId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", Id: orgId},
		{Type: "project", Id: projectId},
	}, nil
}

func (s *RBACStore) environmentScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgId, projectId, environmentId string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id, e.id
from environments e
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where e.id = $1 and e.is_deleted = false and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgId, &projectId, &environmentId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", Id: orgId},
		{Type: "project", Id: projectId},
		{Type: "environment", Id: environmentId},
	}, nil
}

func (s *RBACStore) folderScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgId, projectId, environmentId, folderId string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id, e.id, f.id
from folders f
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where f.id = $1 and f.is_deleted = false and e.is_deleted = false and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgId, &projectId, &environmentId, &folderId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", Id: orgId},
		{Type: "project", Id: projectId},
		{Type: "environment", Id: environmentId},
		{Type: "folder", Id: folderId},
	}, nil
}

func (s *RBACStore) secretScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgId, projectId, environmentId, folderId string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id, e.id, f.id
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.id = $1 and s.is_deleted = false and f.is_deleted = false and e.is_deleted = false and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgId, &projectId, &environmentId, &folderId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", Id: orgId},
		{Type: "project", Id: projectId},
		{Type: "environment", Id: environmentId},
		{Type: "folder", Id: folderId},
	}, nil
}

func (s *RBACStore) envTemplateScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgId string
	err := s.db.QueryRowContext(ctx, `
select org_id
from environment_templates
where id = $1 and is_deleted = false
`, id).Scan(&orgId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", Id: orgId},
	}, nil
}

func (s *RBACStore) rolePermissionCodes(ctx context.Context, roleId string) ([]string, error) {
	var codes []string
	err := s.gormDB.WithContext(ctx).
		Table("role_permissions rp").
		Select("p.code").
		Joins("join permissions p on p.id = rp.permission_id").
		Where("rp.role_id = ?", roleId).
		Order("p.code asc").
		Pluck("p.code", &codes).Error
	return codes, err
}

func (s *RBACStore) listRoleBindings(ctx context.Context, userId string, scopes []auth.Scope, pagination Pagination) (domain.PaginatedResult[RoleBinding], error) {
	where := []string{"urb.is_deleted = false"}
	args := []any{}
	if strings.TrimSpace(userId) != "" {
		args = append(args, userId)
		where = append(where, fmt.Sprintf("u.id = $%d", len(args)))
	}
	if len(scopes) > 0 {
		scopeConditions := make([]string, 0, len(scopes))
		for _, scope := range scopes {
			scopeType := normalizeScopeType(scope.Type)
			if scopeType == "" {
				continue
			}
			if scopeType == "global" {
				args = append(args, scopeType)
				scopeConditions = append(scopeConditions, fmt.Sprintf("(urb.scope_type = $%d and urb.scope_id is null)", len(args)))
				continue
			}
			if strings.TrimSpace(scope.Id) == "" {
				continue
			}
			args = append(args, scopeType)
			typeIndex := len(args)
			args = append(args, scope.Id)
			idIndex := len(args)
			scopeConditions = append(scopeConditions, fmt.Sprintf("(urb.scope_type = $%d and urb.scope_id = $%d::uuid)", typeIndex, idIndex))
		}
		if len(scopeConditions) > 0 {
			where = append(where, "("+strings.Join(scopeConditions, " or ")+")")
		}
	}
	return s.queryRoleBindings(ctx, "where "+strings.Join(where, " and "), pagination, args...)
}

func (s *RBACStore) queryRoleBindings(ctx context.Context, where string, pagination Pagination, args ...any) (domain.PaginatedResult[RoleBinding], error) {
	var total int64
	countQuery := `
select count(*)
from user_role_bindings urb
join users u on u.id = urb.user_id
join roles r on r.id = urb.role_id
` + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return domain.PaginatedResult[RoleBinding]{}, err
	}

	args = append(args, pagination.Limit(), pagination.Offset())
	limitPlaceholder := fmt.Sprintf("$%d", len(args)-1)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args))
	rows, err := s.db.QueryContext(ctx, `
select
  urb.id,
  u.id,
  u.external_user_id,
  u.name,
  u.email,
  u.source,
  u.is_disabled,
  u.last_seen_at,
  r.id,
  r.code,
  urb.scope_type,
  coalesce(urb.scope_id::text, ''),
  urb.granted_by,
  urb.expires_at,
  urb.created_at
from user_role_bindings urb
join users u on u.id = urb.user_id
join roles r on r.id = urb.role_id
`+where+`
order by urb.created_at desc
limit `+limitPlaceholder+` offset `+offsetPlaceholder+`
`, args...)
	if err != nil {
		return domain.PaginatedResult[RoleBinding]{}, err
	}
	defer rows.Close()

	var items []RoleBinding
	for rows.Next() {
		var item RoleBinding
		var lastSeen sql.NullTime
		var expiresAt sql.NullTime
		if err := rows.Scan(
			&item.Id,
			&item.User.Id,
			&item.User.ExternalUserId,
			&item.User.Name,
			&item.User.Email,
			&item.User.Source,
			&item.User.IsDisabled,
			&lastSeen,
			&item.RoleId,
			&item.RoleCode,
			&item.ScopeType,
			&item.ScopeId,
			&item.GrantedBy,
			&expiresAt,
			&item.CreatedAt,
		); err != nil {
			return domain.PaginatedResult[RoleBinding]{}, err
		}
		if lastSeen.Valid {
			item.User.LastSeenAt = &lastSeen.Time
		}
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[RoleBinding]{}, err
	}
	return domain.PaginatedResult[RoleBinding]{Items: items, Total: total}, nil
}

func buildUserPermissionsQuery(userId string, scopes []auth.Scope) (string, []any) {
	args := []any{userId}
	conditions := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scopeType := normalizeScopeType(scope.Type)
		if scopeType == "" {
			continue
		}
		if scopeType == "global" {
			args = append(args, scopeType)
			typePlaceholder := fmt.Sprintf("$%d", len(args))
			conditions = append(conditions, fmt.Sprintf("(urb.scope_type = %s and urb.scope_id is null)", typePlaceholder))
			continue
		}
		if strings.TrimSpace(scope.Id) == "" {
			continue
		}
		args = append(args, scopeType)
		typePlaceholder := fmt.Sprintf("$%d", len(args))
		args = append(args, scope.Id)
		idPlaceholder := fmt.Sprintf("$%d", len(args))
		conditions = append(conditions, fmt.Sprintf("(urb.scope_type = %s and urb.scope_id = %s::uuid)", typePlaceholder, idPlaceholder))
	}
	if len(conditions) == 0 {
		conditions = append(conditions, "false")
	}

	query := fmt.Sprintf(`
select distinct p.code
from users u
join user_role_bindings urb on urb.user_id = u.id
join roles r on r.id = urb.role_id
join role_permissions rp on rp.role_id = r.id
join permissions p on p.id = rp.permission_id
where u.id = $1
  and u.is_disabled = false
  and urb.is_deleted = false
  and (urb.expires_at is null or urb.expires_at > now())
  and r.is_deleted = false
  and (%s)
`, strings.Join(conditions, " or "))
	return query, args
}

func normalizeResourceType(value string) string {
	switch strings.TrimSpace(value) {
	case "org":
		return "organization"
	case "env":
		return "environment"
	case "env_template":
		return "env_template"
	default:
		return normalizeScopeType(value)
	}
}

func (s *RBACStore) cacheUserLabel(userId, name string) {
	if s == nil {
		return
	}
	s.userCache.CacheUserLabel(userId, name)
}

func normalizeScopeType(value string) string {
	switch strings.TrimSpace(value) {
	case "global", "organization", "project", "environment", "folder", "secret":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func roleOwnerColumns(scopeType, scopeId string) (string, string) {
	switch normalizeScopeType(scopeType) {
	case "organization":
		return strings.TrimSpace(scopeId), ""
	case "project":
		return "", strings.TrimSpace(scopeId)
	default:
		return "", ""
	}
}

func upsertPermissionTx(ctx context.Context, tx *sql.Tx, permission systemPermission) (string, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return "", err
	}
	var storedId string
	err = tx.QueryRowContext(ctx, `
insert into permissions (id, code, resource_type, action, description, is_system)
values ($1, $2, $3, $4, $5, true)
on conflict (code) do update
set resource_type = excluded.resource_type,
    action = excluded.action,
    description = excluded.description,
    is_system = true,
    updated_at = now()
returning id
`, id, permission.Code, permission.Resource, permission.Action, permission.Description).Scan(&storedId)
	return storedId, err
}

func upsertSystemRoleTx(ctx context.Context, tx *sql.Tx, role systemRole) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
select id
from roles
where code = $1 and is_system = true and is_deleted = false
`, role.Code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = uuidgen.NewUUID()
		if err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, `
insert into roles (id, code, name, description, scope_type, is_system, created_by, updated_by)
values ($1, $2, $3, $4, $5, true, 'system', 'system')
`, id, role.Code, role.Name, role.Description, role.ScopeType)
		return id, err
	}
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
update roles
set name = $2, description = $3, scope_type = $4, is_system = true, updated_by = 'system', updated_at = now()
where id = $1
`, id, role.Name, role.Description, role.ScopeType)
	return id, err
}

func replaceRolePermissionsTx(ctx context.Context, tx *sql.Tx, roleId string, permissionCodes []string) error {
	if _, err := tx.ExecContext(ctx, "delete from role_permissions where role_id = $1", roleId); err != nil {
		return err
	}
	for _, code := range permissionCodes {
		var permissionId string
		err := tx.QueryRowContext(ctx, "select id from permissions where code = $1", code).Scan(&permissionId)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("unknown permission code: %s", code)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
insert into role_permissions (role_id, permission_id)
values ($1, $2)
on conflict do nothing
`, roleId, permissionId); err != nil {
			return err
		}
	}
	return nil
}

func upsertUserTx(ctx context.Context, tx *sql.Tx, externalUserId, name, email string) (string, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return "", err
	}
	var storedId string
	err = tx.QueryRowContext(ctx, `
insert into users (id, external_user_id, name, email, source, last_seen_at)
values ($1, $2, $3, $4, 'jwt', now())
on conflict (external_user_id) do update
set name = case when excluded.name <> '' then excluded.name else users.name end,
    email = case when excluded.email <> '' then excluded.email else users.email end,
    last_seen_at = now(),
    updated_at = now()
returning id
`, id, externalUserId, name, email).Scan(&storedId)
	return storedId, err
}

func upsertUserByIdTx(ctx context.Context, tx *sql.Tx, userId, name, email string) (string, error) {
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return "", ErrNotFound
	}
	var storedId string
	err := tx.QueryRowContext(ctx, `
insert into users (id, external_user_id, name, email, source, last_seen_at)
values ($1, $2, $3, $4, 'jwt', now())
on conflict (id) do update
set name = case when excluded.name <> '' then excluded.name else users.name end,
    email = case when excluded.email <> '' then excluded.email else users.email end,
    last_seen_at = now(),
    updated_at = now()
returning id
`, userId, userId, name, email).Scan(&storedId)
	return storedId, translatePgErr(err)
}

func roleIdByCodeTx(ctx context.Context, tx *sql.Tx, code string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
select id
from roles
where code = $1 and is_deleted = false
order by is_system desc
limit 1
`, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func activeBindingIdTx(ctx context.Context, tx *sql.Tx, userId, roleId, scopeType, scopeId string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
select id
from user_role_bindings
where user_id = $1
  and role_id = $2
  and scope_type = $3
  and (($4 = '' and scope_id is null) or scope_id = nullif($4, '')::uuid)
  and is_deleted = false
`, userId, roleId, scopeType, strings.TrimSpace(scopeId)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func recordRoleBindingAuditTx(ctx context.Context, tx *sql.Tx, actor, action, targetUserId, roleId, scopeType, scopeId string, snapshot []byte) error {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into role_binding_audit_records (id, actor, action, target_user_id, role_id, scope_type, scope_id, snapshot)
values ($1, $2, $3, $4, $5, $6, nullif($7, '')::uuid, $8)
`, id, actor, action, targetUserId, roleId, scopeType, strings.TrimSpace(scopeId), snapshot)
	return err
}

func defaultPermissions() []systemPermission {
	return []systemPermission{
		{Code: "org:create", Resource: "org", Action: "create", Description: "Create organization"},
		{Code: "org:read", Resource: "org", Action: "read", Description: "Read organization"},
		{Code: "org:update", Resource: "org", Action: "update", Description: "Update organization"},
		{Code: "org:delete", Resource: "org", Action: "delete", Description: "Delete organization"},
		{Code: "org:force_delete", Resource: "org", Action: "force_delete", Description: "Force cascade-delete an organization including all child projects, environments, folders, and secrets"},
		{Code: "project:create", Resource: "project", Action: "create", Description: "Create project"},
		{Code: "project:read", Resource: "project", Action: "read", Description: "Read project"},
		{Code: "project:update", Resource: "project", Action: "update", Description: "Update project"},
		{Code: "project:delete", Resource: "project", Action: "delete", Description: "Delete project"},
		{Code: "env:create", Resource: "env", Action: "create", Description: "Create environment"},
		{Code: "env:read", Resource: "env", Action: "read", Description: "Read environment"},
		{Code: "env:update", Resource: "env", Action: "update", Description: "Update environment"},
		{Code: "env:delete", Resource: "env", Action: "delete", Description: "Delete environment"},
		{Code: "env:template:read", Resource: "env_template", Action: "template:read", Description: "Read environment templates"},
		{Code: "folder:create", Resource: "folder", Action: "create", Description: "Create folder"},
		{Code: "folder:read", Resource: "folder", Action: "read", Description: "Read folder"},
		{Code: "folder:update", Resource: "folder", Action: "update", Description: "Update folder"},
		{Code: "folder:delete", Resource: "folder", Action: "delete", Description: "Delete folder"},
		{Code: "secret:list", Resource: "secret", Action: "list", Description: "List secrets"},
		{Code: "secret:search", Resource: "secret", Action: "search", Description: "Search secrets"},
		{Code: "secret:read", Resource: "secret", Action: "read", Description: "Read secret metadata"},
		{Code: "secret:reveal", Resource: "secret", Action: "reveal", Description: "Reveal secret value"},
		{Code: "secret:create", Resource: "secret", Action: "create", Description: "Create secret"},
		{Code: "secret:update", Resource: "secret", Action: "update", Description: "Update secret"},
		{Code: "secret:delete", Resource: "secret", Action: "delete", Description: "Delete secret"},
		{Code: "audit:read", Resource: "audit", Action: "read", Description: "Read audit records"},
		{Code: "rbac:role:read", Resource: "rbac", Action: "role:read", Description: "Read roles"},
		{Code: "rbac:role:manage", Resource: "rbac", Action: "role:manage", Description: "Manage roles"},
		{Code: "rbac:binding:read", Resource: "rbac", Action: "binding:read", Description: "Read role bindings"},
		{Code: "rbac:binding:manage", Resource: "rbac", Action: "binding:manage", Description: "Manage role bindings"},
	}
}

func defaultRoles() []systemRole {
	all := permissionCodes(defaultPermissions())
	resourceRead := []string{"org:read", "project:read", "env:read", "folder:read", "secret:list", "secret:search", "secret:read", "env:template:read"}
	auditRead := append([]string{}, resourceRead...)
	auditRead = append(auditRead, "audit:read")
	secretManage := []string{"secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update", "secret:delete"}
	return []systemRole{
		{Code: "platform_admin", Name: "Platform Admin", ScopeType: "global", Permissions: all},
		{Code: "org_owner", Name: "Organization Owner", ScopeType: "organization", Permissions: all},
		{Code: "org_admin", Name: "Organization Admin", ScopeType: "organization", Permissions: []string{"org:read", "org:update", "project:create", "project:read", "project:update", "project:delete", "env:create", "env:read", "env:update", "env:delete", "folder:create", "folder:read", "folder:update", "folder:delete", "secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update", "secret:delete", "audit:read"}},
		{Code: "org_viewer", Name: "Organization Viewer", ScopeType: "organization", Permissions: resourceRead},
		{Code: "org_auditor", Name: "Organization Auditor", ScopeType: "organization", Permissions: auditRead},
		{Code: "project_admin", Name: "Project Admin", ScopeType: "project", Permissions: []string{"project:read", "project:update", "env:create", "env:read", "env:update", "env:delete", "folder:create", "folder:read", "folder:update", "folder:delete", "secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update", "secret:delete", "rbac:binding:read", "rbac:binding:manage", "audit:read"}},
		{Code: "project_developer", Name: "Project Developer", ScopeType: "project", Permissions: []string{"project:read", "env:read", "folder:read", "secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update"}},
		{Code: "project_viewer", Name: "Project Viewer", ScopeType: "project", Permissions: []string{"project:read", "env:read", "folder:read", "secret:list", "secret:search", "secret:read"}},
		{Code: "project_auditor", Name: "Project Auditor", ScopeType: "project", Permissions: []string{"project:read", "env:read", "folder:read", "secret:list", "secret:read", "audit:read"}},
		{Code: "environment_admin", Name: "Environment Admin", Description: "Manage folders, secrets, members, and audit records in one environment", ScopeType: "environment", Permissions: []string{"env:read", "env:update", "folder:create", "folder:read", "folder:update", "folder:delete", "secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update", "secret:delete", "rbac:binding:read", "rbac:binding:manage", "audit:read"}},
		{Code: "environment_developer", Name: "Environment Developer", Description: "Read, reveal, create, and update secrets in one environment", ScopeType: "environment", Permissions: []string{"env:read", "folder:read", "secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update"}},
		{Code: "environment_viewer", Name: "Environment Viewer", Description: "Read environment metadata and secret keys without revealing values", ScopeType: "environment", Permissions: []string{"env:read", "folder:read", "secret:list", "secret:search", "secret:read"}},
		{Code: "environment_auditor", Name: "Environment Auditor", Description: "Read environment metadata, secret keys, and audit records", ScopeType: "environment", Permissions: []string{"env:read", "folder:read", "secret:list", "secret:read", "audit:read"}},
		{Code: "folder_admin", Name: "Folder Admin", ScopeType: "folder", Permissions: secretManage},
		{Code: "folder_editor", Name: "Folder Editor", ScopeType: "folder", Permissions: []string{"secret:list", "secret:search", "secret:read", "secret:reveal", "secret:create", "secret:update"}},
		{Code: "folder_viewer", Name: "Folder Viewer", ScopeType: "folder", Permissions: []string{"secret:list", "secret:search", "secret:read"}},
	}
}

func permissionCodes(permissions []systemPermission) []string {
	codes := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		codes = append(codes, permission.Code)
	}
	return codes
}

// Compile-time guard:确保 RBACStore 同时满足 store.RBACRepository 和 auth.PermissionStore。
var (
	_ store.RBACRepository = (*RBACStore)(nil)
	_ auth.PermissionStore = (*RBACStore)(nil)
)
