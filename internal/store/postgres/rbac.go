package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"envVault/internal/auth"
	uuidgen "envVault/internal/id"
	"gorm.io/gorm"
)

type RBACStore struct {
	db     *sql.DB
	gormDB *gorm.DB
}

type Permission struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	ResourceType string `json:"resource_type"`
	Action       string `json:"action"`
	Description  string `json:"description"`
	IsSystem     bool   `json:"is_system"`
}

type Role struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ScopeType   string   `json:"scope_type"`
	OrgID       string   `json:"org_id,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	IsSystem    bool     `json:"is_system"`
	Permissions []string `json:"permissions,omitempty" gorm:"-"`
}

type RoleInput struct {
	ID          string
	Code        string
	Name        string
	Description string
	ScopeType   string
	ScopeID     string
	Permissions []string
	Actor       string
}

type User struct {
	ID             string     `json:"id"`
	ExternalUserID string     `json:"external_user_id"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	Source         string     `json:"source"`
	IsDisabled     bool       `json:"is_disabled"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
}

type RoleBinding struct {
	ID        string     `json:"id"`
	User      User       `json:"user" gorm:"-"`
	RoleID    string     `json:"role_id"`
	RoleCode  string     `json:"role_code"`
	ScopeType string     `json:"scope_type"`
	ScopeID   string     `json:"scope_id,omitempty"`
	GrantedBy string     `json:"granted_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type GrantInput struct {
	ExternalUserID string
	Name           string
	Email          string
	RoleCode       string
	ScopeType      string
	ScopeID        string
	ExpiresAt      *time.Time
	Actor          string
}

type EffectivePermissions struct {
	Permissions  []string      `json:"permissions"`
	SourceGrants []RoleBinding `json:"source_grants"`
}

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

