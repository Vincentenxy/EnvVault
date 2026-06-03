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

func (r *Repository) CacheUserLabel(externalUserId, name string) {
	if r == nil {
		return
	}
	r.userCache.CacheUserLabel(externalUserId, name)
}

func (r *Repository) CreateOrganization(ctx context.Context, code, name, comment, actor string) (Entity, error) {
	return r.createEntity(ctx, "organizations", "", "", code, name, comment, actor, "organization")
}

func (r *Repository) ListOrganizations(ctx context.Context, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	return r.listEntities(ctx, "organizations", "", pagination)
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

func (r *Repository) DeleteOrganization(ctx context.Context, id, actor string, force bool) error {
	if !force {
		// 非强制:先查 active project,只要有一个就拒,避免造成孤儿 org。
		var activeProjects int64
		if err := r.db.QueryRowContext(ctx, `
select count(*) from projects where org_id = $1::uuid and is_deleted = false
`, id).Scan(&activeProjects); err != nil {
			return err
		}
		if activeProjects > 0 {
			return fmt.Errorf("organization has %d active project(s); pass force=true to cascade delete: %w", activeProjects, ErrConflict)
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 软删 org 自身 + 快照 + 审计
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "organizations", id, actor, "organization"); err != nil {
		return err
	}
	// 2. 级联:软删 org 下所有 project
	if err := softDeleteByParentTx(ctx, tx, "projects", "org_id", id, actor); err != nil {
		return err
	}
	// 3. 级联:软删 org 下所有 env(跨表:env.project_id ∈ projects under org)
	if _, err := tx.ExecContext(ctx, `
update environments
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where project_id in (select id from projects where org_id = $1::uuid)
  and is_deleted = false
`, id, actor); err != nil {
		return err
	}
	// 4. 级联:软删 org 下所有 folder(沿 project → env → folder)
	if _, err := tx.ExecContext(ctx, `
update folders
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where environment_id in (
  select e.id from environments e
  join projects p on p.id = e.project_id
  where p.org_id = $1::uuid
)
  and is_deleted = false
`, id, actor); err != nil {
		return err
	}
	// 5. 级联:软删 org 下所有 secret(沿 project → env → folder → secret)
	if _, err := tx.ExecContext(ctx, `
update secrets
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where folder_id in (
  select f.id from folders f
  join environments e on e.id = f.environment_id
  join projects p on p.id = e.project_id
  where p.org_id = $1::uuid
)
  and is_deleted = false
`, id, actor); err != nil {
		return err
	}
	return tx.Commit()
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
		if _, err := r.createEntityTx(ctx, tx, "environments", "project_id", project.Id, env.Code, env.Name, env.Comment, actor); err != nil {
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

func (r *Repository) ListProjects(ctx context.Context, orgId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	return r.listEntities(ctx, "projects", orgId, pagination)
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

func (r *Repository) DeleteProject(ctx context.Context, id, actor string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 软删 project 自身 + 快照 + 审计
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "projects", id, actor, "project"); err != nil {
		return err
	}
	// 2. 级联:软删 project 下所有 env
	if err := softDeleteByParentTx(ctx, tx, "environments", "project_id", id, actor); err != nil {
		return err
	}
	// 3. 级联:软删 project 下所有 folder(跨表:folder.environment_id ∈ envs under project)
	if _, err := tx.ExecContext(ctx, `
update folders
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where environment_id in (select id from environments where project_id = $1::uuid)
  and is_deleted = false
`, id, actor); err != nil {
		return err
	}
	// 4. 级联:软删 project 下所有 secret(跨表,沿 env → folder → secret 链)
	if err := softDeleteSecretsUnderProjectTx(ctx, tx, id, actor); err != nil {
		return err
	}
	return tx.Commit()
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

	env, err := r.createEntityTx(ctx, tx, "environments", "project_id", projectId, code, name, comment, actor)
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

func (r *Repository) ListEnvironments(ctx context.Context, projectId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	return r.listEntities(ctx, "environments", projectId, pagination)
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

func (r *Repository) DeleteEnvironment(ctx context.Context, id, actor string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 软删 env 自身 + 快照 + 审计(同事务)
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "environments", id, actor, "environment"); err != nil {
		return err
	}
	// 2. 级联:软删 env 下所有 folder
	if err := softDeleteByParentTx(ctx, tx, "folders", "environment_id", id, actor); err != nil {
		return err
	}
	// 3. 级联:软删 env 下所有 secret(跨表:secret.folder_id ∈ folders under env)
	if err := softDeleteSecretsUnderEnvTx(ctx, tx, id, actor); err != nil {
		return err
	}
	return tx.Commit()
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

func (r *Repository) ListEnvironmentTemplates(ctx context.Context, orgId string, pagination Pagination) (domain.PaginatedResult[EnvironmentTemplate], error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `
select count(*) from environment_templates
where org_id = $1::uuid and is_deleted = false
`, orgId).Scan(&total); err != nil {
		return domain.PaginatedResult[EnvironmentTemplate]{}, err
	}

	rows, err := r.db.QueryContext(ctx, `
select id, org_id::text, code, name, comment, created_by, updated_by, created_at, updated_at
from environment_templates
where org_id = $1::uuid and is_deleted = false
order by name asc
limit $2 offset $3
`, orgId, pagination.Limit(), pagination.Offset())
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
//             parentFolderId 必须是同 env 下 level=1 folder 的 id。
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

// ListFolders 列 folder,按调用方给的过滤器分两路:
//
//   - environmentId 非空(level=1 列表):查该 env 下所有 parent_id IS NULL 的 folder
//   - folderParentId 非空(level=2 列表):查该父 folder 下所有 parent_id = $parentId 的 folder
//
// 两路 SELECT 复用 entityReadColumns,但 ParentId 列分别取 environment_id / parent_id,
// 与 CreateFolder 的 Entity.ParentId 多态语义保持一致(level=1 父=env,level=2 父=folder)。
func (r *Repository) ListFolders(ctx context.Context, envId, parentId string, pagination Pagination) (domain.PaginatedResult[Entity], error) {
	if envId == "" && parentId == "" {
		return domain.PaginatedResult[Entity]{}, errors.New("ListFolders requires envId or parentId")
	}
	if envId != "" && parentId != "" {
		return domain.PaginatedResult[Entity]{}, errors.New("ListFolders accepts only one of envId or parentId")
	}

	var (
		countQuery, query string
		cols              string
		scanInto          func(*Entity) []any
		args              []any
	)
	if envId != "" {
		cols, scanInto = entityReadColumns("environment_id")
		countQuery = `select count(*) from folders where environment_id = $1::uuid and parent_id is null and is_deleted = false`
		query = fmt.Sprintf(`
select %s
from folders t
where t.environment_id = $1::uuid and t.parent_id is null and t.is_deleted = false
`, cols)
		args = []any{envId}
	} else {
		cols, scanInto = entityReadColumns("parent_id")
		countQuery = `select count(*) from folders where parent_id = $1::uuid and is_deleted = false`
		query = fmt.Sprintf(`
select %s
from folders t
where t.parent_id = $1::uuid and t.is_deleted = false
`, cols)
		args = []any{parentId}
	}

	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return domain.PaginatedResult[Entity]{}, err
	}

	args = append(args, pagination.Limit(), pagination.Offset())
	query += fmt.Sprintf(" order by t.name asc limit $%d offset $%d", len(args)-1, len(args))

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

func (r *Repository) UpdateFolder(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "folders", id, name, comment, actor, "folder")
}

func (r *Repository) DeleteFolder(ctx context.Context, id, actor string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 软删 folder 自身 + 快照 + 审计
	if err := r.snapshotAndSoftDeleteTx(ctx, tx, "folders", id, actor, "folder"); err != nil {
		return err
	}
	// 2. 级联:软删 folder 下所有 secret
	if err := softDeleteByParentTx(ctx, tx, "secrets", "folder_id", id, actor); err != nil {
		return err
	}
	return tx.Commit()
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

func (r *Repository) ListSecrets(ctx context.Context, filter ListFilter, pagination Pagination) (domain.PaginatedResult[Secret], error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `
select count(*)
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
where s.is_deleted = false
  and ($1 = '' or e.id = $1::uuid)
  and ($2 = '' or s.folder_id = $2::uuid)
  and ($3 = '' or s.key ilike '%' || $3 || '%')
`, filter.EnvironmentId, filter.FolderId, filter.Keyword).Scan(&total)
	if err != nil {
		return domain.PaginatedResult[Secret]{}, err
	}

	rows, err := r.db.QueryContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and ($1 = '' or e.id = $1::uuid)
  and ($2 = '' or s.folder_id = $2::uuid)
  and ($3 = '' or s.key ilike '%' || $3 || '%')
order by s.key asc
limit $4 offset $5
`, filter.EnvironmentId, filter.FolderId, filter.Keyword, pagination.Limit(), pagination.Offset())
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
	cols, scanInto := entityReadColumns(parentColumn(table))
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
	cols, scanInto := entityReadColumns(parentColumn(table))
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
	cols, scanInto := entityReadColumns(parentColumn(table))
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
	cols, scanInto := entityReadColumns(parentColumn)
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

	returning, scanInto := entityReturning(parentColumn(table))
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
//   1. 拍快照(to_jsonb)到 deleted_records;
//   2. 软删目标行;
//   3. 写 audit_records。
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

// softDeleteByParentTx 在事务内软删某父 id 下的所有未删除行。
// 用于 folder ← env、secret ← folder 等直系子资源。
func softDeleteByParentTx(ctx context.Context, tx *sql.Tx, table, parentCol, parentId, actor string) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
update %s
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where %s = $1::uuid and is_deleted = false
`, table, parentCol), parentId, actor)
	return err
}

// softDeleteSecretsUnderEnvTx 在事务内级联软删 env 下的所有 secret。
// secret 与 env 不直连,要通过 folders.environment_id 间接定位。
func softDeleteSecretsUnderEnvTx(ctx context.Context, tx *sql.Tx, envId, actor string) error {
	_, err := tx.ExecContext(ctx, `
update secrets
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where folder_id in (select id from folders where environment_id = $1::uuid)
  and is_deleted = false
`, envId, actor)
	return err
}

// softDeleteSecretsUnderProjectTx 在事务内级联软删 project 下所有 secret。
// 沿 project → env → folder → secret 链间接定位。
func softDeleteSecretsUnderProjectTx(ctx context.Context, tx *sql.Tx, projectId, actor string) error {
	_, err := tx.ExecContext(ctx, `
update secrets
set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_by = $2, updated_at = now()
where folder_id in (
  select f.id from folders f
  join environments e on e.id = f.environment_id
  where e.project_id = $1::uuid
)
  and is_deleted = false
`, projectId, actor)
	return err
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
