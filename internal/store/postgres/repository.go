package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	uuidgen "envVault/internal/id"
)

var ErrNotFound = errors.New("record not found")

type Repository struct {
	db        *sql.DB
	userCache *UserCache
}

type Entity struct {
	ID             string    `json:"id"`
	Code           string    `json:"code"`
	Name           string    `json:"name"`
	Comment        string    `json:"comment,omitempty"`
	CreatedBy      string    `json:"createdBy"`
	CreatedByLabel string    `json:"createdByLabel"`
	UpdatedBy      string    `json:"updatedBy"`
	UpdatedByLabel string    `json:"updatedByLabel"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type Secret struct {
	ID              string    `json:"id"`
	OrgID           string    `json:"orgId"`
	OrgCode         string    `json:"orgCode"`
	ProjectID       string    `json:"projectId"`
	ProjectCode     string    `json:"projectCode"`
	EnvironmentID   string    `json:"environmentId"`
	EnvironmentCode string    `json:"environmentCode"`
	FolderID        string    `json:"folderId"`
	FolderCode      string    `json:"folderCode"`
	Key             string    `json:"key"`
	Path            string    `json:"path"`
	Value           string    `json:"value,omitempty"`
	Comment         string    `json:"comment,omitempty"`
	Version         int       `json:"version"`
	CreatedBy       string    `json:"createdBy"`
	CreatedByLabel  string    `json:"createdByLabel"`
	UpdatedBy       string    `json:"updatedBy"`
	UpdatedByLabel  string    `json:"updatedByLabel"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type SecretCiphertext struct {
	Algorithm string `json:"algorithm"`
	Nonce     []byte `json:"nonce"`
	Data      []byte `json:"data"`
}

type SecretCacheRecord struct {
	Secret          Secret
	ValueCiphertext json.RawMessage
}

type AuditRecord struct {
	ID             string          `json:"id"`
	Actor          string          `json:"actor"`
	ResourceType   string          `json:"resourceType"`
	ResourceID     string          `json:"resourceId"`
	Action         string          `json:"action"`
	EncryptedValue json.RawMessage `json:"encryptedValue,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type ListFilter struct {
	OrgID         string
	ProjectID     string
	EnvironmentID string
	FolderID      string
	Keyword       string
}

func NewRepository(db *sql.DB, userCache ...*UserCache) *Repository {
	var cache *UserCache
	if len(userCache) > 0 {
		cache = userCache[0]
	}
	return &Repository{db: db, userCache: cache}
}

func (r *Repository) CacheUserLabel(externalUserID, name string) {
	if r == nil {
		return
	}
	r.userCache.Set(externalUserID, name)
}

func (r *Repository) CreateOrganization(ctx context.Context, code, name, comment, actor string) (Entity, error) {
	return r.createEntity(ctx, "organizations", "", "", code, name, comment, actor, "organization")
}

func (r *Repository) ListOrganizations(ctx context.Context, pagination Pagination) (PaginatedResult[Entity], error) {
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

func (r *Repository) DeleteOrganization(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "organizations", id, actor, "organization")
}

func (r *Repository) CreateProject(ctx context.Context, orgID, code, name, comment, actor string, environmentIDs []string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	project, err := r.createEntityTx(ctx, tx, "projects", "org_id", orgID, code, name, comment, actor)
	if err != nil {
		return Entity{}, err
	}

	// If no environment IDs provided, find dev/test/sim/prod from org
	if len(environmentIDs) == 0 {
		rows, err := tx.QueryContext(ctx, `
			select id from environments
			where org_id = $1::uuid and code in ('dev', 'test', 'sim', 'prod') and is_deleted = false
		`, orgID)
		if err != nil {
			return Entity{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var envID string
			if err := rows.Scan(&envID); err != nil {
				return Entity{}, err
			}
			environmentIDs = append(environmentIDs, envID)
		}
		if err := rows.Err(); err != nil {
			return Entity{}, err
		}
	}

	// Associate environments to project
	if err := r.associateEnvironmentsToProjectTx(ctx, tx, project.ID, environmentIDs); err != nil {
		return Entity{}, err
	}

	if err := recordAuditTx(ctx, tx, actor, "project", project.ID, "create", nil); err != nil {
		return Entity{}, err
	}
	return project, tx.Commit()
}

func (r *Repository) ListProjects(ctx context.Context, orgID string, pagination Pagination) (PaginatedResult[Entity], error) {
	return r.listEntities(ctx, "projects", orgID, pagination)
}

func (r *Repository) GetProject(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "projects", id)
}

func (r *Repository) GetProjectByCode(ctx context.Context, orgID, code string) (Entity, error) {
	return r.getEntityByCodeWithParent(ctx, "projects", "org_id", orgID, code)
}

func (r *Repository) UpdateProject(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "projects", id, name, comment, actor, "project")
}

func (r *Repository) DeleteProject(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "projects", id, actor, "project")
}

func (r *Repository) CreateEnvironment(ctx context.Context, orgID, code, name, comment, actor string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	env, err := r.createEntityTx(ctx, tx, "environments", "org_id", orgID, code, name, comment, actor)
	if err != nil {
		return Entity{}, err
	}
	for _, folderName := range []string{"globals", "groups-secrets"} {
		if _, err := r.createEntityTx(ctx, tx, "folders", "environment_id", env.ID, folderName, folderName, "", actor); err != nil {
			return Entity{}, err
		}
	}

	// Associate this environment to all existing projects in the org
	rows, err := tx.QueryContext(ctx, `select id from projects where org_id = $1::uuid and is_deleted = false`, orgID)
	if err != nil {
		return Entity{}, err
	}
	defer rows.Close()

	var projectIDs []string
	for rows.Next() {
		var projID string
		if err := rows.Scan(&projID); err != nil {
			return Entity{}, err
		}
		projectIDs = append(projectIDs, projID)
	}
	if err := rows.Err(); err != nil {
		return Entity{}, err
	}

	if err := r.associateEnvironmentsToProjectsTx(ctx, tx, projectIDs, env.ID); err != nil {
		return Entity{}, err
	}

	if err := recordAuditTx(ctx, tx, actor, "environment", env.ID, "create", nil); err != nil {
		return Entity{}, err
	}
	return env, tx.Commit()
}

func (r *Repository) ListEnvironments(ctx context.Context, orgID string, pagination Pagination) (PaginatedResult[Entity], error) {
	return r.listEntities(ctx, "environments", orgID, pagination)
}

func (r *Repository) ListProjectEnvironments(ctx context.Context, projectID string, pagination Pagination) (PaginatedResult[Entity], error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `
		select count(*)
		from project_environments pe
		join environments e on e.id = pe.environment_id
		where pe.project_id = $1::uuid and e.is_deleted = false
	`, projectID).Scan(&total)
	if err != nil {
		return PaginatedResult[Entity]{}, err
	}

	rows, err := r.db.QueryContext(ctx, `
		select e.id, e.code, e.name, e.comment, e.created_by, e.updated_by, e.created_at, e.updated_at
		from project_environments pe
		join environments e on e.id = pe.environment_id
		where pe.project_id = $1::uuid and e.is_deleted = false
		order by e.name asc
		limit $2 offset $3
	`, projectID, pagination.Limit(), pagination.Offset())
	if err != nil {
		return PaginatedResult[Entity]{}, err
	}
	defer rows.Close()

	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(
			&entity.ID, &entity.Code, &entity.Name, &entity.Comment,
			&entity.CreatedBy, &entity.UpdatedBy,
			&entity.CreatedAt, &entity.UpdatedAt,
		); err != nil {
			return PaginatedResult[Entity]{}, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	if err := rows.Err(); err != nil {
		return PaginatedResult[Entity]{}, err
	}
	return PaginatedResult[Entity]{Items: items, Total: total}, nil
}

func (r *Repository) associateEnvironmentsToProjectTx(ctx context.Context, tx *sql.Tx, projectID string, environmentIDs []string) error {
	for _, envID := range environmentIDs {
		id, err := uuidgen.NewUUID()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			insert into project_environments (id, project_id, environment_id)
			values ($1, $2, $3)
			on conflict (project_id, environment_id) do nothing
		`, id, projectID, envID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) associateEnvironmentsToProjectsTx(ctx context.Context, tx *sql.Tx, projectIDs []string, environmentID string) error {
	for _, projID := range projectIDs {
		id, err := uuidgen.NewUUID()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			insert into project_environments (id, project_id, environment_id)
			values ($1, $2, $3)
			on conflict (project_id, environment_id) do nothing
		`, id, projID, environmentID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) GetEnvironment(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "environments", id)
}

func (r *Repository) GetEnvironmentByCode(ctx context.Context, orgID, code string) (Entity, error) {
	return r.getEntityByCodeWithParent(ctx, "environments", "org_id", orgID, code)
}

func (r *Repository) UpdateEnvironment(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "environments", id, name, comment, actor, "environment")
}

func (r *Repository) DeleteEnvironment(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "environments", id, actor, "environment")
}

func (r *Repository) CreateFolder(ctx context.Context, environmentID, code, name, comment, actor string) (Entity, error) {
	return r.createEntity(ctx, "folders", "environment_id", environmentID, code, name, comment, actor, "folder")
}

func (r *Repository) ListFolders(ctx context.Context, environmentID string, pagination Pagination) (PaginatedResult[Entity], error) {
	return r.listEntities(ctx, "folders", environmentID, pagination)
}

func (r *Repository) GetFolder(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "folders", id)
}

func (r *Repository) GetFolderByCode(ctx context.Context, environmentID, code string) (Entity, error) {
	return r.getEntityByCodeWithParent(ctx, "folders", "environment_id", environmentID, code)
}

func (r *Repository) UpdateFolder(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "folders", id, name, comment, actor, "folder")
}

func (r *Repository) DeleteFolder(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "folders", id, actor, "folder")
}

func (r *Repository) CreateSecret(ctx context.Context, folderID, key, comment, actor string, ciphertext SecretCiphertext) (Secret, error) {
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
`, id, folderID, key, string(payload), comment, actor).Scan(
		&secret.ID, &secret.FolderID, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if err != nil {
		return Secret{}, err
	}
	r.fillSecretLabels(&secret)
	if err := recordAuditTx(ctx, tx, actor, "secret", secret.ID, "create", payload); err != nil {
		return Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		return Secret{}, err
	}
	return r.GetSecret(ctx, secret.ID)
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
		&secret.ID, &secret.FolderID, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, err
	}
	r.fillSecretLabels(&secret)
	if err := recordAuditTx(ctx, tx, actor, "secret", secret.ID, "update", payload); err != nil {
		return Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		return Secret{}, err
	}
	return r.GetSecret(ctx, secret.ID)
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
join project_environments pe on pe.environment_id = e.id
join projects p on p.id = pe.project_id
join organizations o on o.id = p.org_id
where s.id = $1 and s.is_deleted = false
`, id).Scan(
		&secret.ID, &secret.OrgID, &secret.OrgCode, &secret.ProjectID, &secret.ProjectCode, &secret.EnvironmentID, &secret.EnvironmentCode, &secret.FolderID, &secret.FolderCode, &secret.Key, &secret.Comment, &secret.Version,
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

func (r *Repository) GetSecretByKey(ctx context.Context, folderID, key string) (Secret, error) {
	var secret Secret
	err := r.db.QueryRowContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join project_environments pe on pe.environment_id = e.id
join projects p on p.id = pe.project_id
join organizations o on o.id = p.org_id
where s.folder_id = $1::uuid and s.key = $2 and s.is_deleted = false
`, folderID, key).Scan(
		&secret.ID, &secret.OrgID, &secret.OrgCode, &secret.ProjectID, &secret.ProjectCode, &secret.EnvironmentID, &secret.EnvironmentCode, &secret.FolderID, &secret.FolderCode, &secret.Key, &secret.Comment, &secret.Version,
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
join project_environments pe on pe.environment_id = e.id
join projects p on p.id = pe.project_id
join organizations o on o.id = p.org_id
where s.id = $1 and s.is_deleted = false
`, id).Scan(
		&secret.ID, &secret.OrgID, &secret.OrgCode, &secret.ProjectID, &secret.ProjectCode, &secret.EnvironmentID, &secret.EnvironmentCode, &secret.FolderID, &secret.FolderCode, &secret.Key, &payload, &secret.Comment, &secret.Version,
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

func (r *Repository) ListSecrets(ctx context.Context, filter ListFilter, pagination Pagination) (PaginatedResult[Secret], error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `
select count(*)
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join project_environments pe on pe.environment_id = e.id
join projects p on p.id = pe.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and ($1 = '' or o.id = $1::uuid)
  and ($2 = '' or p.id = $2::uuid)
  and ($3 = '' or e.id = $3::uuid)
  and ($4 = '' or s.folder_id = $4::uuid)
  and ($5 = '' or s.key ilike '%' || $5 || '%')
`, filter.OrgID, filter.ProjectID, filter.EnvironmentID, filter.FolderID, filter.Keyword).Scan(&total)
	if err != nil {
		return PaginatedResult[Secret]{}, err
	}

	rows, err := r.db.QueryContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join project_environments pe on pe.environment_id = e.id
join projects p on p.id = pe.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and ($1 = '' or o.id = $1::uuid)
  and ($2 = '' or p.id = $2::uuid)
  and ($3 = '' or e.id = $3::uuid)
  and ($4 = '' or s.folder_id = $4::uuid)
  and ($5 = '' or s.key ilike '%' || $5 || '%')
order by s.key asc
limit $6 offset $7
`, filter.OrgID, filter.ProjectID, filter.EnvironmentID, filter.FolderID, filter.Keyword, pagination.Limit(), pagination.Offset())
	if err != nil {
		return PaginatedResult[Secret]{}, err
	}
	defer rows.Close()

	var items []Secret
	for rows.Next() {
		var secret Secret
		if err := rows.Scan(
			&secret.ID, &secret.OrgID, &secret.OrgCode, &secret.ProjectID, &secret.ProjectCode, &secret.EnvironmentID, &secret.EnvironmentCode, &secret.FolderID, &secret.FolderCode, &secret.Key, &secret.Comment, &secret.Version,
			&secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt,
		); err != nil {
			return PaginatedResult[Secret]{}, err
		}
		r.fillSecretLabels(&secret)
		secret.Path = buildSecretPath(secret)
		items = append(items, secret)
	}
	if err := rows.Err(); err != nil {
		return PaginatedResult[Secret]{}, err
	}
	return PaginatedResult[Secret]{Items: items, Total: total}, nil
}