func NewRBACStore(db *sql.DB, gormDB *gorm.DB) *RBACStore {
	return &RBACStore{db: db, gormDB: gormDB}
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

	permissionIDs := make(map[string]string)
	for _, permission := range defaultPermissions() {
		id, err := upsertPermissionTx(ctx, tx, permission)
		if err != nil {
			return err
		}
		permissionIDs[permission.Code] = id
	}

	for _, role := range defaultRoles() {
		roleID, err := upsertSystemRoleTx(ctx, tx, role)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "delete from role_permissions where role_id = $1", roleID); err != nil {
			return err
		}
		for _, permissionCode := range role.Permissions {
			permissionID, ok := permissionIDs[permissionCode]
			if !ok {
				return fmt.Errorf("unknown permission code: %s", permissionCode)
			}
			if _, err := tx.ExecContext(ctx, `
insert into role_permissions (role_id, permission_id)
values ($1, $2)
on conflict do nothing
`, roleID, permissionID); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (s *RBACStore) EnsureBootstrapAdmin(ctx context.Context, externalUserID, name string) error {
	if strings.TrimSpace(externalUserID) == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	userID, err := upsertUserTx(ctx, tx, externalUserID, name, "")
	if err != nil {
		return err
	}
	roleID, err := roleIDByCodeTx(ctx, tx, "platform_admin")
	if err != nil {
		return err
	}
	existingID, err := activeBindingIDTx(ctx, tx, userID, roleID, "global", "")
	if err != nil {
		return err
	}
	if existingID != "" {
		return tx.Commit()
	}
	bindingID, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by)
values ($1, $2, $3, 'global', null, 'bootstrap')
`, bindingID, userID, roleID)
	if err != nil {
		return err
	}
	return tx.Commit()
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
		return s.organizationScopes(ctx, resource.ID)
	case "project":
		return s.projectScopes(ctx, resource.ID)
	case "environment":
		return s.environmentScopes(ctx, resource.ID)
	case "folder":
		return s.folderScopes(ctx, resource.ID)
	case "secret":
		return s.secretScopes(ctx, resource.ID)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resource.Type)
	}
}

func (s *RBACStore) UserPermissions(ctx context.Context, externalUserID string, scopes []auth.Scope) (map[string]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("rbac store is not configured")
	}
	if strings.TrimSpace(externalUserID) == "" || len(scopes) == 0 {
		return map[string]struct{}{}, nil
	}

	query, args := buildUserPermissionsQuery(externalUserID, scopes)
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

func (s *RBACStore) ListPermissions(ctx context.Context, pagination Pagination) (PaginatedResult[Permission], error) {
	var items []Permission
	var total int64
	query := s.gormDB.WithContext(ctx).Model(&Permission{})
	if err := query.Count(&total).Error; err != nil {
		return PaginatedResult[Permission]{}, err
	}
	if err := query.
		Order("resource_type asc, action asc").
		Limit(pagination.Limit()).
		Offset(pagination.Offset()).
		Find(&items).Error; err != nil {
		return PaginatedResult[Permission]{}, err
	}
	return PaginatedResult[Permission]{Items: items, Total: total}, nil
}

func (s *RBACStore) SyncUser(ctx context.Context, externalUserID, name, email string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if _, err := upsertUserTx(ctx, tx, externalUserID, name, email); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetUserByExternalID(ctx, externalUserID)
}

func (s *RBACStore) ListRoles(ctx context.Context, scopeType, scopeID string, pagination Pagination) (PaginatedResult[Role], error) {
	scopeType = normalizeScopeType(scopeType)
	query := s.gormDB.WithContext(ctx).Where("is_deleted = false")
	if scopeType != "" {
		query = query.Where("scope_type = ? or is_system = true", scopeType)
	}
	if strings.TrimSpace(scopeID) != "" {
		query = query.Where("is_system = true or org_id::text = ? or project_id::text = ?", scopeID, scopeID)
	}

	var items []Role
	var total int64
	if err := query.Model(&Role{}).Count(&total).Error; err != nil {
		return PaginatedResult[Role]{}, err
	}
	if err := query.Order("is_system desc, code asc").
		Limit(pagination.Limit()).
		Offset(pagination.Offset()).
		Find(&items).Error; err != nil {
		return PaginatedResult[Role]{}, err
	}
	for i := range items {
		permissions, err := s.rolePermissionCodes(ctx, items[i].ID)
		if err != nil {
			return PaginatedResult[Role]{}, err
		}
		items[i].Permissions = permissions
	}
	return PaginatedResult[Role]{Items: items, Total: total}, nil
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
	permissions, err := s.rolePermissionCodes(ctx, role.ID)
	if err != nil {
		return Role{}, err
	}
	role.Permissions = permissions
	return role, nil
}

func (s *RBACStore) CreateRole(ctx context.Context, input RoleInput) (Role, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Role{}, err
	}
	defer tx.Rollback()

	id, err := uuidgen.NewUUID()
	if err != nil {
		return Role{}, err
	}
	orgID, projectID := roleOwnerColumns(input.ScopeType, input.ScopeID)
	var role Role
	err = tx.QueryRowContext(ctx, `
insert into roles (id, code, name, description, scope_type, org_id, project_id, is_system, created_by)
values ($1, $2, $3, $4, $5, nullif($6, '')::uuid, nullif($7, '')::uuid, false, $8)
returning id, code, name, description, scope_type, coalesce(org_id::text, ''), coalesce(project_id::text, ''), is_system
`, id, input.Code, input.Name, input.Description, input.ScopeType, orgID, projectID, input.Actor).Scan(
		&role.ID, &role.Code, &role.Name, &role.Description, &role.ScopeType, &role.OrgID, &role.ProjectID, &role.IsSystem,
	)
	if err != nil {
		return Role{}, err
	}
	if err := replaceRolePermissionsTx(ctx, tx, role.ID, input.Permissions); err != nil {
		return Role{}, err
	}
	if err := tx.Commit(); err != nil {
		return Role{}, err
	}
	role.Permissions = input.Permissions
	return role, nil
}

func (s *RBACStore) UpdateRole(ctx context.Context, input RoleInput) (Role, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Role{}, err
	}
	defer tx.Rollback()

	var isSystem bool
	if err := tx.QueryRowContext(ctx, "select is_system from roles where id = $1 and is_deleted = false", input.ID).Scan(&isSystem); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Role{}, ErrNotFound
		}
		return Role{}, err
	}
	if isSystem {
		return Role{}, errors.New("system role cannot be updated")
	}

	orgID, projectID := roleOwnerColumns(input.ScopeType, input.ScopeID)
	var role Role
	err = tx.QueryRowContext(ctx, `
update roles
set code = $2, name = $3, description = $4, scope_type = $5, org_id = nullif($6, '')::uuid, project_id = nullif($7, '')::uuid, updated_at = now()
where id = $1 and is_deleted = false
returning id, code, name, description, scope_type, coalesce(org_id::text, ''), coalesce(project_id::text, ''), is_system
`, input.ID, input.Code, input.Name, input.Description, input.ScopeType, orgID, projectID).Scan(
		&role.ID, &role.Code, &role.Name, &role.Description, &role.ScopeType, &role.OrgID, &role.ProjectID, &role.IsSystem,
	)
	if err != nil {
		return Role{}, err
	}
	if err := replaceRolePermissionsTx(ctx, tx, role.ID, input.Permissions); err != nil {
		return Role{}, err
	}
	if err := tx.Commit(); err != nil {
		return Role{}, err
	}
	role.Permissions = input.Permissions
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

func (s *RBACStore) ListRoleBindings(ctx context.Context, scopeType, scopeID string, pagination Pagination) (PaginatedResult[RoleBinding], error) {
	return s.listRoleBindings(ctx, "", []auth.Scope{{Type: scopeType, ID: scopeID}}, pagination)
}

func (s *RBACStore) GrantRole(ctx context.Context, input GrantInput) (RoleBinding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RoleBinding{}, err
	}
	defer tx.Rollback()

	userID, err := upsertUserTx(ctx, tx, input.ExternalUserID, input.Name, input.Email)
	if err != nil {
		return RoleBinding{}, err
	}
	roleID, err := roleIDByCodeTx(ctx, tx, input.RoleCode)
	if err != nil {
		return RoleBinding{}, err
	}
	existingID, err := activeBindingIDTx(ctx, tx, userID, roleID, input.ScopeType, input.ScopeID)
	if err != nil {
		return RoleBinding{}, err
	}
	if existingID != "" {
		if err := tx.Commit(); err != nil {
			return RoleBinding{}, err
		}
		return s.GetRoleBinding(ctx, existingID)
	}

	id, err := uuidgen.NewUUID()
	if err != nil {
		return RoleBinding{}, err
	}
	_, err = tx.ExecContext(ctx, `
insert into user_role_bindings (id, user_id, role_id, scope_type, scope_id, granted_by, expires_at)
values ($1, $2, $3, $4, nullif($5, '')::uuid, $6, $7)
`, id, userID, roleID, input.ScopeType, input.ScopeID, input.Actor, input.ExpiresAt)
	if err != nil {
		return RoleBinding{}, err
	}
	if err := recordRoleBindingAuditTx(ctx, tx, input.Actor, "grant_role", userID, roleID, input.ScopeType, input.ScopeID, nil); err != nil {
		return RoleBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoleBinding{}, err
	}
	return s.GetRoleBinding(ctx, id)
}

func (s *RBACStore) RevokeRole(ctx context.Context, input GrantInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var bindingID, userID, roleID string
	err = tx.QueryRowContext(ctx, `
select urb.id, u.id, r.id
from user_role_bindings urb
join users u on u.id = urb.user_id
join roles r on r.id = urb.role_id
where u.external_user_id = $1
  and r.code = $2
  and urb.scope_type = $3
  and (($4 = '' and urb.scope_id is null) or urb.scope_id = nullif($4, '')::uuid)
  and urb.is_deleted = false
`, input.ExternalUserID, input.RoleCode, input.ScopeType, input.ScopeID).Scan(&bindingID, &userID, &roleID)
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
`, bindingID, input.Actor)
	if err != nil {
		return err
	}
	if err := recordRoleBindingAuditTx(ctx, tx, input.Actor, "revoke_role", userID, roleID, input.ScopeType, input.ScopeID, nil); err != nil {
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

func (s *RBACStore) GetUserByExternalID(ctx context.Context, externalUserID string) (User, error) {
	var user User
	err := s.gormDB.WithContext(ctx).Where("external_user_id = ?", externalUserID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *RBACStore) ListUsers(ctx context.Context, scopeType, scopeID string, pagination Pagination) (PaginatedResult[User], error) {
	var items []User
	baseQuery := s.gormDB.WithContext(ctx).
		Table("users u").
		Joins("join user_role_bindings urb on urb.user_id = u.id").
		Where("urb.is_deleted = false")
	if normalizeScopeType(scopeType) != "" {
		baseQuery = baseQuery.Where("urb.scope_type = ?", normalizeScopeType(scopeType))
	}
	if strings.TrimSpace(scopeID) != "" {
		baseQuery = baseQuery.Where("urb.scope_id = ?::uuid", scopeID)
	}
	var total int64
	if err := baseQuery.Distinct("u.id").Count(&total).Error; err != nil {
		return PaginatedResult[User]{}, err
	}
	err := baseQuery.
		Select("distinct u.id, u.external_user_id, u.name, u.email, u.source, u.is_disabled, u.last_seen_at").
		Order("u.name asc, u.external_user_id asc").
		Limit(pagination.Limit()).
		Offset(pagination.Offset()).
		Find(&items).Error
	return PaginatedResult[User]{Items: items, Total: total}, err
}

func (s *RBACStore) ListUserGrants(ctx context.Context, externalUserID string, pagination Pagination) (PaginatedResult[RoleBinding], error) {
	return s.listRoleBindings(ctx, externalUserID, nil, pagination)
}

func (s *RBACStore) EffectivePermissions(ctx context.Context, externalUserID, scopeType, scopeID string) (EffectivePermissions, error) {
	scopes := []auth.Scope{{Type: "global"}}
	if scopeType != "global" {
		resourceScopes, err := s.ResourceScopes(ctx, auth.Resource{Type: scopeType, ID: scopeID})
		if err != nil {
			return EffectivePermissions{}, err
		}
		scopes = resourceScopes
	}
	permissions, err := s.UserPermissions(ctx, externalUserID, scopes)
	if err != nil {
		return EffectivePermissions{}, err
	}
	codes := make([]string, 0, len(permissions))
	for code := range permissions {
		codes = append(codes, code)
	}
	sourceGrantResult, err := s.listRoleBindings(ctx, externalUserID, scopes, Pagination{PageNum: 1, PageSize: 1000})
	if err != nil {
		return EffectivePermissions{}, err
	}
	return EffectivePermissions{Permissions: codes, SourceGrants: sourceGrantResult.Items}, nil
}

func (s *RBACStore) organizationScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgID string
	err := s.db.QueryRowContext(ctx, `
select id
from organizations
where id = $1 and is_deleted = false
`, id).Scan(&orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", ID: orgID},
	}, nil
}

func (s *RBACStore) projectScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgID, projectID string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id
from projects p
join organizations o on o.id = p.org_id
where p.id = $1 and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", ID: orgID},
		{Type: "project", ID: projectID},
	}, nil
}

