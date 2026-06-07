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

// EnvRef 描述"folder 出现在哪个 env,以及该 env 下同名 folder 的 id"。
//
// 用在 FolderGroup.EnvList / SubFolderGroup.EnvList:同一个 folder code
// 跨多个 env 时,前端要的不是 envId 列表(还要再查一次才能拿到该 env 下
// folder id),而是"env 元信息 + 该 env 下当前 folder 实例的 id"三元组。
//
//   - Id       env 的 uuid
//   - Code     env 的 code(dev / test / sim / prod / ...)
//   - FolderId 当前 env 下、与所在 group.code 同名的 folder 的 uuid
type EnvRef struct {
	Id       string `json:"id"`
	Code     string `json:"code"`
	FolderId string `json:"folderId"`
}

// FolderInProject 是按 project 聚合查询的"扁平行"——DB 返回一行 = 一个 folder,
// 包含 level=1/2 所需的最小字段集,service 层再按 code 聚合为 FolderGroup。
//
// 不复用 FolderTreeEntry 的原因:FolderTreeEntry 是为 tree service 设计的,
// 字段(code/name/comment + level + environmentId + parentId + projectId)刚好对齐;
// 本结构也用这套字段,只是语义聚焦"按 project 列出来再聚合"。
//
// EnvironmentCode 由 repo JOIN environments 填,仅用于 EnvRef 的 Code 字段,
// 不会出现在 group.id / group.name / 等其他 group 字段。
type FolderInProject struct {
	Id              string `json:"id"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Comment         string `json:"comment"`
	Level           int    `json:"level"`
	EnvironmentId   string `json:"environmentId"`
	EnvironmentCode string `json:"environmentCode"`
	ParentId        string `json:"parentId,omitempty"` // level=2 时填父 folder id
	ProjectId       string `json:"projectId"`
}

// ProjectFolderRequest 是 POST /api/v1/folder/listByProject 的请求体。
// projectId 必填,前端先调 /project/list 取到 id,再列其下所有 folder 结构。
type ProjectFolderRequest struct {
	ProjectId string `json:"projectId"`
}

// SubFolderGroup 是 FolderGroup 下属的子 folder 聚合节点(level=2,没有 subFolders)。
// 与 FolderGroup 解耦(不让子层继承父层的 SubFolders 字段)以避免误导前端递归。
type SubFolderGroup struct {
	Id      string   `json:"id"`
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Comment string   `json:"comment,omitempty"`
	EnvList []EnvRef `json:"envList"`
}

// FolderGroup 是按 project 聚合的 level=1 folder 节点:同一 code 跨多个 env 时
// 合并为一组,envList 记录该 code 在哪些 env 下存在(含该 env 下 folder id,
// 免去前端再走一次 /folder/list 反查);subFolders 是该 code 下属的 level=2
// folder(同样按 code 聚合,每组带自己的 EnvList 数组)。
//
// Id / Name / Comment:从该 code 在第一个 env 中的实例取(典型场景下同名 folder
// 的 name/comment 一致;不一致时以第一个为准,前端展示时如需精确可按 env 拉详情)。
type FolderGroup struct {
	Id         string           `json:"id"`
	Code       string           `json:"code"`
	Name       string           `json:"name"`
	Comment    string           `json:"comment,omitempty"`
	EnvList    []EnvRef         `json:"envList"`
	SubFolders []SubFolderGroup `json:"subFolders"`
}

// ProjectFolderTree 是 POST /api/v1/folder/listByProject 的响应根。
// 当前实现是按 level=1 code 聚合的"扁平"视图(1 层 + 子层),不递归到 level=3+。
type ProjectFolderTree struct {
	FolderList []FolderGroup `json:"folderList"`
}
