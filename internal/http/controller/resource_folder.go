package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	rediscache "envVault/internal/store/redis"
)

// createFolderRequest 显式表达 level 与 parent 关系:
//
//   - level=1: parentId = environmentId(env 是父,顶层 folder 唯一可挂靠字段)
//   - level=2: parentId = parent folder id(父 folder 是父;env 由后端从父 folder 反查)
//
// 故意不暴露 environmentId 字段:env 必然等于父 folder 的 env(顶层时等于 parentId),
// 强制让客户端再传一遍是冗余字段、容易出现传错的不一致。
//
// 注意:folders 表 schema 同时持有 environment_id 和 parent_id 两个字段,
// 看似冗余,实际语义不重叠(详见 configs/schema.sql 与 CreateFolder 注释):
//   - environment_id 答"属于哪个 env"(level=1 必填,level=2 也保留做 O(1) 查询)
//   - parent_id      答"父 folder 是谁"(仅 level=2 填,level=1 必为 NULL)
type createFolderRequest struct {
	ParentId string `json:"parentId,omitempty"`
	Level    int    `json:"level"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Comment  string `json:"comment"`
}

func (ctrl *Controller) CreateFolder(c *gin.Context) {
	var req createFolderRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	if req.Level != 1 && req.Level != 2 {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "level must be 1 or 2")
		return
	}

	parentId := strings.TrimSpace(req.ParentId)
	envId := ""

	switch req.Level {
	case 1:
		// 顶层 folder:parentId 字段就是 env id
		if parentId == "" {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "level=1 requires parentId (environmentId)")
			return
		}
		envId = parentId
	case 2:
		// 二级 folder:parentId 字段是父 folder id;env 由后端从父 folder 反查
		if parentId == "" {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "level=2 requires parentId (parent folder id)")
			return
		}
		envOfParent, err := ctrl.repo.GetFolderEnvId(c.Request.Context(), parentId)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		envId = envOfParent
	}

	if !ctrl.allowScope(c, "folder:create", "environment", envId) {
		return
	}
	ctrl.log(c, "CreateFolder", logging.F("level", req.Level), logging.F("parent_id", parentId), logging.F("environment_id", envId), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.repo.CreateFolder(c.Request.Context(), envId, parentId, req.Code, req.Name, req.Comment, ctrl.actor(c), req.Level)
	if err == nil {
		// folder cache 写需要 projectId/parentId/level(除 envId 外) 3 个额外字段,
		// Entity 不携带,因此 CreateFolder 之后再走一次 GetFolderContext 拿全量上下文。
		// 失败仅 warn 不抛。
		_, projectId, parentFolderId, level, ctxErr := ctrl.repo.GetFolderContext(c.Request.Context(), item.Id)
		if ctxErr == nil {
			ctrl.cacheUpsert(c, func(rc *rediscache.Cache) error {
				return rc.UpsertFolder(c.Request.Context(), item, envId, projectId, parentFolderId, level)
			})
		}
	}
	ctrl.write(c, item, err)
}

// folderListRequest 在 listRequest 基础上加 includeSubfolders 开关,触发后响应
// 元素从 Entity 扩展为 folderWithSubfolders(Entity + subfolders 子数组)。
// 只在 environmentId 模式(level=1 父列表)下生效;folderParentId 模式 level=2
// 列表不支持,传了 true 直接 400(校验在 validateListFoldersIncludeSubfolders)。
type folderListRequest struct {
	PageRequest
	OrgId             string `json:"orgId,omitempty"`
	ProjectId         string `json:"projectId,omitempty"`
	EnvironmentId     string `json:"environmentId,omitempty"`
	FolderId          string `json:"folderId,omitempty"`
	FolderParentId    string `json:"folderParentId,omitempty"`
	ResourceType      string `json:"resourceType,omitempty"`
	ResourceId        string `json:"resourceId,omitempty"`
	Keyword           string `json:"keyword,omitempty"`
	IncludeSubfolders bool   `json:"includeSubfolders,omitempty"`
}

// folderWithSubfolders 是 ListFolders 在 includeSubfolders=true 时的响应元素。
// 匿名嵌入 Entity 让 Go 的 json.Marshal 把 Entity 字段(id/parentId/code/name/...)
// 铺平到顶层,再追加 subfolders 数组;与 tree.get 的 TreeNode.children 同款约定。
// 无子 folder 时 subfolders 为 []Entity{}(非 null),handler 兜底赋值。
type folderWithSubfolders struct {
	Entity
	Subfolders []Entity `json:"subfolders"`
}

// ListFolders v7 起不再走 allowScope 入口;repo SQL 按 caller.UserId 自动收窄可见 folder。
// envId/parentId 的「父 folder 反查 env」逻辑保留(GetFolderEnvId 仍然需要),
// 因为 level=2 list 时父 folder.id 是路径定位的关键,不是 authz。
//
// v10 起支持 includeSubfolders=true:仅 environmentId 模式生效,响应 list 元素
// 升级为 folderWithSubfolders,一次性返回两级目录;否则行为与历史完全一致。
func (ctrl *Controller) ListFolders(c *gin.Context) {
	var req folderListRequest
	if !ctrl.bind(c, &req) {
		return
	}
	// 把 listRequest 透传到共用 validator(同字段同校验)。
	if !validateListFolders(c, listRequest{
		PageRequest:    req.PageRequest,
		OrgId:          req.OrgId,
		ProjectId:      req.ProjectId,
		EnvironmentId:  req.EnvironmentId,
		FolderId:       req.FolderId,
		FolderParentId: req.FolderParentId,
		ResourceType:   req.ResourceType,
		ResourceId:     req.ResourceId,
		Keyword:        req.Keyword,
	}) {
		return
	}
	if !validateListFoldersIncludeSubfolders(c, req) {
		return
	}

	envId := strings.TrimSpace(req.EnvironmentId)
	parentId := strings.TrimSpace(req.FolderParentId)

	// 两种模式:
	//   - environmentId 非空:列 env 下所有 level=1 (parent_id IS NULL) folder
	//   - folderParentId 非空:列该父 folder 下所有 level=2 folder;env 由后端从父 folder 反查
	// (反查是路径定位,不是 authz)
	if parentId != "" {
		if _, err := ctrl.repo.GetFolderEnvId(c.Request.Context(), parentId); err != nil {
			ctrl.write(c, nil, err)
			return
		}
	}

	ctrl.log(c, "ListFolders", logging.F("environment_id", envId), logging.F("folder_parent_id", parentId), logging.F("include_subfolders", req.IncludeSubfolders))
	pagination := paginationFromRequest(req.PageRequest)
	userId := auth.UserFromContext(c).UserId
	result, err := ctrl.repo.ListFolders(c.Request.Context(), userId, envId, parentId, pagination)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}

	// 默认(false)路径:完全等同历史响应,list 元素是 Entity,不带 subfolders 字段。
	if !req.IncludeSubfolders {
		ctrl.write(c, pageData(result.Items, result.Total, pagination), nil)
		return
	}

	// includeSubfolders=true 路径:拉所有子 folder,组装嵌套响应。
	parentIds := make([]string, 0, len(result.Items))
	for _, it := range result.Items {
		parentIds = append(parentIds, it.Id)
	}
	children, err := ctrl.repo.ListFolderChildren(c.Request.Context(), userId, parentIds)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}

	list := make([]folderWithSubfolders, 0, len(result.Items))
	for _, it := range result.Items {
		// ListFolderChildren 保证 map 非 nil,但 value 可能为 nil(无子 folder),
		// 兜底为 []Entity{} 让 JSON 出 [] 而非 null。
		subs := children[it.Id]
		if subs == nil {
			subs = []Entity{}
		}
		list = append(list, folderWithSubfolders{Entity: it, Subfolders: subs})
	}
	ctrl.write(c, pageData(list, result.Total, pagination), nil)
}

func (ctrl *Controller) GetFolder(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "folder") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	var item Entity
	var err error
	if useCode {
		ctrl.log(c, "GetFolder", logging.F("environment_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.repo.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = item.Id
	} else {
		ctrl.log(c, "GetFolder", logging.F("id", req.Id))
		item, err = ctrl.repo.GetFolder(c.Request.Context(), rid)
	}
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if !ctrl.allowScope(c, "folder:read", "folder", rid) {
		return
	}
	ctrl.write(c, item, nil)
}

func (ctrl *Controller) UpdateFolder(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "folder") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		ctrl.log(c, "UpdateFolder", logging.F("environment_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
		folder, err := ctrl.repo.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = folder.Id
	} else {
		ctrl.log(c, "UpdateFolder", logging.F("id", req.Id), logging.F("name", req.Name))
	}
	if !ctrl.allowScope(c, "folder:update", "folder", rid) {
		return
	}
	item, err := ctrl.repo.UpdateFolder(c.Request.Context(), rid, req.Name, req.Comment, ctrl.actor(c))
	if err == nil {
		envId, projectId, parentFolderId, level, ctxErr := ctrl.repo.GetFolderContext(c.Request.Context(), rid)
		if ctxErr == nil {
			ctrl.cacheUpsert(c, func(rc *rediscache.Cache) error {
				return rc.UpsertFolder(c.Request.Context(), item, envId, projectId, parentFolderId, level)
			})
		}
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteFolder(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "folder") {
		return
	}
	ctrl.log(c, "DeleteFolder")
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		folder, err := ctrl.repo.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = folder.Id
	}
	if !ctrl.allowScope(c, "folder:delete", "folder", rid) {
		return
	}
	scope, err := ctrl.repo.DeleteFolder(c.Request.Context(), rid, ctrl.actor(c))
	if err == nil {
		ctrl.cacheInvalidateCascade(c, scope)
	}
	ctrl.write(c, gin.H{"deleted": true}, err)
}