func (r *Repository) ListSecretCacheRecords(ctx context.Context) ([]SecretCacheRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
select s.id, o.id, o.code, p.id, p.code, e.id, e.code, s.folder_id, f.code, s.key, s.value_ciphertext, s.comment, s.version,
       s.created_by, s.updated_by,
       s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join project_environments pe on pe.environment_id = e.id
join projects p on p.id = pe.project_id
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
			&record.Secret.ID,
			&record.Secret.OrgID,
			&record.Secret.OrgCode,
			&record.Secret.ProjectID,
			&record.Secret.ProjectCode,
			&record.Secret.EnvironmentID,
			&record.Secret.EnvironmentCode,
			&record.Secret.FolderID,
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

func (r *Repository) ListAuditRecords(ctx context.Context, resourceType, resourceID string, pagination Pagination) (PaginatedResult[AuditRecord], error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `
select count(*)
from audit_records
where ($1 = '' or resource_type = $1)
  and ($2 = '' or resource_id = $2::uuid)
`, resourceType, resourceID).Scan(&total)
	if err != nil {
		return PaginatedResult[AuditRecord]{}, err
	}

	rows, err := r.db.QueryContext(ctx, `
select id, actor, resource_type, resource_id, action, coalesce(encrypted_value, 'null'::jsonb), created_at
from audit_records
where ($1 = '' or resource_type = $1)
  and ($2 = '' or resource_id = $2::uuid)
order by created_at desc
limit $3 offset $4
`, resourceType, resourceID, pagination.Limit(), pagination.Offset())
	if err != nil {
		return PaginatedResult[AuditRecord]{}, err
	}
	defer rows.Close()

	var items []AuditRecord
	for rows.Next() {
		var item AuditRecord
		if err := rows.Scan(&item.ID, &item.Actor, &item.ResourceType, &item.ResourceID, &item.Action, &item.EncryptedValue, &item.CreatedAt); err != nil {
			return PaginatedResult[AuditRecord]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return PaginatedResult[AuditRecord]{}, err
	}
	return PaginatedResult[AuditRecord]{Items: items, Total: total}, nil
}

func (r *Repository) RecordAudit(ctx context.Context, actor, resourceType, resourceID, action string) error {
	auditID, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
insert into audit_records (id, actor, resource_type, resource_id, action)
values ($1, $2, $3, $4, $5)
`, auditID, actor, resourceType, resourceID, action)
	return err
}

func (r *Repository) createEntity(ctx context.Context, table, parentColumn, parentID, code, name, comment, actor, resourceType string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	entity, err := r.createEntityTx(ctx, tx, table, parentColumn, parentID, code, name, comment, actor)
	if err != nil {
		return Entity{}, err
	}
	if err := recordAuditTx(ctx, tx, actor, resourceType, entity.ID, "create", nil); err != nil {
		return Entity{}, err
	}
	return entity, tx.Commit()
}

func (r *Repository) listEntities(ctx context.Context, table, parentID string, pagination Pagination) (PaginatedResult[Entity], error) {
	countQuery := fmt.Sprintf("select count(*) from %s where is_deleted = false", table)
	query := fmt.Sprintf(`
select t.id, t.name, t.comment,
       t.code,
       t.created_by, t.updated_by,
       t.created_at, t.updated_at
from %s t
where t.is_deleted = false`, table)
	args := []any{}
	if parentID != "" {
		countQuery += fmt.Sprintf(" and %s = $1::uuid", parentColumn(table))
		query += fmt.Sprintf(" and t.%s = $1::uuid", parentColumn(table))
		args = append(args, parentID)
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return PaginatedResult[Entity]{}, err
	}

	args = append(args, pagination.Limit(), pagination.Offset())
	query += fmt.Sprintf(" order by name asc limit $%d offset $%d", len(args)-1, len(args))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return PaginatedResult[Entity]{}, err
	}
	defer rows.Close()

	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(
			&entity.ID, &entity.Name, &entity.Comment, &entity.Code,
			&entity.CreatedBy, &entity.UpdatedBy,
			&entity.CreatedAt, &entity.UpdatedAt,
		); err != nil {
			return PaginatedResult[Entity]{}, err
		}
		r.fillEntityLabels(&entity)
		items = append(items, entity)
	}
	if err := rows.Err(); err != nil {
		return PaginatedResult[Entity]{}, err
	}
	return PaginatedResult[Entity]{Items: items, Total: total}, nil
}

func (r *Repository) getEntity(ctx context.Context, table, id string) (Entity, error) {
	var entity Entity
	query := fmt.Sprintf(`
select t.id, t.name, t.comment,
       t.code,
       t.created_by, t.updated_by,
       t.created_at, t.updated_at
from %s t
where t.id = $1 and t.is_deleted = false`, table)
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&entity.ID, &entity.Name, &entity.Comment, &entity.Code,
		&entity.CreatedBy, &entity.UpdatedBy,
		&entity.CreatedAt, &entity.UpdatedAt,
	)
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
	query := fmt.Sprintf(`
select t.id, t.name, t.comment,
       t.code,
       t.created_by, t.updated_by,
       t.created_at, t.updated_at
from %s t
where t.code = $1 and t.is_deleted = false`, table)
	err := r.db.QueryRowContext(ctx, query, code).Scan(
		&entity.ID, &entity.Name, &entity.Comment, &entity.Code,
		&entity.CreatedBy, &entity.UpdatedBy,
		&entity.CreatedAt, &entity.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
	r.fillEntityLabels(&entity)
	return entity, nil
}

func (r *Repository) getEntityByCodeWithParent(ctx context.Context, table, parentColumn, parentID, code string) (Entity, error) {
	var entity Entity
	query := fmt.Sprintf(`
select t.id, t.name, t.comment,
       t.code,
       t.created_by, t.updated_by,
       t.created_at, t.updated_at
from %s t
where t.%s = $1::uuid and t.code = $2 and t.is_deleted = false`, table, parentColumn)
	err := r.db.QueryRowContext(ctx, query, parentID, code).Scan(
		&entity.ID, &entity.Name, &entity.Comment, &entity.Code,
		&entity.CreatedBy, &entity.UpdatedBy,
		&entity.CreatedAt, &entity.UpdatedAt,
	)
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

	var entity Entity
	query := fmt.Sprintf("update %s set name = $2, comment = $3, updated_by = $4, updated_at = now() where id = $1 and is_deleted = false returning id, name, comment, code, created_by, updated_by, created_at, updated_at", table)
	err = tx.QueryRowContext(ctx, query, id, name, comment, actor).Scan(
		&entity.ID, &entity.Name, &entity.Comment, &entity.Code, &entity.CreatedBy, &entity.UpdatedBy, &entity.CreatedAt, &entity.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
	r.fillEntityLabels(&entity)
	if err := recordAuditTx(ctx, tx, actor, resourceType, entity.ID, "update", nil); err != nil {
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

	snapshot, err := snapshotTx(ctx, tx, table, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	key := resourceType + ":" + id
	deletedID, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into deleted_records (id, resource_type, resource_id, resource_key, snapshot, deleted_by)
values ($1, $2, $3, $4, $5, $6)
`, deletedID, resourceType, id, key, snapshot, actor)
	if err != nil {
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

	if err := recordAuditTx(ctx, tx, actor, resourceType, id, "delete", snapshot); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) createEntityTx(ctx context.Context, tx *sql.Tx, table, parentColumn, parentID, code, name, comment, actor string) (Entity, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return Entity{}, err
	}
	var entity Entity
	if parentColumn == "" {
		query := fmt.Sprintf("insert into %s (id, code, name, comment, created_by, updated_by) values ($1, $2, $3, $4, $5, $5) returning id, code, name, comment, created_by, updated_by, created_at, updated_at", table)
		err := tx.QueryRowContext(ctx, query, id, code, name, comment, actor).Scan(
			&entity.ID, &entity.Code, &entity.Name, &entity.Comment, &entity.CreatedBy, &entity.UpdatedBy, &entity.CreatedAt, &entity.UpdatedAt,
		)
		if err != nil {
			return Entity{}, err
		}
		r.fillEntityLabels(&entity)
		return entity, err
	}

	query := fmt.Sprintf("insert into %s (id, %s, code, name, comment, created_by, updated_by) values ($1, $2, $3, $4, $5, $6, $6) returning id, code, name, comment, created_by, updated_by, created_at, updated_at", table, parentColumn)
	err = tx.QueryRowContext(ctx, query, id, parentID, code, name, comment, actor).Scan(
		&entity.ID, &entity.Code, &entity.Name, &entity.Comment, &entity.CreatedBy, &entity.UpdatedBy, &entity.CreatedAt, &entity.UpdatedAt,
	)
	if err != nil {
		return Entity{}, err
	}
	r.fillEntityLabels(&entity)
	return entity, err
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

func recordAuditTx(ctx context.Context, tx *sql.Tx, actor, resourceType, resourceID, action string, encryptedValue []byte) error {
	auditID, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
insert into audit_records (id, actor, resource_type, resource_id, action, encrypted_value)
values ($1, $2, $3, $4, $5, $6)
`, auditID, actor, resourceType, resourceID, action, encryptedValue)
	return err
}

func snapshotTx(ctx context.Context, tx *sql.Tx, table, id string) ([]byte, error) {
	query := fmt.Sprintf("select to_jsonb(t) from %s t where id = $1 and is_deleted = false", table)
	var snapshot []byte
	err := tx.QueryRowContext(ctx, query, id).Scan(&snapshot)
	return snapshot, err
}

func parentColumn(table string) string {
	switch table {
	case "projects":
		return "org_id"
	case "environments":
		return "org_id"
	case "folders":
		return "environment_id"
	default:
		return ""
	}
}
