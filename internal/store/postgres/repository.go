package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"envVault/internal/domain"
	uuidgen "envVault/internal/id"
	"envVault/internal/store"
)

// ErrNotFound 是 Repository 在 row 不存在或已软删时返回的哨兵错误。
// 业务层用 errors.Is 判定,handler 层映射为 404。
var ErrNotFound = domain.ErrNotFound

// ErrConflict 在写入违反唯一约束时返回,使用方可通过 errors.Is(err, ErrConflict) 识别。
// 由 handler 层映射为 409 Conflict 响应。
var ErrConflict = domain.ErrConflict

// pgUniqueViolation 是 PostgreSQL SQLSTATE 中"违反唯一约束"的错误码。
const pgUniqueViolation = "23505"

// translatePgErr 把底层驱动错误翻译为哨兵错误。
// 当前只翻译唯一冲突 (23505) → ErrConflict;其他错误原样返回。
// 调用者必须把 Exec/Query/QueryRow 的 err 整个传进来,不要提前丢掉。
func translatePgErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrConflict
	}
	return err
}

// Domain aliases:业务实体、值对象、错误全部从 internal/domain 反向 import。
// 这里保留 `postgres.X` 别名是给尚未切到 service 的 controller / 测试用,
// 新代码应直接使用 domain.*。
type (
	Entity              = domain.Entity
	EnvSpec             = domain.EnvSpec
	EnvironmentTemplate = domain.EnvironmentTemplate
	Secret              = domain.Secret
	SecretCiphertext    = domain.SecretCiphertext
	SecretCacheRecord   = domain.SecretCacheRecord
	AuditRecord         = domain.AuditRecord
	ListFilter          = domain.ListFilter
	Pagination          = domain.Pagination
)

type Repository struct {
	db        *sql.DB
	userCache *UserCache
}

func NewRepository(db *sql.DB, userCache ...*UserCache) *Repository {
	var cache *UserCache
	if len(userCache) > 0 {
		cache = userCache[0]
	}
	return &Repository{db: db, userCache: cache}
}

func (r *Repository) CacheUserLabel(userId, name string) {
	if r == nil || r.userCache == nil {
		return
	}
	r.userCache.CacheUserLabel(userId, name)
}

func (r *Repository) CreateOrganization(ctx context.Context, code, name, comment, actor string) (Entity, error) {
	return r.createEntity(ctx, "organizations", "", "", code, name, comment, actor, "organization")
}

func (r *Repository) ListOrganizations(ctx context.Context, callerUserId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	cte := organizationNavigationCTE()
	cols, scanInto := entityReadColumns(parentColumn("organizations"))
	narrow := " and t.id in (select org_id from visible_organizations)"
	var total int64
	countQuery := cte + fmt.Sprintf(`
select count(*) from organizations t
where t.is_deleted = false%s
	`, narrow)
	if err := r.db.QueryRowContext(ctx, countQuery, callerUserId).Scan(&total); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select %s
from organizations t
where t.is_deleted = false%s
order by t.name asc
limit $2 offset $3
	`, cols, narrow), callerUserId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	defer rows.Close()
	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return domain.PaginatedResult[Entity]{}, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	return domain.PaginatedResult[Entity]{Items: items, Total: total}, nil
}

func (r *Repository) GetOrganization(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "organizations", id)
}

func (r *Repository) GetOrganizationByCode(ctx context.Context, code string) (Entity, error) {
	return r.getEntityByCode(ctx, "organizations", code)
}

func (r *Repository) UpdateOrganization(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "organizations", id, name, comment, actor, "organization")
}

// DeleteOrganization 级联软删 org + 所有下属 project/env/folder/secret。
// 返回的 CascadeScope 包含所有被软删的下游 id,handler 用它同步 Redis cache 失效。
// force=false 时只删 org 自身(若有 active project 直接返回 ErrConflict),
// 此时 ProjectIds/EnvironmentIds/FolderIds/SecretIds 均为空。
func (r *Repository) DeleteOrganization(ctx context.Context, id, actor string, force bool) (domain.CascadeScope, error) {
	scope := domain.CascadeScope{OrganizationId: id}
	if !force {
		// 非强制:先查 active project,只要有一个就拒,避免造成孤儿 org。
		var activeProjects int64
		if err := r.db.QueryRowContext(ctx, `
select count(*) from projects where org_id = $1::uuid and is_deleted = false
`, id).Scan(&activeProjects); err != nil {
			return scope, err
		}
		if activeProjects > 0 {
			return scope, fmt.Errorf("organization has %d active project(s); pass force=true to cascade delete: %w", activeProjects, ErrConflict)
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return scope, err
	}
	defer tx.Rollback()

	// 1. 软删 org 自身 + 快照 + 审计
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "organizations", id, actor, "organization"); err != nil {
		return scope, err
	}
	// 2. 级联:软删 org 下所有 project(RETURNING id 收集到 CascadeScope)
	scope.ProjectIds, err = softDeleteByParentTxReturning(ctx, tx, "projects", "org_id", id, actor)
	if err != nil {
		return scope, err
	}
	// 3. 级联:软删 org 下所有 env(跨表:env.project_id ∈ projects under org)
	scope.EnvironmentIds, err = softDeleteEnvUnderOrgTx(ctx, tx, id, actor)
	if err != nil {
		return scope, err
	}
	// 4. 级联:软删 org 下所有 folder(沿 project → env → folder)
	scope.FolderIds, err = softDeleteFolderUnderOrgTx(ctx, tx, id, actor)
	if err != nil {
		return scope, err
	}
	// 5. 级联:软删 org 下所有 secret(沿 project → env → folder → secret)
	scope.SecretIds, err = softDeleteSecretUnderOrgTx(ctx, tx, id, actor)
	if err != nil {
		return scope, err
	}
	return scope, tx.Commit()
}

func (r *Repository) CreateProject(ctx context.Context, orgId, code, name, comment, actor string, envs []EnvSpec) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	project, err := r.createEntityTx(ctx, tx, "projects", "org_id", orgId, code, name, comment, actor)
	if err != nil {
		return Entity{}, err
	}

	// v3:env 归属 project,逐个创建 + upsert 模板。folder 由调用方按需显式创建。
	for _, env := range envs {
		if _, err := r.createEnvironmentTx(ctx, tx, project.Id, env.Code, env.Name, env.Comment, actor, env.SortOrder); err != nil {
			return Entity{}, err
		}
		if err := r.upsertEnvironmentTemplateTx(ctx, tx, orgId, env.Code, env.Name, env.Comment, actor); err != nil {
			return Entity{}, err
		}
	}

	if err := recordAuditTx(ctx, tx, actor, "project", project.Id, "create", nil); err != nil {
		return Entity{}, err
	}
	return project, tx.Commit()
}

func (r *Repository) ListProjects(ctx context.Context, callerUserId, orgId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	cte := projectNavigationCTE()
	cols, scanInto := entityReadColumns(parentColumn("projects"))
	narrow := " and t.id in (select project_id from visible_projects)"
	var total int64
	if err := r.db.QueryRowContext(ctx, cte+fmt.Sprintf(`
select count(*) from projects t
where t.is_deleted = false
  and t.org_id = $2::uuid%s
	`, narrow), callerUserId, orgId).Scan(&total); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select %s
from projects t
where t.is_deleted = false
  and t.org_id = $2::uuid%s
order by t.name asc
limit $3 offset $4
	`, cols, narrow), callerUserId, orgId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	defer rows.Close()
	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return domain.PaginatedResult[Entity]{}, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	return domain.PaginatedResult[Entity]{Items: items, Total: total}, nil
}

func (r *Repository) GetProject(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "projects", id)
}

func (r *Repository) GetProjectByCode(ctx context.Context, orgId, code string) (Entity, error) {
	return r.getEntityByCodeWithParent(ctx, "projects", "org_id", orgId, code)
}

func (r *Repository) UpdateProject(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "projects", id, name, comment, actor, "project")
}

// DeleteProject 级联软删 project + 下属 env/folder/secret。
// 返回 CascadeScope:ProjectIds 为 [id] 自身(便于 handler 统一遍历),其他 3 类
// 收集所有被软删的下游 id。
func (r *Repository) DeleteProject(ctx context.Context, id, actor string) (domain.CascadeScope, error) {
	scope := domain.CascadeScope{ProjectIds: []string{id}}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return scope, err
	}
	defer tx.Rollback()

	// 1. 软删 project 自身 + 快照 + 审计
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "projects", id, actor, "project"); err != nil {
		return scope, err
	}
	// 2. 级联:软删 project 下所有 env
	scope.EnvironmentIds, err = softDeleteByParentTxReturning(ctx, tx, "environments", "project_id", id, actor)
	if err != nil {
		return scope, err
	}
	// 3. 级联:软删 project 下所有 folder(跨表:folder.environment_id ∈ envs under project)
	scope.FolderIds, err = softDeleteFolderUnderProjectTx(ctx, tx, id, actor)
	if err != nil {
		return scope, err
	}
	// 4. 级联:软删 project 下所有 secret(跨表,沿 env → folder → secret 链)
	scope.SecretIds, err = softDeleteSecretsUnderProjectTx(ctx, tx, id, actor)
	if err != nil {
		return scope, err
	}
	return scope, tx.Commit()
}

func (r *Repository) CreateEnvironment(ctx context.Context, projectId, code, name, comment, actor string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	// v3:env 归属 project,先反查 project 拿到 orgId 以供模板 upsert 使用
	var orgId string
	if err := tx.QueryRowContext(ctx, `
select org_id from projects where id = $1::uuid and is_deleted = false
`, projectId).Scan(&orgId); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Entity{}, ErrNotFound
		}
		return Entity{}, err
	}

	env, err := r.createEnvironmentTx(ctx, tx, projectId, code, name, comment, actor, 0)
	if err != nil {
		return Entity{}, err
	}
	// 注意:不在此处自动建 globals / groups-secrets folder,
	// 由调用方按需显式创建,避免隐性资源注入。

	if err := r.upsertEnvironmentTemplateTx(ctx, tx, orgId, code, name, comment, actor); err != nil {
		return Entity{}, err
	}

	if err := recordAuditTx(ctx, tx, actor, "environment", env.Id, "create", nil); err != nil {
		return Entity{}, err
	}
	return env, tx.Commit()
}

