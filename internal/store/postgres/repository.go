package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	uuidgen "envVault/internal/id"
)

var ErrNotFound = errors.New("record not found")

type Repository struct {
	db *sql.DB
}

type Entity struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Comment   string    `json:"comment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Secret struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID string    `json:"environment_id"`
	FolderID      string    `json:"folder_id"`
	Key           string    `json:"key"`
	Value         string    `json:"value,omitempty"`
	Comment       string    `json:"comment,omitempty"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
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
	ResourceType   string          `json:"resource_type"`
	ResourceID     string          `json:"resource_id"`
	Action         string          `json:"action"`
	EncryptedValue json.RawMessage `json:"encrypted_value,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type ListFilter struct {
	OrgID         string
	ProjectID     string
	EnvironmentID string
	FolderID      string
	Keyword       string
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateOrganization(ctx context.Context, name, comment, actor string) (Entity, error) {
	return r.createEntity(ctx, "organizations", "", "", name, comment, actor, "organization")
}

func (r *Repository) ListOrganizations(ctx context.Context) ([]Entity, error) {
	return r.listEntities(ctx, "organizations", "")
}

func (r *Repository) GetOrganization(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "organizations", id)
}

func (r *Repository) UpdateOrganization(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "organizations", id, name, comment, actor, "organization")
}

func (r *Repository) DeleteOrganization(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "organizations", id, actor, "organization")
}

func (r *Repository) CreateProject(ctx context.Context, orgID, name, comment, actor string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	project, err := createEntityTx(ctx, tx, "projects", "org_id", orgID, name, comment)
	if err != nil {
		return Entity{}, err
	}
	for _, envName := range []string{"dev", "test", "sim", "prod"} {
		env, err := createEntityTx(ctx, tx, "environments", "project_id", project.ID, envName, "")
		if err != nil {
			return Entity{}, err
		}
		for _, folderName := range []string{"globals", "groups-secrets"} {
			if _, err := createEntityTx(ctx, tx, "folders", "environment_id", env.ID, folderName, ""); err != nil {
				return Entity{}, err
			}
		}
	}
	if err := recordAuditTx(ctx, tx, actor, "project", project.ID, "create", nil); err != nil {
		return Entity{}, err
	}
	return project, tx.Commit()
}

func (r *Repository) ListProjects(ctx context.Context, orgID string) ([]Entity, error) {
	return r.listEntities(ctx, "projects", orgID)
}

func (r *Repository) GetProject(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "projects", id)
}

func (r *Repository) UpdateProject(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "projects", id, name, comment, actor, "project")
}

func (r *Repository) DeleteProject(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "projects", id, actor, "project")
}

func (r *Repository) CreateEnvironment(ctx context.Context, projectID, name, comment, actor string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	env, err := createEntityTx(ctx, tx, "environments", "project_id", projectID, name, comment)
	if err != nil {
		return Entity{}, err
	}
	for _, folderName := range []string{"globals", "groups-secrets"} {
		if _, err := createEntityTx(ctx, tx, "folders", "environment_id", env.ID, folderName, ""); err != nil {
			return Entity{}, err
		}
	}
	if err := recordAuditTx(ctx, tx, actor, "environment", env.ID, "create", nil); err != nil {
		return Entity{}, err
	}
	return env, tx.Commit()
}

func (r *Repository) ListEnvironments(ctx context.Context, projectID string) ([]Entity, error) {
	return r.listEntities(ctx, "environments", projectID)
}

func (r *Repository) GetEnvironment(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "environments", id)
}

func (r *Repository) UpdateEnvironment(ctx context.Context, id, name, comment, actor string) (Entity, error) {
	return r.updateEntity(ctx, "environments", id, name, comment, actor, "environment")
}

func (r *Repository) DeleteEnvironment(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "environments", id, actor, "environment")
}

func (r *Repository) CreateFolder(ctx context.Context, environmentID, name, comment, actor string) (Entity, error) {
	return r.createEntity(ctx, "folders", "environment_id", environmentID, name, comment, actor, "folder")
}

func (r *Repository) ListFolders(ctx context.Context, environmentID string) ([]Entity, error) {
	return r.listEntities(ctx, "folders", environmentID)
}

func (r *Repository) GetFolder(ctx context.Context, id string) (Entity, error) {
	return r.getEntity(ctx, "folders", id)
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
insert into secrets (id, folder_id, key, value_ciphertext, comment, version)
values ($1, $2, $3, $4, $5, 1)
returning id, folder_id, key, comment, version, created_at, updated_at
`, id, folderID, key, string(payload), comment).Scan(
		&secret.ID, &secret.FolderID, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if err != nil {
		return Secret{}, err
	}
	if err := recordAuditTx(ctx, tx, actor, "secret", secret.ID, "create", payload); err != nil {
		return Secret{}, err
	}
	return secret, tx.Commit()
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
set key = $2, value_ciphertext = $3, comment = $4, version = version + 1, updated_at = now()
where id = $1 and is_deleted = false
returning id, folder_id, key, comment, version, created_at, updated_at
`, id, key, string(payload), comment).Scan(
		&secret.ID, &secret.FolderID, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, err
	}
	if err := recordAuditTx(ctx, tx, actor, "secret", secret.ID, "update", payload); err != nil {
		return Secret{}, err
	}
	return secret, tx.Commit()
}

func (r *Repository) GetSecret(ctx context.Context, id string) (Secret, error) {
	var secret Secret
	err := r.db.QueryRowContext(ctx, `
select s.id, o.id, p.id, e.id, s.folder_id, s.key, s.comment, s.version, s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.id = $1 and s.is_deleted = false
`, id).Scan(
		&secret.ID, &secret.OrgID, &secret.ProjectID, &secret.EnvironmentID, &secret.FolderID, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	return secret, err
}

func (r *Repository) ListSecrets(ctx context.Context, filter ListFilter) ([]Secret, error) {
	rows, err := r.db.QueryContext(ctx, `
select s.id, o.id, p.id, e.id, s.folder_id, s.key, s.comment, s.version, s.created_at, s.updated_at
from secrets s
join folders f on f.id = s.folder_id
join environments e on e.id = f.environment_id
join projects p on p.id = e.project_id
join organizations o on o.id = p.org_id
where s.is_deleted = false
  and ($1 = '' or o.id = $1::uuid)
  and ($2 = '' or p.id = $2::uuid)
  and ($3 = '' or e.id = $3::uuid)
  and ($4 = '' or s.folder_id = $4::uuid)
  and ($5 = '' or s.key ilike '%' || $5 || '%')
order by s.key asc
`, filter.OrgID, filter.ProjectID, filter.EnvironmentID, filter.FolderID, filter.Keyword)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Secret
	for rows.Next() {
		var secret Secret
		if err := rows.Scan(&secret.ID, &secret.OrgID, &secret.ProjectID, &secret.EnvironmentID, &secret.FolderID, &secret.Key, &secret.Comment, &secret.Version, &secret.CreatedAt, &secret.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, secret)
	}
	return items, rows.Err()
}

func (r *Repository) ListSecretCacheRecords(ctx context.Context) ([]SecretCacheRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
select s.id, o.id, p.id, e.id, s.folder_id, s.key, s.value_ciphertext, s.comment, s.version, s.created_at, s.updated_at
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
			&record.Secret.ID,
			&record.Secret.OrgID,
			&record.Secret.ProjectID,
			&record.Secret.EnvironmentID,
			&record.Secret.FolderID,
			&record.Secret.Key,
			&record.ValueCiphertext,
			&record.Secret.Comment,
			&record.Secret.Version,
			&record.Secret.CreatedAt,
			&record.Secret.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, record)
	}
	return items, rows.Err()
}

func (r *Repository) DeleteSecret(ctx context.Context, id, actor string) error {
	return r.deleteEntity(ctx, "secrets", id, actor, "secret")
}

func (r *Repository) ListAuditRecords(ctx context.Context, resourceType, resourceID string) ([]AuditRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
select id, actor, resource_type, resource_id, action, coalesce(encrypted_value, 'null'::jsonb), created_at
from audit_records
where ($1 = '' or resource_type = $1)
  and ($2 = '' or resource_id = $2::uuid)
order by created_at desc
`, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AuditRecord
	for rows.Next() {
		var item AuditRecord
		if err := rows.Scan(&item.ID, &item.Actor, &item.ResourceType, &item.ResourceID, &item.Action, &item.EncryptedValue, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) createEntity(ctx context.Context, table, parentColumn, parentID, name, comment, actor, resourceType string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	entity, err := createEntityTx(ctx, tx, table, parentColumn, parentID, name, comment)
	if err != nil {
		return Entity{}, err
	}
	if err := recordAuditTx(ctx, tx, actor, resourceType, entity.ID, "create", nil); err != nil {
		return Entity{}, err
	}
	return entity, tx.Commit()
}

func (r *Repository) listEntities(ctx context.Context, table, parentID string) ([]Entity, error) {
	query := fmt.Sprintf("select id, name, comment, created_at, updated_at from %s where is_deleted = false", table)
	args := []any{}
	if parentID != "" {
		query += fmt.Sprintf(" and %s = $1::uuid", parentColumn(table))
		args = append(args, parentID)
	}
	query += " order by name asc"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(&entity.ID, &entity.Name, &entity.Comment, &entity.CreatedAt, &entity.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, entity)
	}
	return items, rows.Err()
}

func (r *Repository) getEntity(ctx context.Context, table, id string) (Entity, error) {
	var entity Entity
	query := fmt.Sprintf("select id, name, comment, created_at, updated_at from %s where id = $1 and is_deleted = false", table)
	err := r.db.QueryRowContext(ctx, query, id).Scan(&entity.ID, &entity.Name, &entity.Comment, &entity.CreatedAt, &entity.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	return entity, err
}

func (r *Repository) updateEntity(ctx context.Context, table, id, name, comment, actor, resourceType string) (Entity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entity{}, err
	}
	defer tx.Rollback()

	var entity Entity
	query := fmt.Sprintf("update %s set name = $2, comment = $3, updated_at = now() where id = $1 and is_deleted = false returning id, name, comment, created_at, updated_at", table)
	err = tx.QueryRowContext(ctx, query, id, name, comment).Scan(&entity.ID, &entity.Name, &entity.Comment, &entity.CreatedAt, &entity.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ErrNotFound
	}
	if err != nil {
		return Entity{}, err
	}
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

	query := fmt.Sprintf("update %s set is_deleted = true, deleted_at = now(), deleted_by = $2, updated_at = now() where id = $1 and is_deleted = false", table)
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

func createEntityTx(ctx context.Context, tx *sql.Tx, table, parentColumn, parentID, name, comment string) (Entity, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return Entity{}, err
	}
	var entity Entity
	if parentColumn == "" {
		query := fmt.Sprintf("insert into %s (id, name, comment) values ($1, $2, $3) returning id, name, comment, created_at, updated_at", table)
		err := tx.QueryRowContext(ctx, query, id, name, comment).Scan(&entity.ID, &entity.Name, &entity.Comment, &entity.CreatedAt, &entity.UpdatedAt)
		return entity, err
	}

	query := fmt.Sprintf("insert into %s (id, %s, name, comment) values ($1, $2, $3, $4) returning id, name, comment, created_at, updated_at", table, parentColumn)
	err = tx.QueryRowContext(ctx, query, id, parentID, name, comment).Scan(&entity.ID, &entity.Name, &entity.Comment, &entity.CreatedAt, &entity.UpdatedAt)
	return entity, err
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
		return "project_id"
	case "folders":
		return "environment_id"
	default:
		return ""
	}
}
