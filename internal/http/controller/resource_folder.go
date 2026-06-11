package controller

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	rediscache "envVault/internal/store/redis"
)

// createFolderRequest 表达「在指定 env 下批量创建 folder」的契约。
//
// 关键约定:
//   - 旧版 `parentId`(folder uuid)字段已移除 —— 改用 `parentCode`(父 folder 的 code),
//     让前端无需先查父 folder id 就能跨 env 复刻同一棵树。
//   - `envList` 必填且非空,每项是 **env 的 id(UUID)**,不是 env code。
//     后端按 envList 在每个 env 下各建一个 folder,整批 1 个事务。
//   - `level=1` 时 parentCode 不需要(顶层 folder 的父就是 env 本身)。
//   - `level=2` 时 parentCode 必填,后端在每个 env 下用 parentCode 找同 code 的
//     level=1 sibling parent folder,挂子 folder 于此。
//
// 响应形态:`data` 直接是 `[Entity, ...]`,按 envList 顺序(创建接口响应规范:
// 批量产生列表的 create 直接把列表放在 data 中,不再用 created/items 等中间字段)。
//
// 注意:folders 表 schema 同时持有 environment_id 和 parent_id 两个字段,
// 看似冗余,实际语义不重叠(详见 configs/schema.sql 与 CreateFolder 注释):
//   - environment_id 答"属于哪个 env"(level=1 必填,level=2 也保留做 O(1) 查询)
//   - parent_id      答"父 folder 是谁"(仅 level=2 填,level=1 必为 NULL)
type createFolderRequest struct {
	Level      int      `json:"level"`
	Code       string   `json:"code"`
	Name       string   `json:"name"`
	Comment    string   `json:"comment"`
	ParentCode string   `json:"parentCode,omitempty"`
	EnvList    []string `json:"envList"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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

	// envList 是必填字段。空 → 直接 -1(沿用 envVault 通用失败业务码,
	// 区别于「请求体格式不合法」的 1002 CodeInvalidRequest)。
	if len(req.EnvList) == 0 {
		response.FailWithMsg(c, "envList is required and cannot be empty")
		return
	}
	if errMsg := validateEnvIdsForCreate(req.EnvList); errMsg != "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, errMsg)
		return
	}

	// level=2 必须带 parentCode
	if req.Level == 2 && strings.TrimSpace(req.ParentCode) == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "parentCode is required for level=2")
		return
	}

	ctrl.log(c, "CreateFolder", logging.F("level", req.Level), logging.F("parent_code", req.ParentCode), logging.F("code", req.Code), logging.F("name", req.Name), logging.F("env_list_len", len(req.EnvList)))

	// 走批量路径(level=1 / level=2 都不再有 legacy 单条创建)
	ctrl.createFolderBatch(c, req)
}

// createFolderBatch 走 envList 批量路径,封装 level=1 / level=2 两种分支:
//   - level=1:无需 parentCode,直接按 envList 在每个 env 下创建顶层 folder
//   - level=2:parentCode 是参考父 folder 的 code,后端在每个 env 下找同名 sibling
//     parent folder,挂子 folder 于此
func (ctrl *Controller) createFolderBatch(c *gin.Context, req createFolderRequest) {
	ctx := c.Request.Context()
	actor := ctrl.actor(c)

	var created []Entity
	var err error
	if req.Level == 1 {
		created, err = ctrl.repo.CreateTopLevelFoldersInEnvs(ctx, req.Code, req.Name, req.Comment, actor, req.EnvList)
	} else {
		created, err = ctrl.repo.CreateFoldersAcrossEnvs(ctx, req.ParentCode, req.Code, req.Name, req.Comment, actor, req.EnvList)
	}
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	// 同步 cache:每个新建 folder 走 GetFolderContext 拿全量上下文后 Upsert。
	// 任一失败仅 warn,不抛(已在 DB 写入,cache 降级可接受)。
	for _, item := range created {
		envId, projectId, parentFolderId, level, ctxErr := ctrl.repo.GetFolderContext(ctx, item.Id)
		if ctxErr != nil {
			continue
		}
		ctrl.cacheUpsert(c, func(rc *rediscache.Cache) error {
			return rc.UpsertFolder(ctx, item, envId, projectId, parentFolderId, level)
		})
	}
	ctrl.write(c, created, nil)
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

// listFoldersByProjectRequest 是 POST /api/v1/folder/listByProject 的请求体。
// 关键区别于 folderListRequest:此接口按 project 维度一次性返回该 project 下
// 所有 folder(level=1 + level=2,按 code 聚合),响应里 folderList 元素带
// envList(列出每个 code 在哪些 env 下存在)。不与 folder/list 互斥,可共存。
type listFoldersByProjectRequest struct {
	ProjectId string `json:"projectId"`
}

// ListFoldersByProject 按 projectId 列出所有 folder + 子 folder(level=1 + level=2),
// level=1 按 code 聚合,每组带 envList;subFolders 跟随父层(level=2 同样按 code 聚合)。
//
// 关键契约:"子目录跟随父目录,当父目录有这个页面的时候子目录一定存在"——
// SQL 一次性拉同 project 下 level=1+2,service 层按 (code, parent_id) 聚合;
// 父 group 出现在结果里,它的所有 subFolders(由 RBAC narrowing 限制)都一定
// 存在;反向不保证——父不可见时,其子层被整体跳过。
func (ctrl *Controller) ListFoldersByProject(c *gin.Context) {
	var req listFoldersByProjectRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if strings.TrimSpace(req.ProjectId) == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "projectId is required")
		return
	}
	if !uuidPattern.MatchString(req.ProjectId) {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "projectId must be a UUID")
		return
	}
	if ctrl.tree == nil {
		logging.Error(c.Request.Context(), "ListFoldersByProject", "tree service is not configured")
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": response.CodeServiceUnavailable, "msg": "tree service is not configured", "data": nil})
		return
	}
	ctrl.log(c, "ListFoldersByProject", logging.F("project_id", req.ProjectId))
	user := auth.UserFromContext(c)
	tree, err := ctrl.tree.GetProjectFolderTree(c.Request.Context(), user, domain.ProjectFolderRequest{ProjectId: req.ProjectId})
	ctrl.write(c, tree, err)
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

// UpdateFolder 改 folder 的可读属性。
//
// 字段契约(2024-12 锁定):
//   - 唯一可修改的字段:`name` 和 `comment`
//   - 不可通过本端点修改:`id` / `code` / `environmentId` / `parentId` /
//     `level` / `createdBy` / `createdAt` —— 这些列在 repo `updateEntity` 的
//     SQL `SET` 列表外,即使请求里塞了也会被静默忽略
//   - 请求里的 `parentId` 仅作为「按 (envId, code) 查 folder」的 lookup 字段,
//     不是要写入 folder 行的字段
//
// 权限契约:仅校验 `folder:update` 在目标 folder 上。不再叠加其他权限。
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

// validateEnvIdsForCreate 校验 createFolderRequest 的 envList 字段(env id 列表)。
// 返回 "" 表示通过,非空表示错误信息(直接给到客户端)。
//
// 规则:
//   - 每项必须是合法 UUID 格式(8-4-4-4-12)
//   - 长度非空(空数组校验在 handler 上层先做,因为空数组走 -1 通用失败而不是 1002)
//
// 抽成纯函数是为了让单测不依赖 controller 全套依赖(repo / authorizer / cache)。
// 元素在 env 层面的存在性在 repo 端校验(查 environments.is_deleted)。
func validateEnvIdsForCreate(envIds []string) string {
	for _, id := range envIds {
		if !uuidPattern.MatchString(id) {
			return fmt.Sprintf("envList contains invalid id: %s (must be a UUID)", id)
		}
	}
	return ""
}