func (s *RBACStore) environmentScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgID, projectID, environmentID string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id, e.id
from environments e
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where e.id = $1 and e.is_deleted = false and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgID, &projectID, &environmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", ID: orgID},
		{Type: "project", ID: projectID},
		{Type: "environment", ID: environmentID},
	}, nil
}

func (s *RBACStore) folderScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgID, projectID, environmentID, folderID string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id, e.id, f.id
from folders f
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where f.id = $1 and f.is_deleted = false and e.is_deleted = false and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgID, &projectID, &environmentID, &folderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", ID: orgID},
		{Type: "project", ID: projectID},
		{Type: "environment", ID: environmentID},
		{Type: "folder", ID: folderID},
	}, nil
}

func (s *RBACStore) secretScopes(ctx context.Context, id string) ([]auth.Scope, error) {
	var orgID, projectID, environmentID, folderID string
	err := s.db.QueryRowContext(ctx, `
select o.id, p.id, e.id, f.id
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.id = $1 and s.is_deleted = false and f.is_deleted = false and e.is_deleted = false and p.is_deleted = false and o.is_deleted = false
`, id).Scan(&orgID, &projectID, &environmentID, &folderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []auth.Scope{
		{Type: "global"},
		{Type: "organization", ID: orgID},
		{Type: "project", ID: projectID},
		{Type: "environment", ID: environmentID},
		{Type: "folder", ID: folderID},
	}, nil
}

func (s *RBACStore) rolePermissionCodes(ctx context.Context, roleID string) ([]string, error) {
	var codes []string
	err := s.gormDB.WithContext(ctx).
		Table("role_permissions rp").
		Select("p.code").
		Joins("join permissions p on p.id = rp.permission_id").
		Where("rp.role_id = ?", roleID).
		Order("p.code asc").
		Pluck("p.code", &codes).Error
	return codes, err
}

func (s *RBACStore) listRoleBindings(ctx context.Context, externalUserID string, scopes []auth.Scope, pagination Pagination) (PaginatedResult[RoleBinding], error) {
	where := []string{"urb.is_deleted = false"}
	args := []any{}
	if strings.TrimSpace(externalUserID) != "" {
		args = append(args, externalUserID)
		where = append(where, fmt.Sprintf("u.external_user_id = $%d", len(args)))
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
			if strings.TrimSpace(scope.ID) == "" {
				continue
			}
			args = append(args, scopeType)
			typeIndex := len(args)
			args = append(args, scope.ID)
			idIndex := len(args)
			scopeConditions = append(scopeConditions, fmt.Sprintf("(urb.scope_type = $%d and urb.scope_id = $%d::uuid)", typeIndex, idIndex))
		}
		if len(scopeConditions) > 0 {
			where = append(where, "("+strings.Join(scopeConditions, " or ")+")")
		}
	}
	return s.queryRoleBindings(ctx, "where "+strings.Join(where, " and "), pagination, args...)
}