func (r *Repository) ListEnvironments(ctx context.Context, callerUserId, projectId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	cte := environmentNavigationCTE()
	cols, scanInto := environmentReadColumns()
	narrow := " and t.id in (select environment_id from visible_environments)"
	var total int64
	if err := r.db.QueryRowContext(ctx, cte+fmt.Sprintf(`
select count(*)
from environments t
join projects p on p.id = t.project_id
where t.is_deleted = false
  and p.is_deleted = false
  and t.project_id = $2::uuid%s
	`, narrow), callerUserId, projectId).Scan(&total); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select %s
from environments t
join projects p on p.id = t.project_id
where t.is_deleted = false
  and p.is_deleted = false
  and t.project_id = $2::uuid%s
order by t.sort_order asc, t.created_at asc
limit $3 offset $4
	`, cols, narrow), callerUserId, projectId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	defer rows.Close()
	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return domain.PaginatedResult[Entity]{}, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	return domain.PaginatedResult[Entity]{Items: items, Total: total}, nil
}

func (r *Repository) GetEnvironment(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "environments", id)
}

func (r *Repository) GetEnvironmentByCode(ctx context.Context, projectId, code string) (Entity, error) {
	return r.getEntityByCodeWithParent(ctx, "environments", "project_id", projectId, code)
}

func (r *Repository) UpdateEnvironment(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "environments", id, name, comment, actor, "environment")
}

// DeleteEnvironment 级联软删 env + 下属 folder/secret。
// 返回 CascadeScope:EnvironmentIds 为 [id] 自身,其他 2 类收集所有被软删的下游 id。
func (r *Repository) DeleteEnvironment(ctx context.Context, id, actor string) (domain.CascadeScope, error) {
	scope := domain.CascadeScope{EnvironmentIds: []string{id}}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return scope, err
	}
	defer tx.Rollback()

	// 1. 软删 env 自身 + 快照 + 审计(同事务)
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "environments", id, actor, "environment"); err != nil {
		return scope, err
	}
	// 2. 级联:软删 env 下所有 folder
	scope.FolderIds, err = softDeleteByParentTxReturning(ctx, tx, "folders", "environment_id", id, actor)
	if err != nil {
		return scope, err
	}
	// 3. 级联:软删 env 下所有 secret(跨表:secret.folder_id ∈ folders under env)
	scope.SecretIds, err = softDeleteSecretsUnderEnvTx(ctx, tx, id, actor)
	if err != nil {
		return scope, err
	}
	return scope, tx.Commit()
}

// upsertEnvironmentTemplateTx 在事务内 upsert env 模板;已存在则跳过,name/comment 保持首次写入快照。
func (r *Repository) upsertEnvironmentTemplateTx(
	ctx context.Context, tx *sql.Tx,
	orgId, code, name, comment, actor string,
) error {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into environment_templates (id, org_id, code, name, comment, created_by, updated_by)
values ($1, $2, $3, $4, $5, $6, $6)
on conflict (org_id, code) where is_deleted = false do nothing
`, id, orgId, code, name, comment, actor)
	return err
}

func (r *Repository) ListEnvironmentTemplates(ctx context.Context, callerUserId, orgId string, pagination Pagination) (domain.PaginatedResult[EnvironmentTemplate], error) {
	cte := userReadScopeCTE()
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "env_template", column: "t.id"},
		{scopeType: "organization", column: "t.org_id"},
	})
	var total int64
	if err := r.db.QueryRowContext(ctx, cte+fmt.Sprintf(`
select count(*) from environment_templates t
where t.org_id = $3::uuid and t.is_deleted = false%s
`, narrow), callerUserId, "env:template:read", orgId).Scan(&total); err != nil {
		return domain.PaginatedResult[EnvironmentTemplate]{}, err
	}

	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select id, org_id::text, code, name, comment, created_by, updated_by, created_at, updated_at
from environment_templates t
where t.org_id = $3::uuid and t.is_deleted = false%s
order by t.name asc
limit $4 offset $5
`, narrow), callerUserId, "env:template:read", orgId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[EnvironmentTemplate]{}, err
	}
	defer rows.Close()

	var items []EnvironmentTemplate
	for rows.Next() {
		var item EnvironmentTemplate
		if err := rows.Scan(
			&item.Id, &item.OrgId, &item.Code, &item.Name, &item.Comment,
			&item.CreatedBy, &item.UpdatedBy,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return domain.PaginatedResult[EnvironmentTemplate]{}, err
		}
		item.CreatedByLabel = r.userLabel(item.CreatedBy)
		item.UpdatedByLabel = r.userLabel(item.UpdatedBy)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[EnvironmentTemplate]{}, err
	}
	return domain.PaginatedResult[EnvironmentTemplate]{Items: items, Total: total}, nil
}

func (r *Repository) GetEnvironmentTemplate(ctx context.Context, id string) (EnvironmentTemplate, error) {
	var item EnvironmentTemplate
	err := r.db.QueryRowContext(ctx, `
select id, org_id::text, code, name, comment, created_by, updated_by, created_at, updated_at
from environment_templates
where id = $1::uuid and is_deleted = false
`, id).Scan(
		&item.Id, &item.OrgId, &item.Code, &item.Name, &item.Comment,
		&item.CreatedBy, &item.UpdatedBy,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentTemplate{}, ErrNotFound
	}
	if err != nil {
		return EnvironmentTemplate{}, err
	}
	item.CreatedByLabel = r.userLabel(item.CreatedBy)
	item.UpdatedByLabel = r.userLabel(item.UpdatedBy)
	return item, nil
}

func (r *Repository) GetEnvironmentTemplateByCode(ctx context.Context, orgId, code string) (EnvironmentTemplate, error) {
	var item EnvironmentTemplate
	err := r.db.QueryRowContext(ctx, `
select id, org_id::text, code, name, comment, created_by, updated_by, created_at, updated_at
from environment_templates
where org_id = $1::uuid and code = $2 and is_deleted = false
`, orgId, code).Scan(
		&item.Id, &item.OrgId, &item.Code, &item.Name, &item.Comment,
		&item.CreatedBy, &item.UpdatedBy,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentTemplate{}, ErrNotFound
	}
	if err != nil {
		return EnvironmentTemplate{}, err
	}
	item.CreatedByLabel = r.userLabel(item.CreatedBy)
	item.UpdatedByLabel = r.userLabel(item.UpdatedBy)
	return item, nil
}

// CreateFolder 创建 folder。
//
// 关键设计:folders 表同时持 environment_id 与 parent_id 两个字段,
// 看似冗余,实际语义不重叠:
//
//   - environment_id:答"这个 folder 属于哪个 env"
//     level=1 时 parent_id 必为 NULL(父是 env,不是 folder),所以顶层 folder
//     唯一能挂到 env 的字段就是 environment_id,不能砍。
//     level=2 时此字段确实可由 parent.environment_id 推出,但保留是反范式,
//     换来 O(1) 的 env 范围查询。
//
//   - parent_id:答"这个 folder 的父 folder 是谁"
//     仅 level=2 填写;level=1 必为 NULL(父不是 folder)。
//
// 入参语义:
//   - level=1:environmentId 是 env id,parentFolderId 忽略。
//   - level=2:environmentId 是 env id(由 controller 从父 folder 反查后传入),
//     parentFolderId 必须是同 env 下 level=1 folder 的 id。
//
// 返回的 Entity.ParentId 字段多态,反映"父":
//   - level=1:ParentId = environmentId(env 是父)
//   - level=2:ParentId = parentFolderId(父 folder 是父)
func (r *Repository) CreateFolder(
	ctx context.Context,
	environmentId, parentFolderId, code, name, comment, actor string,
	level int,
) (Entity, error) {
	if level == 1 {
		return r.createEntity(ctx, "folders", "environment_id", environmentId, code, name, comment, actor, "folder")
	}
	if level != 2 {
		return Entity{}, fmt.Errorf("invalid folder level %d, want 1 or 2: %w", level, ErrConflict)
	}
	if strings.TrimSpace(parentFolderId) == "" {
		return Entity{}, errors.New("level=2 folder requires parentFolderId")
	}
	// 校验 parent folder:存在、未软删、是 level=1、environment_id 一致。
	var parentEnvId string
	var parentLevel int
	var parentDeleted bool
	err := r.db.QueryRowContext(ctx, `
select environment_id::text, level, is_deleted from folders where id = $1::uuid
`, parentFolderId).Scan(&parentEnvId, &parentLevel, &parentDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
	if parentDeleted {
		return Entity{}, ErrNotFound
	}
	if parentLevel != 1 {
		return Entity{}, fmt.Errorf("parent folder must be level=1, got %d: %w", parentLevel, ErrConflict)
	}
	if parentEnvId != environmentId {
		return Entity{}, fmt.Errorf("parent folder environment %s != request environment %s: %w", parentEnvId, environmentId, ErrConflict)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	id, err := uuidgen.NewUUID()
	if err != nil {
		return Entity{}, err
	}
	// 注意:RETURNING 列顺序故意不取 environment_id,改用显式赋值
	// 让 Entity.ParentId 反映"父"(level=2 时是父 folder id,而非 env id)。
	var folder Entity
	err = tx.QueryRowContext(ctx, `
