package postgres

import (
	"context"
	"fmt"

	"envVault/internal/domain"
)

// ListAllOrganizationsForTree 列 caller 可见的全量 organization(无分页),
// 复用 ListOrganizations 的 narrowing 子句,去掉 count/limit/offset。
func (r *Repository) ListAllOrganizationsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error) {
	cte := userReadScopeCTE()
	cols, scanInto := entityReadColumns(parentColumn("organizations"))
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "organization", column: "t.id"},
	})
	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select %s
from organizations t
where t.is_deleted = false%s
order by t.name asc
`, cols, narrow), callerUserId, "org:read")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Entity, 0)
	for rows.Next() {
		var entity domain.Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return nil, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	return items, rows.Err()
}

// ListAllProjectsForTree 列 caller 可见的全量 project(无分页)。
func (r *Repository) ListAllProjectsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error) {
	cte := userReadScopeCTE()
	cols, scanInto := entityReadColumns(parentColumn("projects"))
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "project", column: "t.id"},
		{scopeType: "organization", column: "t.org_id"},
	})
	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select %s
from projects t
where t.is_deleted = false%s
order by t.name asc
`, cols, narrow), callerUserId, "project:read")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Entity, 0)
	for rows.Next() {
		var entity domain.Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return nil, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	return items, rows.Err()
}

// ListAllEnvironmentsForTree 列 caller 可见的全量 environment(无分页)。
// environments 表不持 org_id,join projects 暴露 p.org_id 用于 narrowing。
func (r *Repository) ListAllEnvironmentsForTree(ctx context.Context, callerUserId string) ([]domain.Entity, error) {
	cte := userReadScopeCTE()
	cols, scanInto := entityReadColumns(parentColumn("environments"))
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "environment", column: "t.id"},
		{scopeType: "project", column: "t.project_id"},
		{scopeType: "organization", column: "p.org_id"},
	})
	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select %s
from environments t
join projects p on p.id = t.project_id
where t.is_deleted = false
  and p.is_deleted = false%s
order by t.name asc
`, cols, narrow), callerUserId, "env:read")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Entity, 0)
	for rows.Next() {
		var entity domain.Entity
		if err := rows.Scan(scanInto(&entity)...); err != nil {
			return nil, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	return items, rows.Err()
}

// ListAllFoldersForTree 列 caller 可见的全量 folder(无分页),并携带 tree 组装
// 必需的 level / environment_id / parent_id / project_id 4 个 folder 专属字段。
//
// 收窄链 (folder, environment, project, organization) 与 ListFolders 一致。
// 不分页、不分 envId/parentId 过滤——tree 自己按 ParentId 关系拼。
func (r *Repository) ListAllFoldersForTree(ctx context.Context, callerUserId string) ([]domain.FolderTreeEntry, error) {
	cte := userReadScopeCTE()
	narrow := scopeNarrowingWhere([]narrowingEntry{
		{scopeType: "folder", column: "t.id"},
		{scopeType: "environment", column: "t.environment_id"},
		{scopeType: "project", column: "e.project_id"},
		{scopeType: "organization", column: "p.org_id"},
	})
	// level=1 的 parent_id 是 NULL;COALESCE 成空串便于统一 Scan 到 string。
	rows, err := r.db.QueryContext(ctx, cte+fmt.Sprintf(`
select t.id,
       t.environment_id::text,
       coalesce(t.parent_id::text, ''),
       t.level,
       t.code,
       t.name,
       t.comment,
       t.created_by,
       t.updated_by,
       t.created_at,
       t.updated_at,
       e.project_id::text
from folders t
join environments e on e.id = t.environment_id
join projects p on p.id = e.project_id
where t.is_deleted = false
  and e.is_deleted = false
  and p.is_deleted = false%s
order by t.name asc
`, narrow), callerUserId, "folder:read")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.FolderTreeEntry, 0)
	for rows.Next() {
		var entry domain.FolderTreeEntry
		if err := rows.Scan(
			&entry.Id,
			&entry.EnvironmentId,
			&entry.ParentId,
			&entry.Level,
			&entry.Code,
			&entry.Name,
			&entry.Comment,
			&entry.CreatedBy,
			&entry.UpdatedBy,
			&entry.CreatedAt,
			&entry.UpdatedAt,
			&entry.ProjectId,
		); err != nil {
			return nil, err
		}
		// Entity.ParentId 填 environment_id(level=1 时就是父)或 parent_id(level=2 时是父 folder)。
		// TreeService 不依赖此字段做父子关系(它用 EnvironmentId + ParentId 显式判断),
		// 这里填环境 id 仅作回退(例如 ListFolders 风格接口仍可读)。
		entry.Entity.ParentId = entry.EnvironmentId
		r.fillEntityLabels(&entry.Entity)
		items = append(items, entry)
	}
	return items, rows.Err()
}