func (s *RBACStore) queryRoleBindings(ctx context.Context, where string, pagination Pagination, args ...any) (PaginatedResult[RoleBinding], error) {
	var total int64
	countQuery := `
select count(*)
from user_role_bindings urb
join users u on u.id = urb.user_id
join roles r on r.id = urb.role_id
` + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return PaginatedResult[RoleBinding]{}, err
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
		return PaginatedResult[RoleBinding]{}, err
	}
	defer rows.Close()

	var items []RoleBinding
	for rows.Next() {
		var item RoleBinding
		var lastSeen sql.NullTime
		var expiresAt sql.NullTime
		if err := rows.Scan(
			&item.ID,
			&item.User.ID,
			&item.User.ExternalUserID,
			&item.User.Name,
			&item.User.Email,
			&item.User.Source,
			&item.User.IsDisabled,
			&lastSeen,
			&item.RoleID,
			&item.RoleCode,
			&item.ScopeType,
			&item.ScopeID,
			&item.GrantedBy,
			&expiresAt,
			&item.CreatedAt,
		); err != nil {
			return PaginatedResult[RoleBinding]{}, err
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
		return PaginatedResult[RoleBinding]{}, err
	}
	return PaginatedResult[RoleBinding]{Items: items, Total: total}, nil
}

