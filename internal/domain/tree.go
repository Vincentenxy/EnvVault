package domain

import "time"

// TreeNodeType 标识树节点的资源类别,避免前端靠 id 前缀去猜。
type TreeNodeType string

const (
	TreeNodeOrganization TreeNodeType = "organization"
	TreeNodeProject      TreeNodeType = "project"
	TreeNodeEnvironment  TreeNodeType = "environment"
	TreeNodeFolder       TreeNodeType = "folder"
)

// TreeNode 是树形接口的 DTO,故意剥离审计字段(createdBy/updatedBy/at 等),
// 这些留给详情接口。前端用 Type/Id 即可定位资源,不需要 createdByLabel。
//
// Children 始终输出为 []TreeNode(非 nil),前端可以省去 children?.length 的
// 判空写法,直接 children.map(...) 即可。
type TreeNode struct {
	Id       string       `json:"id"`
	Type     TreeNodeType `json:"type"`
	Level    int          `json:"level,omitempty"`    // 仅 folder:1 或 2
	ParentId string       `json:"parentId,omitempty"` // 顶层 organization 无父
	Code     string       `json:"code"`
	Name     string       `json:"name"`
	Comment  string       `json:"comment,omitempty"`
	Children []TreeNode   `json:"children"`
}

// TreeStats 反映本次组装的统计信息,供前端做提示 / 审计。
//
//   - Organizations/Projects/Environments/Folders:caller 可见 + 满足 depth 的节点数
//   - Dropped:被 RBAC 收窄丢掉的节点数(含级联丢弃)
//   - Orphans:父不可见但子可见的孤立节点数(包含默认挂到虚拟根的)
type TreeStats struct {
	Organizations int `json:"organizations"`
	Projects      int `json:"projects"`
	Environments  int `json:"environments"`
	Folders       int `json:"folders"`
	Dropped       int `json:"dropped"`
	Orphans       int `json:"orphans"`
}

// ResourceTree 是 GetTree 接口的响应体。
//
// Source 字段标识本棵树是 cache 拼出来的还是 database fallback 拼出来的,便于
// debug 与性能观测。前端可忽略。
type ResourceTree struct {
	Roots       []TreeNode `json:"roots"`
	Stats       TreeStats  `json:"stats"`
	GeneratedAt time.Time  `json:"generatedAt"`
	Source      string     `json:"source"`
}

// TreeRequest 是 GetTree 接口的请求体。MaxDepth 控制返回的层级数:
//
//	1 = 仅 organization
//	2 = + project
//	3 = + environment
//	4 = + folder(包含 level=1 与 level=2)
//
// 默认在 service 层做归一化(maxDepth 越界或 0 → 4)。
type TreeRequest struct {
	IncludeOrphans bool `json:"includeOrphans"`
	MaxDepth       int  `json:"maxDepth"`
}

// CascadeScope 是级联删除后 Repo 返回的"受影响的子资源 id 集合",
// 供 handler 调 cache 失效时使用。DeleteOrganization 会级联填齐 4 类;
// DeleteProject 只填 EnvironmentIds + FolderIds + SecretIds;
// DeleteEnvironment 只填 FolderIds + SecretIds;DeleteFolder 只填 SecretIds。
type CascadeScope struct {
	OrganizationId string
	ProjectIds     []string
	EnvironmentIds []string
	FolderIds      []string
	SecretIds      []string
}

// Empty 判断级联范围是否完全无下游(单独删 1 个 org/project/env/folder 时,
// 该方法用于让 handler 决定是否走 cache 遍历)。
func (c CascadeScope) Empty() bool {
	return len(c.ProjectIds) == 0 &&
		len(c.EnvironmentIds) == 0 &&
		len(c.FolderIds) == 0 &&
		len(c.SecretIds) == 0
}

// FolderTreeEntry 是 tree 组装场景下的 folder 元数据,比 Entity 多了 4 个字段:
//
//   - Level:1=顶层(父是 env),2=子层(父是 level=1 folder)
//   - EnvironmentId:不论 level,本字段恒等于所属 env 的 id(沿用 schema 的反范式列)
//   - ParentId:仅 level=2 时填父 folder id;level=1 时为空(Entity.ParentId 不可用,
//     因为它对 level=1 而言语义是"env id",但对 level=2 而言是"父 folder id",会冲突)
//   - ProjectId:反范式,Level=1/2 都需要,group by 树时方便定位根
//
// 这个类型对齐 internal/store/redis/cache.go:UpsertFolder 的形参 list,
// 保持 cache hash 字段与 DB 行的字段集合一致。
type FolderTreeEntry struct {
	Entity
	Level         int    `json:"level"`
	EnvironmentId string `json:"environmentId"`
	ParentId      string `json:"parentId,omitempty"`
	ProjectId     string `json:"projectId"`
}