insert into folders (id, environment_id, parent_id, level, code, name, comment, created_by, updated_by)
values ($1, $2, $3, $4, $5, $6, $7, $8, $8)
returning id, code, name, comment, created_by, updated_by, created_at, updated_at
`, id, environmentId, parentFolderId, level, code, name, comment, actor).Scan(
		&folder.Id, &folder.Code, &folder.Name, &folder.Comment,
		&folder.CreatedBy, &folder.UpdatedBy, &folder.CreatedAt, &folder.UpdatedAt,
	)
	if err != nil {
		return Entity{}, translatePgErr(err)
	}
	folder.ParentId = parentFolderId
	r.fillEntityLabels(&folder)
	if err := recordAuditTx(ctx, tx, actor, "folder", folder.Id, "create", nil); err != nil {
		return Entity{}, err
	}
	return folder, tx.Commit()
}

// CreateFoldersAcrossEnvs 批量跨环境创建 level=2 folder(子 folder)。
//
// 适用场景:前端传 `{ "code":"stripe", "parentCode":"payment", "level":2,
// "envList":["<dev-id>","<test-id>"] }`,后端在每个 env 下用 parentCode 反查
// 同 code 的 level=1 sibling parent folder,挂子 folder 于此。
//
// 入参约束:
//   - parentCode 非空,作为参考父 folder 的 code
//   - envIds 非空,每项是 env id(UUID)
//   - 任一 env 不存在 → ErrNotFound
//   - 任一 env 下找不到 code = parentCode 的 level=1 sibling parent → ErrNotFound
//   - 任一 (env, sibling parent) 下目标子 code 已存在 → ErrConflict
//
// 返回:按 envIds 顺序排列的 []Entity,Entity.ParentId 是该 env 下的 sibling parent id。
// 任何一步失败返回 (nil, error) + rollback,error 类型沿用既有的 ErrNotFound / ErrConflict。
//
// 注:本方法没有显式校验 env 与 parent folder 属于同一 project——反查"code 相同的 level=1
// folder"已经隐式证明该 env 在 parent folder 所属 project 链上;若 env 误传成其他
// project 下的同名 env,sibling 反查自然返 ErrNotFound。
func (r *Repository) CreateFoldersAcrossEnvs(
	ctx context.Context,
	parentCode, code, name, comment, actor string,
	envIds []string,
) ([]domain.Entity, error) {
	if len(envIds) == 0 {
		return nil, errors.New("CreateFoldersAcrossEnvs requires non-empty envIds")
	}
	if strings.TrimSpace(parentCode) == "" {
		return nil, errors.New("CreateFoldersAcrossEnvs requires non-empty parentCode")
	}

	// 1. 校验每个 envId 存在 + 未软删;同 id 复用 map 缓存。
	seenEnv := make(map[string]bool, len(envIds))
	verifiedEnvIds := make([]string, 0, len(envIds))
	for _, envId := range envIds {
		if seenEnv[envId] {
			verifiedEnvIds = append(verifiedEnvIds, envId)
			continue
		}
		var isDeleted bool
		qerr := r.db.QueryRowContext(ctx, `
select is_deleted
from environments
where id = $1::uuid
`, envId).Scan(&isDeleted)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil, fmt.Errorf("environment id %q not found: %w", envId, ErrNotFound)
		}
		if qerr != nil {
			return nil, qerr
		}
		if isDeleted {
			return nil, fmt.Errorf("environment id %q is soft-deleted: %w", envId, ErrNotFound)
		}
		seenEnv[envId] = true
		verifiedEnvIds = append(verifiedEnvIds, envId)
	}

	// 2. 解析每个 env → sibling parent id(同 env 复用 map)。
	type envPlan struct {
		envId           string
		siblingParentId string
	}
	plan := make([]envPlan, 0, len(verifiedEnvIds))
	seenSibling := make(map[string]string) // envId -> siblingParentId
	for _, envId := range verifiedEnvIds {
		siblingParentId, ok := seenSibling[envId]
		if !ok {
			qerr := r.db.QueryRowContext(ctx, `
select id::text
from folders
where environment_id = $1::uuid and code = $2 and level = 1 and is_deleted = false
limit 1
`, envId, parentCode).Scan(&siblingParentId)
			if errors.Is(qerr, sql.ErrNoRows) {
				return nil, fmt.Errorf("env %q has no level=1 folder with code %q: %w", envId, parentCode, ErrNotFound)
			}
			if qerr != nil {
				return nil, qerr
			}
			seenSibling[envId] = siblingParentId
		}
		plan = append(plan, envPlan{envId: envId, siblingParentId: siblingParentId})
	}

	// 3. 单事务批量 INSERT + 每条 recordAudit。
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	created := make([]domain.Entity, 0, len(plan))
	for _, p := range plan {
		id, err := uuidgen.NewUUID()
		if err != nil {
			return nil, err
		}
		var folder Entity
		err = tx.QueryRowContext(ctx, `
insert into folders (id, environment_id, parent_id, level, code, name, comment, created_by, updated_by)
values ($1, $2, $3, 2, $4, $5, $6, $7, $7)
returning id, code, name, comment, created_by, updated_by, created_at, updated_at
`, id, p.envId, p.siblingParentId, code, name, comment, actor).Scan(
			&folder.Id, &folder.Code, &folder.Name, &folder.Comment,
			&folder.CreatedBy, &folder.UpdatedBy, &folder.CreatedAt, &folder.UpdatedAt,
		)
		if err != nil {
			// 唯一索引冲突 (env, parent_id, code) 翻译为 ErrConflict
			return nil, translatePgErr(err)
		}
		folder.ParentId = p.siblingParentId
		r.fillEntityLabels(&folder)
		if err := recordAuditTx(ctx, tx, actor, "folder", folder.Id, "create", nil); err != nil {
			return nil, err
		}
		created = append(created, folder)
	}
	return created, tx.Commit()
}

// CreateTopLevelFoldersInEnvs 批量跨环境创建 level=1 folder(顶层 folder,parent_id=NULL)。
//
// 与 CreateFoldersAcrossEnvs 的区别:不需要父 folder,只需 envIds + code/name/comment;
// 每个 env 下创建 1 个 level=1 folder,parent_id=NULL,environment_id=env.id。
//
// 整批 1 个事务,任一 env 不存在 / 目标 code 已在该 env 下存在 → 整体回滚。
// 适用场景:前端传 `{ "level": 1, "code": "globals", "envList": ["<env-id-1>",
// "<env-id-2>", ...] }` 在多个 env 下一次性创建同名顶层 folder。
func (r *Repository) CreateTopLevelFoldersInEnvs(
	ctx context.Context,
	code, name, comment, actor string,
	envIds []string,
) ([]domain.Entity, error) {
	if len(envIds) == 0 {
		return nil, errors.New("CreateTopLevelFoldersInEnvs requires non-empty envIds")
	}

	// 1. 校验每个 envId 存在 + 未软删;同 id 复用 map 缓存。
	seenEnv := make(map[string]bool, len(envIds))
	verifiedEnvIds := make([]string, 0, len(envIds))
	for _, envId := range envIds {
		if seenEnv[envId] {
			verifiedEnvIds = append(verifiedEnvIds, envId)
			continue
		}
		var isDeleted bool
		qerr := r.db.QueryRowContext(ctx, `
select is_deleted
from environments
where id = $1::uuid
`, envId).Scan(&isDeleted)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil, fmt.Errorf("environment id %q not found: %w", envId, ErrNotFound)
		}
		if qerr != nil {
			return nil, qerr
		}
		if isDeleted {
			return nil, fmt.Errorf("environment id %q is soft-deleted: %w", envId, ErrNotFound)
		}
		seenEnv[envId] = true
		verifiedEnvIds = append(verifiedEnvIds, envId)
	}

	// 2. 单事务批量 INSERT + 每条 recordAudit。
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	created := make([]domain.Entity, 0, len(verifiedEnvIds))
	for _, envId := range verifiedEnvIds {
		id, err := uuidgen.NewUUID()
		if err != nil {
			return nil, err
		}
		var folder Entity
		err = tx.QueryRowContext(ctx, `
insert into folders (id, environment_id, parent_id, level, code, name, comment, created_by, updated_by)
values ($1, $2, NULL, 1, $3, $4, $5, $6, $6)
returning id, code, name, comment, created_by, updated_by, created_at, updated_at
`, id, envId, code, name, comment, actor).Scan(
			&folder.Id, &folder.Code, &folder.Name, &folder.Comment,
			&folder.CreatedBy, &folder.UpdatedBy, &folder.CreatedAt, &folder.UpdatedAt,
		)
		if err != nil {
			// 唯一索引冲突 (env, parent_id=NULL, code) 翻译为 ErrConflict
			return nil, translatePgErr(err)
		}
		// level=1 时 Entity.ParentId 多态语义是"父=env id",这里填 envId 与 CreateFolder 一致。
		folder.ParentId = envId
		r.fillEntityLabels(&folder)
		if err := recordAuditTx(ctx, tx, actor, "folder", folder.Id, "create", nil); err != nil {
			return nil, err
		}
		created = append(created, folder)
	}
	return created, tx.Commit()
}

// ListFolders 列 folder,按调用方给的过滤器分两路:
//
//   - environmentId 非空(level=1 列表):查该 env 下所有 parent_id IS NULL 的 folder
//   - folderParentId 非空(level=2 列表):查该父 folder 下所有 parent_id = $parentId 的 folder
//
// 两路 SELECT 复用 entityReadColumns,但 ParentId 列分别取 environment_id / parent_id,
// 与 CreateFolder 的 Entity.ParentId 多态语义保持一致(level=1 父=env,level=2 父=folder)。
//
// v7:caller 通过 user.UserId 透传,SQL 在 WHERE 末尾追加 narrowing 子句,按 (folder, env, project, org) 链收窄。
func (r *Repository) ListFolders(ctx context.Context, callerUserId, envId, parentId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	if envId == "" && parentId == "" {
		return domain.PaginatedResult[Entity]{}, errors.New("ListFolders requires envId or parentId")
	}
	if envId != "" && parentId != "" {
		return domain.PaginatedResult[Entity]{}, errors.New("ListFolders accepts only one of envId or parentId")
	}

	cte := userReadScopeCTE()
	// folder 表不持 project_id / org_id,需要 join env + project 暴露层级。
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "folder", column: "t.id"},
		{scopeType: "folder", column: "t.parent_id"},
		{scopeType: "environment", column: "t.environment_id"},
		{scopeType: "project", column: "e.project_id"},
		{scopeType: "organization", column: "p.org_id"},
	})

	var (
		countQuery, query string
		cols              string
		scanInto          func(*Entity) []any
		args              []any
	)
	// 注意:CTE 占用 $1=callerUserId, $2=permissionCode,后续占位从 $3 开始。
	if envId != "" {
		cols, scanInto = entityReadColumns("environment_id")
		countQuery = cte + fmt.Sprintf(`
select count(*)
from folders t
join environments e on e.id = t.environment_id
join projects p on p.id = e.project_id
where t.environment_id = $3::uuid
  and t.parent_id is null
  and t.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false%s
`, narrow)
		query = cte + fmt.Sprintf(`
select %s
from folders t
join environments e on e.id = t.environment_id
join projects p on p.id = e.project_id
where t.environment_id = $3::uuid
  and t.parent_id is null
  and t.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false%s
order by t.name asc
limit $4 offset $5
`, cols, narrow)
		args = []any{callerUserId, "folder:read", envId}
	} else {
		cols, scanInto = entityReadColumns("parent_id")
		countQuery = cte + fmt.Sprintf(`
select count(*)
from folders t
join environments e on e.id = t.environment_id
join projects p on p.id = e.project_id
where t.parent_id = $3::uuid
  and t.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false%s
`, narrow)
		query = cte + fmt.Sprintf(`
select %s
from folders t
join environments e on e.id = t.environment_id
join projects p on p.id = e.project_id
where t.parent_id = $3::uuid
  and t.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false%s
order by t.name asc
limit $4 offset $5
`, cols, narrow)
		args = []any{callerUserId, "folder:read", parentId}
	}

	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}

	args = append(args, pagination.Limit(), pagination.Offset())

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	defer rows.Close()

	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return domain.PaginatedResult[Entity]{}, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	return domain.PaginatedResult[Entity]{Items: items, Total: total}, nil
}

func (r *Repository) GetFolder(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "folders", id)
}