func buildUserPermissionsQuery(externalUserID string, scopes []auth.Scope) (string, []any) {
	args := []any{externalUserID}
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
		if strings.TrimSpace(scope.ID) == "" {
			continue
		}
		args = append(args, scopeType)
		typePlaceholder := fmt.Sprintf("$%d", len(args))
		args = append(args, scope.ID)
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
where u.external_user_id = $1
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
	default:
		return normalizeScopeType(value)
	}
}

func normalizeScopeType(value string) string {
	switch strings.TrimSpace(value) {
	case "global", "organization", "project", "environment", "folder", "secret":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func roleOwnerColumns(scopeType, scopeID string) (string, string) {
	switch normalizeScopeType(scopeType) {
	case "organization":
		return strings.TrimSpace(scopeID), ""
	case "project":
		return "", strings.TrimSpace(scopeID)
	default:
		return "", ""
	}
}

func upsertPermissionTx(ctx context.Context, tx *sql.Tx, permission systemPermission) (string, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return "", err
	}
	var storedID string
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
`, id, permission.Code, permission.Resource, permission.Action, permission.Description).Scan(&storedID)
	return storedID, err
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
insert into roles (id, code, name, description, scope_type, is_system, created_by)
values ($1, $2, $3, $4, $5, true, 'system')
`, id, role.Code, role.Name, role.Description, role.ScopeType)
		return id, err
	}
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
update roles
set name = $2, description = $3, scope_type = $4, is_system = true, updated_at = now()
where id = $1
`, id, role.Name, role.Description, role.ScopeType)
	return id, err
}

func replaceRolePermissionsTx(ctx context.Context, tx *sql.Tx, roleID string, permissionCodes []string) error {
	if _, err := tx.ExecContext(ctx, "delete from role_permissions where role_id = $1", roleID); err != nil {
		return err
	}
	for _, code := range permissionCodes {
		var permissionID string
		err := tx.QueryRowContext(ctx, "select id from permissions where code = $1", code).Scan(&permissionID)
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
`, roleID, permissionID); err != nil {
			return err
		}
	}
	return nil
}

