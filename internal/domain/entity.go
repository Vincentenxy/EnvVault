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
	SortOrder      int       `json:"sortOrder,omitempty"`
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
	Code      string
	Name      string
	Comment   string
	SortOrder int
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
//
// Values 是 v11 search 接口的"跨 env 明文聚合"字段,key 是 envCode:
//
//   - project 维度:Values 是 multi-entry map(同 (folder,key) 跨多 env 的值聚合),
//     形如 {"dev": "...", "test": "...", "sim": "...", "prod": "..."};
//   - env/folder 维度:Values 是 single-entry map(只有当前 envCode 一个 key)。
//
// 填充规则:service 层对每条 secret 做 secret:reveal 权限判定,有权限则解密填值,
// 无权限(或不存在的 env)则填空串。前端可统一用 values[envCode] 访问。
type Secret struct {
	Id              string            `json:"id"`
	OrgId           string            `json:"orgId"`
	OrgCode         string            `json:"orgCode"`
	ProjectId       string            `json:"projectId"`
	ProjectCode     string            `json:"projectCode"`
	EnvironmentId   string            `json:"environmentId"`
	EnvironmentCode string            `json:"environmentCode"`
	FolderId        string            `json:"folderId"`
	FolderCode      string            `json:"folderCode"`
	Key             string            `json:"key"`
	Path            string            `json:"path"`
	Value           string            `json:"value,omitempty"`
	Values          map[string]string `json:"values,omitempty"`
	Comment         string            `json:"comment,omitempty"`
	Version         int               `json:"version"`
	CreatedBy       string            `json:"createdBy"`
	CreatedByLabel  string            `json:"createdByLabel"`
	UpdatedBy       string            `json:"updatedBy"`
	UpdatedByLabel  string            `json:"updatedByLabel"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

// SecretCacheRecord is the tuple written into the Redis secret cache
// (metadata + raw ciphertext, never plaintext).
type SecretCacheRecord struct {
	Secret          Secret
	ValueCiphertext json.RawMessage
}

// SecretGroup is the response shape for SearchSecrets under the project
// scope (filter.ProjectId != ""). One SecretGroup corresponds to one
// (folder, key) pair, and the Envs map carries one Secret per environment
// code that defines that key.
//
// Custom MarshalJSON flattens Envs into the top-level object so the wire
// shape is:
//
//	{ "key": "<secret key>", "dev": {<Secret>}, "test": {<Secret>}, ... }
//
// where each envCode key maps to the full Secret metadata of that env's
// row. The flat shape is what the frontend wants for "the same key in
// every env" views; the Go-side struct stays typed so we don't lose
// compile-time guarantees on field names.
//
// 顶层 "key" 字段名刻意与数据库 secrets.key 对齐,前端可以无歧义地直接对应
// 到表列;不用 "code" 命名是为了与 Secret 内的 folderCode / projectCode 等
// 区分("key" 在 secret 语境下就是 secret key,无歧义)。
//
// Why custom MarshalJSON: encoding/json cannot emit dynamic top-level
// keys (Envs is map[string]Secret, not a struct field). Implementing
// MarshalJSON is the idiomatic Go way to get a typed struct + dynamic
// wire keys. The env code is the only thing that varies per row, and
// the Secret payload itself is fully typed via the standard JSON tags
// on Secret.
//
// env/folder scope of SearchSecrets does NOT use this type — that path
// returns []Secret directly with a single-entry Values map.
type SecretGroup struct {
	Key  string            // secret key, surfaces as the top-level "key" field
	Envs map[string]Secret // envCode -> full Secret
}

// MarshalJSON implements custom flattening for SecretGroup.
//
// Output order is intentionally not guaranteed: the resulting map is
// re-built on every call and Go's map iteration is randomized. The
// frontend reads individual keys by env code, so order does not matter.
//
// Error path: json.Marshal of map[string]any can only fail if one of
// the inner Secret values contains a non-serializable value (e.g. a
// func, channel, or uncomparable type). Secret only has string / time /
// map fields, all of which marshal cleanly, so this is a defensive
// passthrough.
func (g SecretGroup) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(g.Envs)+1)
	out["key"] = g.Key
	for envCode, secret := range g.Envs {
		out[envCode] = secret
	}
	return json.Marshal(out)
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
// OrgId 不在此处:"按 org 列全部 secret" 等同于安全事件级别的危险操作,
// 不应该通过一个普通 list 接口暴露;由专门的"审计导出"接口另说。
//
// ProjectId 允许作为"仅按 project 收窄"的兜底粒度:当 caller 不指 folder
// 也不指 env、但又不想全量扫时,projectId 就是最粗的合法 scope。
// List / Search 内部的优先级(folder > env > project)由 service 层负责收敛,
// 这里只承载原始字段,不做"忽略谁"这种语义判断。
type ListFilter struct {
	EnvironmentId string
	FolderId      string
	ProjectId     string
	Keyword       string
}