// ListFolderChildren 批量拉取多个父 folder 下的 level=2 子 folder,按 caller 的
// user_role_bindings 在 (folder, environment, project, organization) 层做 narrowing;
// 复用 v7 的 userReadScopeCTE + scopeNarrowingWhere。
//
// 行为约定:
//   - parentIds 为空时直接返回空 map(不发 SQL,避免 $3::uuid[] 空数组在某些 PG 版本下
//     与 ANY 语义交互产生歧义;空数组走 ANY 本身是 no match,但显式短路更省)。
//   - 返回的 map 始终非 nil(空 parent id 不会出现;对应某个 parent 的空数组是 []Entity{},非 nil),
//     让 handler 直接 `subs := children[it.Id]; if subs == nil { subs = []Entity{} }` 即可。
//   - 单 SQL 内 SQL 参数:$1=callerUserId, $2='folder:read', $3=parentIds(uuid[])。
//   - 输出按 t.parent_id ASC, t.name ASC 排,Go 端按 parent_id 分组组装为 map。
func (r *Repository) ListFolderChildren(ctx context.Context, callerUserId string, parentIds []string) (map[string][]Entity, error) {
	result := make(map[string][]Entity, len(parentIds))
	if len(parentIds) == 0 {
		return result, nil
	}

	cte := userReadScopeCTE()
	// 子 folder 的 Entity.ParentId 在 level=2 时固定为父 folder id,所以读 parent_id 列。
	cols, scanInto := entityReadColumns("parent_id")
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "folder", column: "t.id"},
		{scopeType: "folder", column: "t.parent_id"},
		{scopeType: "environment", column: "t.environment_id"},
		{scopeType: "project", column: "p.id"},
		{scopeType: "organization", column: "p.org_id"},
	})
	query := cte + fmt.Sprintf(`
select %s
from folders t
join environments e on e.id = t.environment_id
join projects p on p.id = e.project_id
where t.parent_id = any($3::uuid[])
  and t.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false%s
order by t.parent_id asc, t.name asc
`, cols, narrow)

	rows, err := r.db.QueryContext(ctx, query, callerUserId, "folder:read", parentIds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var entity Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return nil, err
		}
		r.fillEntityLabels(&entity)
		// entity.ParentId(level=2) 必为父 folder id,即 group key;不可能为空。
		pid := entity.ParentId
		result[pid] = append(result[pid], entity)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Repository) GetFolderByCode(ctx context.Context, environmentId, code string) (Entity, error) {
	return r.getEntityByCodeWithParent(ctx, "folders", "environment_id", environmentId, code)
}

// GetFolderEnvId 返回一个 folder 所属的 environment id。
// level=1 / level=2 folder 都直接持有 environment_id 字段,不需要向上递归。
// 给 controller 在创建 level=2 folder 时反查 env 用,供 RBAC scope 检查与 INSERT。
func (r *Repository) GetFolderEnvId(ctx context.Context, folderId string) (string, error) {
	var envId string
	err := r.db.QueryRowContext(ctx, `
select environment_id::text
from folders
where id = $1::uuid and is_deleted = false
`, folderId).Scan(&envId)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return envId, nil
}

// GetFolderContext 返回 cache 同步所需的 folder 全量上下文。
// level=1:parentId 为空(level=1 父是 env,不在 folder 行里);level=2:parentId 是父 folder id。
// 一次性走 1 次 SQL 把 envId / projectId / parentId / level 都取回来,避免 handler
// 在 CreateFolder/UpdateFolder 后再发多次请求拼数据。
func (r *Repository) GetFolderContext(ctx context.Context, folderId string) (string, string, string, int, error) {
	var envId, projectId, parentId string
	var level int
	err := r.db.QueryRowContext(ctx, `
select f.environment_id::text,
       e.project_id::text,
       coalesce(f.parent_id::text, ''),
       f.level
from folders f
join environments e on e.id = f.environment_id
where f.id = $1::uuid and f.is_deleted = false
`, folderId).Scan(&envId, &projectId, &parentId, &level)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", 0, ErrNotFound
	}
	if err != nil {
		return "", "", "", 0, err
	}
	return envId, projectId, parentId, level, nil
}

func (r *Repository) UpdateFolder(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "folders", id, name, comment, actor, "folder")
}

// DeleteFolder 级联软删 folder + 下属 secret。
// 返回 CascadeScope:FolderIds 为 [id] 自身,SecretIds 收集所有被软删的下游 id。
func (r *Repository) DeleteFolder(ctx context.Context, id, actor string) (domain.CascadeScope, error) {
	scope := domain.CascadeScope{FolderIds: []string{id}}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return scope, err
	}
	defer tx.Rollback()

	// 1. 软删 folder 自身 + 快照 + 审计
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "folders", id, actor, "folder"); err != nil {
		return scope, err
	}
	// 2. 级联:软删 folder 下所有 secret
	scope.SecretIds, err = softDeleteByParentTxReturning(ctx, tx, "secrets", "folder_id", id, actor)
	if err != nil {
		return scope, err
	}
	return scope, tx.Commit()
}

func (r *Repository) CreateSecret(ctx context.Context, folderId, key, comment, actor string, ciphertext SecretCiphertext) (Secret, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, err
	}
	defer tx.Rollback()

	id, err := uuidgen.NewUUID()
	if err != nil {
		return Secret{}, err
	}
	payload, err := json.Marshal(ciphertext)
	if err != nil {
		return Secret{}, err
	}

	var secret Secret
	err = tx.QueryRowContext(ctx, `
insert into secrets (id, folder_id, key, value_ciphertext, comment, version, created_by, updated_by)
values ($1, $2, $3, $4, $5, 1, $6, $6)
returning id, folder_id, key, comment, version, created_by, updated_by, created_at, updated_at
`, id, folderId, key, string(payload), comment, actor).Scan(
		&secret.Id, &secret.FolderId, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if err != nil {
		return Secret{}, translatePgErr(err)
	}
	r.fillSecretLabels(&secret)
	if err := recordAuditTx(ctx, tx, actor, "secret", secret.Id, "create", payload); err != nil {
		return Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		return Secret{}, err
	}
	return r.GetSecret(ctx, secret.Id)
}

// BatchCreateSecrets 单事务批量创建 secret + 1 条 batch audit。
// N 条 INSERT,每条 RETURNING id;任一条失败(unique violation 等)→ rollback 整批。
// 全部 INSERT 成功后,1 条 audit_records(action="create_batch",
// resource_type="folder", resource_id=template folder id,
// encrypted_value=jsonb([{envCode, key, secretId}, ...])。
// commit 后用 r.GetSecret 拉每条的完整 metadata(带 path / 4 级 codes)。
func (r *Repository) BatchCreateSecrets(ctx context.Context, items []store.BatchCreateSecretItem) ([]Secret, error) {
	if len(items) == 0 {
		return []Secret{}, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 预生成 uuid + 序列化 ciphertext。任一条 serialize 失败 → 整批 abort。
	type pending struct {
		item    store.BatchCreateSecretItem
		id      string
		payload []byte
	}
	pendings := make([]pending, 0, len(items))
	for _, it := range items {
		uid, err := uuidgen.NewUUID()
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(it.Ciphertext)
		if err != nil {
			return nil, err
		}
		pendings = append(pendings, pending{item: it, id: uid, payload: payload})
	}

	// 按 envCode 分组收集:同 folder 跨多个 secretList item 会共享 envCode。
	// 简化:每个 (envCode, key) 单独 audit,template folder id 来自任一 item。
	auditEntries := make([]map[string]any, 0, len(pendings))
	var templateFolderId string
	insertedIds := make([]string, 0, len(pendings))

	for _, p := range pendings {
		var secretId string
		err := tx.QueryRowContext(ctx, `
insert into secrets (id, folder_id, key, value_ciphertext, comment, version, created_by, updated_by)
values ($1, $2, $3, $4, $5, 1, $6, $6)
returning id
`, p.id, p.item.FolderId, p.item.Key, string(p.payload), p.item.Comment, p.item.Actor).Scan(&secretId)
		if err != nil {
			return nil, translatePgErr(err)
		}
		insertedIds = append(insertedIds, secretId)
		if templateFolderId == "" {
			templateFolderId = p.item.FolderId
		}
		// 解析 envCode:folderId → folder.environment_id → environments.code。
		// 在事务内通过 1 次额外 join 拿到,避免 service 层预扫 N 次。
		var envCode string
		if err := tx.QueryRowContext(ctx, `
select e.code from folders f join environments e on e.id = f.environment_id where f.id = $1::uuid
`, p.item.FolderId).Scan(&envCode); err != nil {
			return nil, fmt.Errorf("resolve env code for folder %s: %w", p.item.FolderId, err)
		}
		auditEntries = append(auditEntries, map[string]any{
			"envCode":  envCode,
			"key":      p.item.Key,
			"secretId": secretId,
		})
	}

	// 1 条 batch audit。
	auditPayload, err := json.Marshal(auditEntries)
	if err != nil {
		return nil, fmt.Errorf("marshal batch create audit: %w", err)
	}
	if err := recordAuditTx(ctx, tx, items[0].Actor, "folder", templateFolderId, "create_batch", auditPayload); err != nil {
		return nil, fmt.Errorf("record create_batch audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// commit 后拉每条完整 metadata。
	out := make([]Secret, 0, len(insertedIds))
	for _, id := range insertedIds {
		sec, err := r.GetSecret(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("post-commit GetSecret(%s): %w", id, err)
		}
		out = append(out, sec)
	}
	return out, nil
}

func (r *Repository) UpdateSecret(ctx context.Context, id, key, comment, actor string, ciphertext SecretCiphertext) (Secret, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, err
	}
	defer tx.Rollback()

	payload, err := json.Marshal(ciphertext)
	if err != nil {
		return Secret{}, err
	}

	var secret Secret
	err = tx.QueryRowContext(ctx, `
update secrets
set key = $2, value_ciphertext = $3, comment = $4, version = version + 1, updated_by = $5, updated_at = now()
where id = $1 and is_deleted = false
returning id, folder_id, key, comment, version, created_by, updated_by, created_at, updated_at
`, id, key, string(payload), comment, actor).Scan(
		&secret.Id, &secret.FolderId, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, translatePgErr(err)
	}
	r.fillSecretLabels(&secret)
	if err := recordAuditTx(ctx, tx, actor, "secret", secret.Id, "update", payload); err != nil {
		return Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		return Secret{}, err
	}
	return r.GetSecret(ctx, secret.Id)
}

func (r *Repository) GetSecret(ctx context.Context, id string) (Secret, error) {
	var secret Secret
	err := r.db.QueryRowContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.id = $1 and s.is_deleted = false
`, id).Scan(
		&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &secret.Comment, &secret.Version,
		&secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, err
	}
	r.fillSecretLabels(&secret)
	secret.Path = buildSecretPath(secret)
	return secret, nil
}

func (r *Repository) GetSecretByKey(ctx context.Context, folderId, key string) (Secret, error) {
	var secret Secret
	err := r.db.QueryRowContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.folder_id = $1::uuid and s.key = $2 and s.is_deleted = false
`, folderId, key).Scan(
		&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &secret.Comment, &secret.Version,
		&secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, err
	}
	r.fillSecretLabels(&secret)
	secret.Path = buildSecretPath(secret)
	return secret, nil
}

// GetSecretByPath 用 4 级 code + key 一次 SQL 解析到 secret 元数据,5 表 join 走 4 个
// (parent_id, code) where is_deleted = false 唯一索引,执行计划为 4 步 index-nested-loop。
// 任何一段 code 找不到 → 0 rows → 返回 ErrNotFound。Path 字段由 buildSecretPath 自动拼接。
func (r *Repository) GetSecretByPath(ctx context.Context, orgCode, projectCode, envCode, folderCode, key string) (Secret, error) {
	var secret Secret
	err := r.db.QueryRowContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f
  on f.id = s.folder_id
 and f.code = $5
 and f.is_deleted = false
join environments e
  on e.id = f.environment_id
 and e.code = $4
 and e.is_deleted = false
join projects p
  on p.id = e.project_id
 and p.code = $3
 and p.is_deleted = false
join organizations o
  on o.id = p.org_id
 and o.code = $2
 and o.is_deleted = false
where s.key = $1
  and s.is_deleted = false
limit 1
`, key, orgCode, projectCode, envCode, folderCode).Scan(
		&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &secret.Comment, &secret.Version,
		&secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, err
	}
	r.fillSecretLabels(&secret)
	secret.Path = buildSecretPath(secret)
	return secret, nil
}

func (r *Repository) GetSecretCiphertext(ctx context.Context, id string) (Secret, SecretCiphertext, error) {
	var secret Secret
	var payload []byte
	err := r.db.QueryRowContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.value_ciphertext, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.id = $1 and s.is_deleted = false
`, id).Scan(
		&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &payload, &secret.Comment, &secret.Version,
		&secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, SecretCiphertext{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, SecretCiphertext{}, err
	}
	var ciphertext SecretCiphertext
	if err := json.Unmarshal(payload, &ciphertext); err != nil {
		return Secret{}, SecretCiphertext{}, err
	}
	r.fillSecretLabels(&secret)
	secret.Path = buildSecretPath(secret)
	return secret, ciphertext, nil
}

func (r *Repository) ListSecrets(ctx context.Context, callerUserId, action string, filter ListFilter, pagination Pagination) (domain.PaginatedResult[Secret], error) {
	cte := userReadScopeCTE()
	// secret 表不持 project_id / org_id,需 join 4 张表暴露完整 ancestor 链。
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "secret", column: "s.id"},
		{scopeType: "folder", column: "s.folder_id"},
		{scopeType: "folder", column: "f.parent_id"},
		{scopeType: "environment", column: "e.id"},
		{scopeType: "project", column: "p.id"},
		{scopeType: "organization", column: "o.id"},
	})

	var total int64
	err := r.db.QueryRowContext(ctx, cte+fmt.Sprintf(`
select count(*)
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and f.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false
  and o.is_deleted = false
  and ($3 = '' or e.id = $3::uuid)
  and ($4 = '' or s.folder_id = $4::uuid)
  and ($5 = '' or s.key ilike '%%' || $5 || '%%')
  and ($6 = '' or p.id = $6::uuid)%s
`, narrow), callerUserId, action, filter.EnvironmentId, filter.FolderId, filter.Keyword, filter.ProjectId).Scan(&total)
	if err != nil {
		return domain.PaginatedResult[Secret]{}, err
	}

	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and f.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false
  and o.is_deleted = false
  and ($3 = '' or e.id = $3::uuid)
  and ($4 = '' or s.folder_id = $4::uuid)
  and ($5 = '' or s.key ilike '%%' || $5 || '%%')
  and ($6 = '' or p.id = $6::uuid)%s
