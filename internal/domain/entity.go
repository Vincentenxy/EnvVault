// Package domain holds the business entities and value objects shared by
// every other layer (service, repository, handler). It must not import any
// infrastructure package (database driver, http framework, etc.).
package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by repositories when a row does not exist or has
// been soft-deleted. Layers above use errors.Is to map it to 404 responses.
var ErrNotFound = errors.New("record not found")

// ErrConflict is returned by repositories when a write would violate a
// uniqueness or referential constraint (e.g. duplicate code within the
// same parent scope). Layers above use errors.Is to map it to 409 responses.
var ErrConflict = errors.New("resource already exists")

// Entity is the canonical shape for the four basic CRUD resources
// (organization, project, environment, folder). All share the same audit
// fields and lifecycle (code is immutable, name/comment editable).
//
// ParentId 字段多态,语义按 entity 类别变化:
//
//   - organization:空(顶层,无父)
//   - project:ParentId = org_id
//   - environment:ParentId = project_id
//   - folder level=1:ParentId = environment_id(env 是父)
//   - folder level=2:ParentId = parent folder id(父 folder 是父)
//
// 字段为顶层实体(organization)时为空,JSON 标签 omitempty 隐藏。
type Entity struct {
	Id             string    `json:"id"`
	ParentId       string    `json:"parentId,omitempty"`
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

// EnvSpec is the minimal input used to declare an environment when
// creating a project (or driving the env template upsert).
type EnvSpec struct {
	Code    string
	Name    string
	Comment string
}

// EnvironmentTemplate is the org-level read-only snapshot of an env code
// that has been instantiated at least once inside that org. Name and
// comment are frozen at the first write.
type EnvironmentTemplate struct {
	Id             string    `json:"id"`
	OrgId          string    `json:"orgId"`
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

// SecretCiphertext is the encrypted payload persisted in secrets.value_ciphertext.
type SecretCiphertext struct {
	Algorithm string `json:"algorithm"`
	Nonce     []byte `json:"nonce"`
	Data      []byte `json:"data"`
}

// Secret is the denormalised read model joined across
// organizations -> projects -> environments -> folders -> secrets, used
// by both the API and the Redis warm-cache loader.
type Secret struct {
	Id              string    `json:"id"`
	OrgId           string    `json:"orgId"`
	OrgCode         string    `json:"orgCode"`
	ProjectId       string    `json:"projectId"`
	ProjectCode     string    `json:"projectCode"`
	EnvironmentId   string    `json:"environmentId"`
	EnvironmentCode string    `json:"environmentCode"`
	FolderId        string    `json:"folderId"`
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

// SecretCacheRecord is the tuple written into the Redis secret cache
// (metadata + raw ciphertext, never plaintext).
type SecretCacheRecord struct {
	Secret          Secret
	ValueCiphertext json.RawMessage
}

// AuditRecord models audit_records. EncryptedValue is kept raw JSON
// because the audit log should survive even if the encryption key rotates.
type AuditRecord struct {
	Id             string          `json:"id"`
	Actor          string          `json:"actor"`
	ResourceType   string          `json:"resourceType"`
	ResourceId     string          `json:"resourceId"`
	Action         string          `json:"action"`
	EncryptedValue json.RawMessage `json:"encryptedValue,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// ListFilter 是 ListSecrets / SearchSecrets 的过滤条件。
//
// OrgId / ProjectId 不在此处:folder_id 已经是最细粒度的资源定位,
// env_id 是次细粒度。"按 org 列全部 secret" 等同于安全事件级别的危险操作,
// 不应该通过一个普通 list 接口暴露;由专门的"审计导出"接口另说。
type ListFilter struct {
	EnvironmentId string
	FolderId      string
	Keyword       string
}