func upsertUserTx(ctx context.Context, tx *sql.Tx, externalUserID, name, email string) (string, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return "", err
	}
	var storedID string
	err = tx.QueryRowContext(ctx, `
insert into users (id, external_user_id, name, email, source, last_seen_at)
values ($1, $2, $3, $4, 'jwt', now())
on conflict (external_user_id) do update
set name = case when excluded.name <> '' then excluded.name else users.name end,
    email = case when excluded.email <> '' then excluded.email else users.email end,
    last_seen_at = now(),
    updated_at = now()
returning id
`, id, externalUserID, name, email).Scan(&storedID)
	return storedID, err
}

func roleIDByCodeTx(ctx context.Context, tx *sql.Tx, code string) (string, error) {
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

func activeBindingIDTx(ctx context.Context, tx *sql.Tx, userID, roleID, scopeType, scopeID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
select id
from user_role_bindings
where user_id = $1
  and role_id = $2
  and scope_type = $3
  and (($4 = '' and scope_id is null) or scope_id = nullif($4, '')::uuid)
  and is_deleted = false
`, userID, roleID, scopeType, strings.TrimSpace(scopeID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func recordRoleBindingAuditTx(ctx context.Context, tx *sql.Tx, actor, action, targetUserID, roleID, scopeType, scopeID string, snapshot []byte) error {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into role_binding_audit_records (id, actor, action, target_user_id, role_id, scope_type, scope_id, snapshot)
values ($1, $2, $3, $4, $5, $6, nullif($7, '')::uuid, $8)
`, id, actor, action, targetUserID, roleID, scopeType, strings.TrimSpace(scopeID), snapshot)
	return err
}

func defaultPermissions() []systemPermission {
	return []systemPermission{
		{Code: "org:create", Resource: "org", Action: "create", Description: "Create organization"},
		{Code: "org:read", Resource: "org", Action: "read", Description: "Read organization"},
		{Code: "org:update", Resource: "org", Action: "update", Description: "Update organization"},
		{Code: "org:delete", Resource: "org", Action: "delete", Description: "Delete organization"},
		{Code: "project:create", Resource: "project", Action: "create", Description: "Create project"},
		{Code: "project:read", Resource: "project", Action: "read", Description: "Read project"},
		{Code: "project:update", Resource: "project", Action: "update", Description: "Update project"},
		{Code: "project:delete", Resource: "project", Action: "delete", Description: "Delete project"},
		{Code: "env:create", Resource: "env", Action: "create", Description: "Create environment"},
		{Code: "env:read", Resource: "env", Action: "read", Description: "Read environment"},
		{Code: "env:update", Resource: "env", Action: "update", Description: "Update environment"},
		{Code: "env:delete", Resource: "env", Action: "delete", Description: "Delete environment"},
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
	resourceRead := []string{"org:read", "project:read", "env:read", "folder:read", "secret:list", "secret:search", "secret:read"}
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