order by s.key asc
limit $7 offset $8
`, narrow), callerUserId, action, filter.EnvironmentId, filter.FolderId, filter.Keyword, filter.ProjectId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[Secret]{}, err
	}
	defer rows.Close()

	var items []Secret
	for rows.Next() {
		var secret Secret
		if err := rows.Scan(
			&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &secret.Comment, &secret.Version,
			&secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
		); err != nil {
			return domain.PaginatedResult[Secret]{}, err
		}
		r.fillSecretLabels(&secret)
		secret.Path = buildSecretPath(secret)
		items = append(items, secret)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[Secret]{}, err
	}
	return domain.PaginatedResult[Secret]{Items: items, Total: total}, nil
}

// ListSecretsWithCiphertext 同 ListSecrets,额外返回 value_ciphertext(以
// SecretCacheRecord 形式),专供 service.Search 在填 Values 字段时一次拿全
// metadata + ciphertext,避免 N+1 拉 id 再批量取 ciphertext。
//
// cascade narrowing 用 caller 传入的 action(secret:search);Values 字段的
// "是否有权解密"由 service 层对每行单独 authorizer.Allow("secret:reveal") 判定,
// repo 不做这层判定(走的是 search 而非 reveal 的 narrowing)。
func (r *Repository) ListSecretsWithCiphertext(
	ctx context.Context,
	callerUserId, action string,
	filter domain.ListFilter,
	pagination domain.Pagination,
) (domain.PaginatedResult[SecretCacheRecord], error) {
	cte := userReadScopeCTE()
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "secret", column: "s.id"},
		{scopeType: "folder", column: "s.folder_id"},
		{scopeType: "folder", column: "f.parent_id"},
		{scopeType: "environment", column: "e.id"},
		{scopeType: "project", column: "p.id"},
		{scopeType: "organization", column: "o.id"},
	})

	var total int64
	err := r.db.QueryRowContext(ctx, cte+fmt.Sprintf(`
select count(*)
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and f.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false
  and o.is_deleted = false
  and ($3 = '' or e.id = $3::uuid)
  and ($4 = '' or s.folder_id = $4::uuid)
  and ($5 = '' or s.key ilike '%%' || $5 || '%%')
  and ($6 = '' or p.id = $6::uuid)%s
`, narrow), callerUserId, action, filter.EnvironmentId, filter.FolderId, filter.Keyword, filter.ProjectId).Scan(&total)
	if err != nil {
		return domain.PaginatedResult[SecretCacheRecord]{}, err
	}

	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.value_ciphertext, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and f.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false
  and o.is_deleted = false
  and ($3 = '' or e.id = $3::uuid)
  and ($4 = '' or s.folder_id = $4::uuid)
  and ($5 = '' or s.key ilike '%%' || $5 || '%%')
  and ($6 = '' or p.id = $6::uuid)%s
order by s.key asc, e.code asc
limit $7 offset $8
`, narrow), callerUserId, action, filter.EnvironmentId, filter.FolderId, filter.Keyword, filter.ProjectId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[SecretCacheRecord]{}, err
	}
	defer rows.Close()

	var items []SecretCacheRecord
	for rows.Next() {
		var record SecretCacheRecord
		if err := rows.Scan(
			&record.Secret.Id, &record.Secret.OrgId, &record.Secret.OrgCode, &record.Secret.ProjectId, &record.Secret.ProjectCode, &record.Secret.EnvironmentId, &record.Secret.EnvironmentCode, &record.Secret.FolderId, &record.Secret.FolderCode, &record.Secret.Key, &record.ValueCiphertext, &record.Secret.Comment, &record.Secret.Version,
			&record.Secret.CreatedBy, &record.Secret.UpdatedBy, &record.Secret.CreatedAt, &record.Secret.UpdatedAt,
		); err != nil {
			return domain.PaginatedResult[SecretCacheRecord]{}, err
		}
		r.fillSecretLabels(&record.Secret)
		record.Secret.Path = buildSecretPath(record.Secret)
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[SecretCacheRecord]{}, err
	}
	return domain.PaginatedResult[SecretCacheRecord]{Items: items, Total: total}, nil
}

// BatchRevealSecretsByPath 一次性按 folder 路径 + 可选 keys 拉取 secret 明文所需的 metadata + ciphertext。
// 复用 v7 的 userReadScopeCTE + narrowingPredicate + scopeNarrowingWhere,做 secret:reveal 权限的 cascade narrowing。
// keys 为空时返回 folder 下所有 secret(无分页、无上限);返回顺序按 s.key ASC,方便 service 端做 notFound diff。
// 返回 ([]Secret, [][]byte) 两个等长切片:Secret 不含 value,ciphertext 让 service 层解密后填 Secret.Value。
func (r *Repository) BatchRevealSecretsByPath(
	ctx context.Context,
	callerUserId, action, orgCode, projectCode, envCode, folderCode string,
	keys []string,
) ([]Secret, [][]byte, error) {
	cte := userReadScopeCTE()
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "secret", column: "s.id"},
		{scopeType: "folder", column: "s.folder_id"},
		{scopeType: "folder", column: "f.parent_id"},
		{scopeType: "environment", column: "e.id"},
		{scopeType: "project", column: "p.id"},
		{scopeType: "organization", column: "o.id"},
	})
	// 4 段 code 解析(secret:reveal 不走 GetSecretByPath 的 5 表 join,而是 4 表 join + 1 个 WHERE 过滤),
	// 用 4 步 index-nested-loop 走 (parent_id, code) 唯一索引。
	query := cte + fmt.Sprintf(`
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.value_ciphertext, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f
  on f.id = s.folder_id
 and f.code = $6
 and f.is_deleted = false
join environments e
  on e.id = f.environment_id
 and e.code = $5
 and e.is_deleted = false
join projects p
  on p.id = e.project_id
 and p.code = $4
 and p.is_deleted = false
join organizations o
  on o.id = p.org_id
 and o.code = $3
 and o.is_deleted = false
where s.is_deleted = false
  and (cardinality($7::text[]) = 0 or s.key = any($7::text[]))%s
order by s.key asc
`, narrow)
	rows, err := r.db.QueryContext(ctx, query, callerUserId, action, orgCode, projectCode, envCode, folderCode, keys)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var (
		items      []Secret
		ciphertext [][]byte
	)
	for rows.Next() {
		var (
			secret  Secret
			payload []byte
		)
		if err := rows.Scan(
			&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &payload, &secret.Comment, &secret.Version,
			&secret.CreatedBy, &secret.UpdatedBy,
			&secret.CreatedAt, &secret.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		r.fillSecretLabels(&secret)
		secret.Path = buildSecretPath(secret)
		items = append(items, secret)
		ciphertext = append(ciphertext, payload)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, ciphertext, nil
}

// ListSecretsByProjectFolderKey 按 (project, folderCode, key) 维度 + env 过滤列表
// 拉取 secret metadata + ciphertext,跨 env 一次性 reveal 用。
//
// 与 BatchRevealSecretsByPath 的差异:
//   - 入参是 (projectId, folderCode, key) 维度,不依赖 4 级 code 全路径;
//   - 跨 env 拉取(envCodes 控制白名单,空数组走"项目下所有 env"兜底);
//   - 排序按 e.code ASC,方便 service 端按 envCode 索引。
//
// 复用 v7 的 userReadScopeCTE + narrowingPredicate + scopeNarrowingWhere 做 secret:reveal 权限的
// cascade narrowing。envCodes 空时 cardinality($6::text[]) = 0 为真,SQL 走「不限 env」分支;
// 本接口 service 层已保证 cleaned 非空,但 SQL 仍写兜底以防直调 repo 的场景。
func (r *Repository) ListSecretsByProjectFolderKey(
	ctx context.Context,
	callerUserId, action, projectId, folderCode, key string,
	envCodes []string,
) ([]Secret, [][]byte, error) {
	cte := userReadScopeCTE()
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "secret", column: "s.id"},
		{scopeType: "folder", column: "s.folder_id"},
		{scopeType: "folder", column: "f.parent_id"},
		{scopeType: "environment", column: "e.id"},
		{scopeType: "project", column: "p.id"},
		{scopeType: "organization", column: "o.id"},
	})
	query := cte + fmt.Sprintf(`
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.value_ciphertext, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f
  on f.id = s.folder_id
 and f.code = $4
 and f.is_deleted = false
join environments e
  on e.id = f.environment_id
 and e.is_deleted = false
 and (cardinality($6::text[]) = 0 or e.code = any($6::text[]))
join projects p
  on p.id = e.project_id
 and p.id = $3::uuid
 and p.is_deleted = false
join organizations o
  on o.id = p.org_id
 and o.is_deleted = false
where s.is_deleted = false
  and s.key = $5%s
order by e.code asc
`, narrow)
	rows, err := r.db.QueryContext(ctx, query, callerUserId, action, projectId, folderCode, key, envCodes)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var (
		items      []Secret
		ciphertext [][]byte
	)
	for rows.Next() {
		var (
			secret  Secret
			payload []byte
		)
		if err := rows.Scan(
			&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &payload, &secret.Comment, &secret.Version,
			&secret.CreatedBy, &secret.UpdatedBy,
			&secret.CreatedAt, &secret.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		r.fillSecretLabels(&secret)
		secret.Path = buildSecretPath(secret)
		items = append(items, secret)
		ciphertext = append(ciphertext, payload)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, ciphertext, nil
}

// ListSecretsInProjectByEnvs 按 project + (可选 folderCode) + env 列表拉取 secret
// 的 metadata + ciphertext,用于 ListAcrossEnvs 的"key 为空"分支。
//
// 与 ListSecretsByProjectFolderKey 的差异:
//   - 不带 key 过滤,直接返回 (env × folder × secret) 序列;
//   - folderCode 为空时走"项目下所有 folder"兜底,非空时 SQL 限定到该 folder;
//   - service 层按 (folderCode, key) 滚动聚合为多个 SecretAcrossEnvs;
//   - 排序按 (e.code ASC, f.code ASC, s.key ASC) 保证 service 端分组时输入稳定。
//
// 复用 v7 的 userReadScopeCTE + narrowingPredicate + scopeNarrowingWhere 做 secret:reveal
// 权限的 cascade narrowing。envCodes 空时走 cardinality($4::text[]) = 0 兜底;
// folderCode 空时走 $5::text = ” 兜底。
func (r *Repository) ListSecretsInProjectByEnvs(
	ctx context.Context,
	callerUserId, action, projectId, folderCode string,
	envCodes []string,
) ([]Secret, [][]byte, error) {
	cte := userReadScopeCTE()
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "secret", column: "s.id"},
		{scopeType: "folder", column: "s.folder_id"},
		{scopeType: "folder", column: "f.parent_id"},
		{scopeType: "environment", column: "e.id"},
		{scopeType: "project", column: "p.id"},
		{scopeType: "organization", column: "o.id"},
	})
	query := cte + fmt.Sprintf(`
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.value_ciphertext, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f
  on f.id = s.folder_id
 and f.is_deleted = false
 and ($5::text = '' or f.code = $5)
join environments e
  on e.id = f.environment_id
 and e.is_deleted = false
 and (cardinality($4::text[]) = 0 or e.code = any($4::text[]))
join projects p
  on p.id = e.project_id
 and p.id = $3::uuid
 and p.is_deleted = false
join organizations o
  on o.id = p.org_id
 and o.is_deleted = false
where s.is_deleted = false%s
order by e.code asc, f.code asc, s.key asc
`, narrow)
	rows, err := r.db.QueryContext(ctx, query, callerUserId, action, projectId, envCodes, folderCode)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var (
		items      []Secret
		ciphertext [][]byte
	)
	for rows.Next() {
		var (
			secret  Secret
			payload []byte
		)
		if err := rows.Scan(
			&secret.Id, &secret.OrgId, &secret.OrgCode, &secret.ProjectId, &secret.ProjectCode, &secret.EnvironmentId, &secret.EnvironmentCode, &secret.FolderId, &secret.FolderCode, &secret.Key, &payload, &secret.Comment, &secret.Version,
			&secret.CreatedBy, &secret.UpdatedBy,
			&secret.CreatedAt, &secret.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		r.fillSecretLabels(&secret)
		secret.Path = buildSecretPath(secret)
		items = append(items, secret)
		ciphertext = append(ciphertext, payload)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, ciphertext, nil
}

func (r *Repository) ListSecretCacheRecords(ctx context.Context) ([]SecretCacheRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.value_ciphertext, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
order by s.key asc
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []SecretCacheRecord
	for rows.Next() {
		var record SecretCacheRecord
		if err := rows.Scan(
			&record.Secret.Id,
			&record.Secret.OrgId,
			&record.Secret.OrgCode,
			&record.Secret.ProjectId,
			&record.Secret.ProjectCode,
			&record.Secret.EnvironmentId,
			&record.Secret.EnvironmentCode,
			&record.Secret.FolderId,
			&record.Secret.FolderCode,
			&record.Secret.Key,
			&record.ValueCiphertext,
			&record.Secret.Comment,
			&record.Secret.Version,
			&record.Secret.CreatedBy,
			&record.Secret.UpdatedBy,
			&record.Secret.CreatedAt,
			&record.Secret.UpdatedAt,
		); err != nil {
			return nil, err
		}
		r.fillSecretLabels(&record.Secret)
		record.Secret.Path = buildSecretPath(record.Secret)
		items = append(items, record)
	}
	return items, rows.Err()
}

func (r *Repository) DeleteSecret(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "secrets", id, actor, "secret")
}

func (r *Repository) ListAuditRecords(ctx context.Context, resourceType, resourceId string, pagination Pagination) (domain.PaginatedResult[AuditRecord], error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `
select count(*)
from audit_records
where ($1 = '' or resource_type = $1)
  and ($2 = '' or resource_id = $2::uuid)
`, resourceType, resourceId).Scan(&total)
	if err != nil {
		return domain.PaginatedResult[AuditRecord]{}, err
	}

	rows, err := r.db.QueryContext(ctx, `
select id, actor, resource_type, resource_id, action, coalesce(encrypted_value, 'null'::jsonb), created_at
from audit_records
where ($1 = '' or resource_type = $1)
  and ($2 = '' or resource_id = $2::uuid)
order by created_at desc
limit $3 offset $4
`, resourceType, resourceId, pagination.Limit(), pagination.Offset())
	if err != nil {
		return domain.PaginatedResult[AuditRecord]{}, err
	}
	defer rows.Close()

	var items []AuditRecord
	for rows.Next() {
		var item AuditRecord
		if err := rows.Scan(&item.Id, &item.Actor, &item.ResourceType, &item.ResourceId, &item.Action, &item.EncryptedValue, &item.CreatedAt); err != nil {
			return domain.PaginatedResult[AuditRecord]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[AuditRecord]{}, err
	}
	return domain.PaginatedResult[AuditRecord]{Items: items, Total: total}, nil
}

func (r *Repository) RecordAudit(ctx context.Context, actor, resourceType, resourceId, action string, encryptedValue []byte) error {
	auditId, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	var payload interface{}
	if len(encryptedValue) > 0 {
		payload = json.RawMessage(encryptedValue)
	}
	_, err = r.db.ExecContext(ctx, `
insert into audit_records (id, actor, resource_type, resource_id, action, encrypted_value)
values ($1, $2, $3, $4, $5, $6)
`, auditId, actor, resourceType, resourceId, action, payload)
	return err
}

func (r *Repository) createEntity(ctx context.Context, table, parentColumn, parentId, code, name, comment, actor, resourceType string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	entity, err := r.createEntityTx(ctx, tx, table, parentColumn, parentId, code, name, comment, actor)
	if err != nil {
		return Entity{}, err
	}
	if err := recordAuditTx(ctx, tx, actor, resourceType, entity.Id, "create", nil); err != nil {
		return Entity{}, err
	}
	return entity, tx.Commit()
}

func (r *Repository) listEntities(ctx context.Context, table, parentId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	countQuery := fmt.Sprintf("select count(*) from %s where is_deleted = false", table)
	cols, scanInto := entityReadColumnsForTable(table)
	query := fmt.Sprintf(`
select %s
from %s t
where t.is_deleted = false`, cols, table)
	args := []any{}
	if parentId != "" {
		countQuery += fmt.Sprintf(" and %s = $1::uuid", parentColumn(table))
		query += fmt.Sprintf(" and t.%s = $1::uuid", parentColumn(table))
		args = append(args, parentId)
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}

	args = append(args, pagination.Limit(), pagination.Offset())
	query += fmt.Sprintf(" order by name asc limit $%d offset $%d", len(args)-1, len(args))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	defer rows.Close()

	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return domain.PaginatedResult[Entity]{}, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	if err := rows.Err(); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}
	return domain.PaginatedResult[Entity]{Items: items, Total: total}, nil
}

func (r *Repository) getEntity(ctx context.Context, table, id string) (Entity, error) {
	var entity Entity
	cols, scanInto := entityReadColumnsForTable(table)
	query := fmt.Sprintf(`
select %s
from %s t
where t.id = $1 and t.is_deleted = false`, cols, table)
	err := r.db.QueryRowContext(ctx, query, id).Scan(scanInto(&entity)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
	r.fillEntityLabels(&entity)
	return entity, nil
}

func (r *Repository) getEntityByCode(ctx context.Context, table, code string) (Entity, error) {
	var entity Entity
	cols, scanInto := entityReadColumnsForTable(table)
	query := fmt.Sprintf(`
select %s
from %s t
where t.code = $1 and t.is_deleted = false`, cols, table)
	err := r.db.QueryRowContext(ctx, query, code).Scan(scanInto(&entity)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
	r.fillEntityLabels(&entity)
	return entity, nil
}

func (r *Repository) getEntityByCodeWithParent(ctx context.Context, table, parentColumn, parentId, code string) (Entity, error) {
	var entity Entity
	// 参数 parentColumn 遮蔽了包级 parentColumn 函数;此处值与 parentColumn(table) 等价,直接传。
	cols, scanInto := entityReadColumnsForTable(table)
	query := fmt.Sprintf(`
select %s
from %s t
where t.%s = $1::uuid and t.code = $2 and t.is_deleted = false`, cols, table, parentColumn)
	err := r.db.QueryRowContext(ctx, query, parentId, code).Scan(scanInto(&entity)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
	r.fillEntityLabels(&entity)
	return entity, nil
}

func (r *Repository) updateEntity(ctx context.Context, table, id, name, comment, actor, resourceType string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	returning, scanInto := entityReturningForTable(table)
	var entity Entity
	query := fmt.Sprintf("update %s set name = $2, comment = $3, updated_by = $4, updated_at = now() where id = $1 and is_deleted = false returning %s", table, returning)
	err = tx.QueryRowContext(ctx, query, id, name, comment, actor).Scan(scanInto(&entity)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
	r.fillEntityLabels(&entity)
	if err := recordAuditTx(ctx, tx, actor, resourceType, entity.Id, "update", nil); err != nil {
		return Entity{}, err
	}
	return entity, tx.Commit()
}

func (r *Repository) deleteEntity(ctx context.Context, table, id, actor, resourceType string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, table, id, actor, resourceType); err != nil {
		return err
	}
	return tx.Commit()
}

// snapshotAndSoftDeleteTx 在调用方事务内:
//  1. 拍快照(to_jsonb)到 deleted_records;
//  2. 软删目标行;
//  3. 写 audit_records。
//
// 用于 Delete* 级联场景:父行先按此函数处理,再在同事务里级联软删子行。
func (r *Repository) snapshotAndSoftDeleteTx(ctx context.Context, tx *sql.Tx, table, id, actor, resourceType string) error {
	snapshot, err := snapshotTx(ctx, tx, table, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	key := resourceType + ":" + id
	deletedId, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
insert into deleted_records (id, resource_type, resource_id, resource_key, snapshot, deleted_by)
values ($1, $2, $3, $4, $5, $6)
`, deletedId, resourceType, id, key, snapshot, actor); err != nil {
		return err
	}

	query := fmt.Sprintf("update %s set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now() where id = $1 and is_deleted = false", table)
	result, err := tx.ExecContext(ctx, query, id, actor)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}

	return recordAuditTx(ctx, tx, actor, resourceType, id, "delete", snapshot)
}

// softDeleteByParentTxReturning 在事务内软删某父 id 下的所有未删除行,
// 通过 RETURNING id 收集被软删的行 id,
// 供 DeleteXxx 系列方法回填 CascadeScope 供 cache 失效。
// 无下游时返回空 slice(非 nil),handler 循环 range 不会 nil 报错。
// 用于 folder ← env、secret ← folder 等直系子资源。
func softDeleteByParentTxReturning(ctx context.Context, tx *sql.Tx, table, parentCol, parentId, actor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
update %s
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where %s = $1::uuid and is_deleted = false
returning id::text
`, table, parentCol), parentId, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// softDeleteEnvUnderOrgTx 级联软删 org 下所有 env(env 跨表:env.project_id ∈ projects under org),
// RETURNING id 收集到 []string。
func softDeleteEnvUnderOrgTx(ctx context.Context, tx *sql.Tx, orgId, actor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
update environments
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where project_id in (select id from projects where org_id = $1::uuid)
  and is_deleted = false
returning id::text
`, orgId, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// softDeleteFolderUnderOrgTx 级联软删 org 下所有 folder(沿 project → env → folder),
// RETURNING id 收集到 []string。
func softDeleteFolderUnderOrgTx(ctx context.Context, tx *sql.Tx, orgId, actor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
update folders
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where environment_id in (
  select e.id from environments e
  join projects p on p.id = e.project_id
  where p.org_id = $1::uuid
)
  and is_deleted = false
returning id::text
`, orgId, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// softDeleteFolderUnderProjectTx 级联软删 project 下所有 folder(沿 env → folder),
// RETURNING id 收集到 []string。
func softDeleteFolderUnderProjectTx(ctx context.Context, tx *sql.Tx, projectId, actor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
update folders
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where environment_id in (select id from environments where project_id = $1::uuid)
  and is_deleted = false
returning id::text
`, projectId, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// softDeleteSecretUnderOrgTx 级联软删 org 下所有 secret(沿 project → env → folder → secret),
// RETURNING id 收集到 []string。
func softDeleteSecretUnderOrgTx(ctx context.Context, tx *sql.Tx, orgId, actor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
update secrets
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where folder_id in (
  select f.id from folders f
  join environments e on e.id = f.environment_id
  join projects p on p.id = e.project_id
  where p.org_id = $1::uuid
)
  and is_deleted = false
returning id::text
`, orgId, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// softDeleteSecretsUnderEnvTx 在事务内级联软删 env 下的所有 secret。
// secret 与 env 不直连,要通过 folders.environment_id 间接定位。
// 改返回 []string(被软删的 secret id),供 DeleteEnvironment 收集到 CascadeScope。
func softDeleteSecretsUnderEnvTx(ctx context.Context, tx *sql.Tx, envId, actor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
update secrets
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where folder_id in (select id from folders where environment_id = $1::uuid)
  and is_deleted = false
returning id::text
`, envId, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// softDeleteSecretsUnderProjectTx 在事务内级联软删 project 下所有 secret。
// 沿 project → env → folder → secret 链间接定位。
// 改返回 []string,供 DeleteProject 收集到 CascadeScope。
func softDeleteSecretsUnderProjectTx(ctx context.Context, tx *sql.Tx, projectId, actor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
update secrets
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where folder_id in (
  select f.id from folders f
  join environments e on e.id = f.environment_id
  where e.project_id = $1::uuid
)
  and is_deleted = false
returning id::text
`, projectId, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) createEntityTx(ctx context.Context, tx *sql.Tx, table, parentColumn, parentId, code, name, comment, actor string) (Entity, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return Entity{}, err
	}
	returning, scanInto := entityReturning(parentColumn)
	var entity Entity
	if parentColumn == "" {
		query := fmt.Sprintf("insert into %s (id, code, name, comment, created_by, updated_by) values ($1, $2, $3, $4, $5, $5) returning %s", table, returning)
		if err := tx.QueryRowContext(ctx, query, id, code, name, comment, actor).Scan(scanInto(&entity)...); err != nil {
			return Entity{}, translatePgErr(err)
		}
		r.fillEntityLabels(&entity)
		return entity, nil
	}
	query := fmt.Sprintf("insert into %s (id, %s, code, name, comment, created_by, updated_by) values ($1, $2, $3, $4, $5, $6, $6) returning %s", table, parentColumn, returning)
	if err := tx.QueryRowContext(ctx, query, id, parentId, code, name, comment, actor).Scan(scanInto(&entity)...); err != nil {
		return Entity{}, translatePgErr(err)
	}
	r.fillEntityLabels(&entity)
	return entity, nil
}

func (r *Repository) createEnvironmentTx(ctx context.Context, tx *sql.Tx, projectId, code, name, comment, actor string, sortOrder int) (Entity, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return Entity{}, err
	}
	if sortOrder <= 0 {
		sortOrder = defaultEnvironmentSortOrder(code)
	}
	returning, scanInto := environmentReturning()
	var entity Entity
	query := fmt.Sprintf(`
insert into environments (id, project_id, code, name, comment, sort_order, created_by, updated_by)
values ($1, $2, $3, $4, $5, $6, $7, $7)
returning %s`, returning)
	if err := tx.QueryRowContext(ctx, query, id, projectId, code, name, comment, sortOrder, actor).Scan(scanInto(&entity)...); err != nil {
		return Entity{}, translatePgErr(err)
	}
	r.fillEntityLabels(&entity)
	return entity, nil
}

func (r *Repository) fillEntityLabels(entity *Entity) {
	entity.CreatedByLabel = r.userLabel(entity.CreatedBy)
	entity.UpdatedByLabel = r.userLabel(entity.UpdatedBy)
}

func (r *Repository) fillSecretLabels(secret *Secret) {
	secret.CreatedByLabel = r.userLabel(secret.CreatedBy)
	secret.UpdatedByLabel = r.userLabel(secret.UpdatedBy)
}

func (r *Repository) userLabel(actor string) string {
	if r == nil {
		return actor
	}
	return r.userCache.Label(actor)
}

func buildSecretPath(secret Secret) string {
	parts := []string{secret.OrgCode, secret.ProjectCode, secret.EnvironmentCode, secret.FolderCode, secret.Key}
	for _, part := range parts {
		if part == "" {
			return ""
		}
	}
	return strings.Join(parts, ".")
}

func recordAuditTx(ctx context.Context, tx *sql.Tx, actor, resourceType, resourceId, action string, encryptedValue []byte) error {
	auditId, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into audit_records (id, actor, resource_type, resource_id, action, encrypted_value)
values ($1, $2, $3, $4, $5, $6)
`, auditId, actor, resourceType, resourceId, action, encryptedValue)
	return err
}

func snapshotTx(ctx context.Context, tx *sql.Tx, table, id string) ([]byte, error) {
	query := fmt.Sprintf("select to_jsonb(t) from %s t where id = $1 and is_deleted = false", table)
	var snapshot []byte
	err := tx.QueryRowContext(ctx, query, id).Scan(&snapshot)
	return snapshot, err
}

// parentColumn 返回指定表上指向"父资源"的列名。
// 只用于"父资源 = env/project/org 这类非 folder 的情况"。
//
// 注意:folders 表同时持有 environment_id 和 parent_id 两个看似重叠的字段,
// 但本函数只认 environment_id——因为它答的是"该 folder 属于哪个 env",
// 而 parent_id 答的是"该 folder 的父 folder 是谁"(仅 level=2 填写)。
// 两者在 folders 表上**不重叠语义**,不能合并。
func parentColumn(table string) string {
	switch table {
	case "projects":
		return "org_id"
	case "environments":
		return "project_id"
	case "folders":
		return "environment_id"
	case "environment_templates":
		return "org_id"
	default:
		return ""
	}
}

func entityReadColumnsForTable(table string) (cols string, scan func(*Entity) []any) {
	if table == "environments" {
		return environmentReadColumns()
	}
	return entityReadColumns(parentColumn(table))
}

func entityReturningForTable(table string) (cols string, scan func(*Entity) []any) {
	if table == "environments" {
		return environmentReturning()
	}
	return entityReturning(parentColumn(table))
}

func environmentReadColumns() (cols string, scan func(*Entity) []any) {
	return "t.id, t.project_id, t.code, t.name, t.comment, t.sort_order, t.created_by, t.updated_by, t.created_at, t.updated_at",
		func(e *Entity) []any {
			return []any{&e.Id, &e.ParentId, &e.Code, &e.Name, &e.Comment, &e.SortOrder, &e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt}
		}
}

func environmentReturning() (cols string, scan func(*Entity) []any) {
	return "id, project_id, code, name, comment, sort_order, created_by, updated_by, created_at, updated_at",
		func(e *Entity) []any {
			return []any{&e.Id, &e.ParentId, &e.Code, &e.Name, &e.Comment, &e.SortOrder, &e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt}
		}
}

func defaultEnvironmentSortOrder(code string) int {
	switch strings.TrimSpace(code) {
	case "dev":
		return 10
	case "test":
		return 20
	case "sim":
		return 30
	case "prod":
		return 40
	default:
		return 100
	}
}

// entityReadColumns 返回 Entity 读路径的 SELECT 列与对应的 Scan 目标。
// parentCol 是表上指向父级 id 的列(如 projects.org_id),空表示顶层实体(organizations)。
// 返回的列带 "t." 前缀,适用于 SELECT;scan 函数返回的指针列表必须按列顺序传给 rows.Scan。
func entityReadColumns(parentCol string) (cols string, scan func(*Entity) []any) {
	if parentCol == "" {
		return "t.id, t.code, t.name, t.comment, t.created_by, t.updated_by, t.created_at, t.updated_at",
			func(e *Entity) []any {
				return []any{&e.Id, &e.Code, &e.Name, &e.Comment, &e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt}
			}
	}
	return fmt.Sprintf("t.id, t.%s, t.code, t.name, t.comment, t.created_by, t.updated_by, t.created_at, t.updated_at", parentCol),
		func(e *Entity) []any {
			return []any{&e.Id, &e.ParentId, &e.Code, &e.Name, &e.Comment, &e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt}
		}
}

// entityReturning 返回 Entity 写路径 RETURNING 子句的列(无表别名)与对应 Scan 目标。
// parentCol 同 entityReadColumns。
func entityReturning(parentCol string) (cols string, scan func(*Entity) []any) {
	if parentCol == "" {
		return "id, code, name, comment, created_by, updated_by, created_at, updated_at",
			func(e *Entity) []any {
				return []any{&e.Id, &e.Code, &e.Name, &e.Comment, &e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt}
			}
	}
	return fmt.Sprintf("id, %s, code, name, comment, created_by, updated_by, created_at, updated_at", parentCol),
		func(e *Entity) []any {
			return []any{&e.Id, &e.ParentId, &e.Code, &e.Name, &e.Comment, &e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt}
		}
}

// Compile-time guard:确保 Repository 满足 store.ResourceRepository 接口。
var _ store.ResourceRepository = (*Repository)(nil)

// ============================================================
// v7: list 接口按 caller 权限自动收窄可见作用域
// ============================================================
//
// 6 个 list 方法统一在 WHERE 末尾追加一段 narrowing 谓词:
//   1. CTE `user_read_scopes` 算出 caller 持有 `permissionCode` 的所有 (scope_type, scope_id) 对
//   2. 主 WHERE 末尾追加 `AND (EXISTS (global allow) OR <cascade narrowing>)`
//
// narrowing 谓词按资源祖先链 OR 拼接:
//   - org 自己               : t.id IN (... 'organization')
//   - project (含 org cascade): t.id IN (... 'project') OR t.org_id IN (... 'organization')
//   - env (含 project/org 链) : t.id IN (... 'environment') OR t.project_id IN (... 'project') OR t.project.org_id IN (... 'organization')
//   - folder (含 env/project/org 链) ... 以此类推
//
// 行为:无 binding / 空 user_id → CTE 返空 → 全部 list 返空(不 403、不 500,符合"我看的就是我能看的")。
// `secret` / `env_template` 这两种 scope_type 当前没有任何 role 会绑在这两个层级,
// 谓词保留为 false 但不写死,支持未来扩展"对单个 secret/env_template 授权"。
//
// helper 假设:
//   - 调用方已经把 callerUserId 放在 args[0],permissionCode 放在 args[1](secret:list 路径)
//     或写死成权限码(其他 5 个 list 方法用各自的 permission code 文本)。
//   - 主查询 SELECT 列、order/limit/offset 在 helper 之外的 caller 控制。
//   - `$1` 已被 caller 占用于 callerUserId(以及 permissionCode),所以后续 narrowing 中的
//     引用从 `$%d` 自增。

// userReadScopeCTE 返回 WITH 子句 + 内部 SELECT 模板,要求调用方传入 callerUserId ($1)
// 和 permissionCode ($2)。CTE 列名 (scope_type, scope_id) 与 narrowingPredicate 配合。
func userReadScopeCTE() string {
	return `with user_read_scopes as (
  select distinct urb.scope_type, urb.scope_id
  from user_role_bindings urb
  join users u on u.id = urb.user_id
  join roles r on r.id = urb.role_id
  join role_permissions rp on rp.role_id = r.id
  join permissions p on p.id = rp.permission_id
  where u.id = $1
    and p.code = $2
    and (urb.expires_at is null or urb.expires_at > now())
    and urb.is_deleted = false
    and r.is_deleted = false
    and u.is_disabled = false
) `
}

// navigationScopeCTE 是级联选择器使用的权限基础集合。它保留权限所属的
// resource_type，使上级列表可以从下级授权反推出导航祖先，而不把下级权限
// 提升为真正的上级 read 权限。
func navigationScopeCTE() string {
	return `with user_navigation_scopes as (
  select distinct urb.scope_type, urb.scope_id, p.resource_type
  from user_role_bindings urb
  join users u on u.id = urb.user_id
  join roles r on r.id = urb.role_id
  join role_permissions rp on rp.role_id = r.id
  join permissions p on p.id = rp.permission_id
  where u.id = $1
    and (urb.expires_at is null or urb.expires_at > now())
    and urb.is_deleted = false
    and r.is_deleted = false
    and u.is_disabled = false
) `
}

func organizationNavigationCTE() string {
	return navigationScopeCTE() + `,
visible_organizations as (
  select o.id as org_id
  from organizations o
  where o.is_deleted = false
    and exists (
      select 1 from user_navigation_scopes s
      where s.scope_type = 'global'
        and s.resource_type in ('org', 'project', 'env', 'folder', 'secret')
    )
  union
  select s.scope_id
  from user_navigation_scopes s
  where s.scope_type = 'organization'
    and s.scope_id is not null
    and s.resource_type in ('org', 'project', 'env', 'folder', 'secret')
  union
  select p.org_id
  from user_navigation_scopes s
  join projects p on p.id = s.scope_id
  where s.scope_type = 'project'
    and s.resource_type in ('project', 'env', 'folder', 'secret')
    and p.is_deleted = false
  union
  select p.org_id
  from user_navigation_scopes s
  join environments e on e.id = s.scope_id
  join projects p on p.id = e.project_id
  where s.scope_type = 'environment'
    and s.resource_type in ('env', 'folder', 'secret')
    and e.is_deleted = false
    and p.is_deleted = false
  union
  select p.org_id
  from user_navigation_scopes s
  join folders f on f.id = s.scope_id
  join environments e on e.id = f.environment_id
  join projects p on p.id = e.project_id
  where s.scope_type = 'folder'
    and s.resource_type in ('folder', 'secret')
    and f.is_deleted = false
    and e.is_deleted = false
    and p.is_deleted = false
  union
  select p.org_id
  from user_navigation_scopes s
  join secrets sec on sec.id = s.scope_id
  join folders f on f.id = sec.folder_id
  join environments e on e.id = f.environment_id
  join projects p on p.id = e.project_id
  where s.scope_type = 'secret'
    and s.resource_type = 'secret'
    and sec.is_deleted = false
    and f.is_deleted = false
    and e.is_deleted = false
    and p.is_deleted = false
) `
}

func projectNavigationCTE() string {
	return navigationScopeCTE() + `,
visible_projects as (
  select p.id as project_id
  from projects p
  where p.is_deleted = false
    and exists (
      select 1 from user_navigation_scopes s
      where s.scope_type = 'global'
        and s.resource_type in ('project', 'env', 'folder', 'secret')
    )
  union
  select p.id
  from user_navigation_scopes s
  join projects p on p.org_id = s.scope_id
  where s.scope_type = 'organization'
    and s.resource_type in ('project', 'env', 'folder', 'secret')
    and p.is_deleted = false
  union
  select s.scope_id
  from user_navigation_scopes s
  where s.scope_type = 'project'
    and s.scope_id is not null
    and s.resource_type in ('project', 'env', 'folder', 'secret')
  union
  select e.project_id
  from user_navigation_scopes s
  join environments e on e.id = s.scope_id
  where s.scope_type = 'environment'
    and s.resource_type in ('env', 'folder', 'secret')
    and e.is_deleted = false
  union
  select e.project_id
  from user_navigation_scopes s
  join folders f on f.id = s.scope_id
  join environments e on e.id = f.environment_id
  where s.scope_type = 'folder'
    and s.resource_type in ('folder', 'secret')
    and f.is_deleted = false
    and e.is_deleted = false
  union
  select e.project_id
  from user_navigation_scopes s
  join secrets sec on sec.id = s.scope_id
  join folders f on f.id = sec.folder_id
  join environments e on e.id = f.environment_id
  where s.scope_type = 'secret'
    and s.resource_type = 'secret'
    and sec.is_deleted = false
    and f.is_deleted = false
    and e.is_deleted = false
) `
}

func environmentNavigationCTE() string {
	return navigationScopeCTE() + `,
visible_environments as (
  select e.id as environment_id
  from environments e
  where e.is_deleted = false
    and exists (
      select 1 from user_navigation_scopes s
      where s.scope_type = 'global'
        and s.resource_type in ('env', 'folder', 'secret')
    )
  union
  select e.id
  from user_navigation_scopes s
  join projects p on p.org_id = s.scope_id
  join environments e on e.project_id = p.id
  where s.scope_type = 'organization'
    and s.resource_type in ('env', 'folder', 'secret')
    and p.is_deleted = false
    and e.is_deleted = false
  union
  select e.id
  from user_navigation_scopes s
  join environments e on e.project_id = s.scope_id
  where s.scope_type = 'project'
    and s.resource_type in ('env', 'folder', 'secret')
    and e.is_deleted = false
  union
  select s.scope_id
  from user_navigation_scopes s
  where s.scope_type = 'environment'
    and s.scope_id is not null
    and s.resource_type in ('env', 'folder', 'secret')
  union
  select f.environment_id
  from user_navigation_scopes s
  join folders f on f.id = s.scope_id
  where s.scope_type = 'folder'
    and s.resource_type in ('folder', 'secret')
    and f.is_deleted = false
  union
  select f.environment_id
  from user_navigation_scopes s
  join secrets sec on sec.id = s.scope_id
  join folders f on f.id = sec.folder_id
  where s.scope_type = 'secret'
    and s.resource_type = 'secret'
    and sec.is_deleted = false
    and f.is_deleted = false
) `
}

// narrowingPredicate 生成 "OR" 串联的 narrowing 谓词。
// entries 顺序即谓词顺序;每个 entry 给出 (scope_type, 该 scope_type 在主表/join 链上的列)。
// 例如 ListProjects 传 {{"project", "t.id"}, {"organization", "t.org_id"}},
// 展开为:
//
//	t.id IN (select scope_id from user_read_scopes where scope_type = 'project')
//	OR t.org_id IN (select scope_id from user_read_scopes where scope_type = 'organization')
//
// caller's binding chain 自动 cascade,无需 caller 端额外逻辑。
func narrowingPredicate(entries []narrowingEntry) string {
	if len(entries) == 0 {
		return "false"
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf(
			"%s in (select scope_id from user_read_scopes where scope_type = '%s')",
			e.column, e.scopeType,
		))
	}
	return strings.Join(parts, " or ")
}

// narrowingEntry 单个 (scope_type, column) 对。
type narrowingEntry struct {
	scopeType string
	column    string
}

// scopeNarrowingWhere 返回主 WHERE 子句末尾应追加的 narrowing 片段:
//
//	AND (
//	  EXISTS (SELECT 1 FROM user_read_scopes WHERE scope_type = 'global')
//	  OR <narrowingPredicate(entries)>
//	)
//
// callerUserId 与 permissionCode 已在 userReadScopeCTE 的 $1/$2 中绑定。
// 返回的字符串以 " and (" 开头,直接拼接到现有 WHERE 末尾。
func scopeNarrowingWhere(entries []narrowingEntry) string {
	return " and (\n          exists (select 1 from user_read_scopes where scope_type = 'global')\n          or " + narrowingPredicate(entries) + "\n        )"
}
