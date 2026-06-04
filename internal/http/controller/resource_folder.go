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

// ListFolders v7 起不再走 allowScope 入口;repo SQL 按 caller.UserId 自动收窄可见 folder。
// envId/parentId 的「父 folder 反查 env」逻辑保留(GetFolderEnvId 仍然需要),
// 因为 level=2 list 时父 folder.id 是路径定位的关键,不是 authz。
func (ctrl *Controller) ListFolders(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListFolders(c, req) {
		return
	}

	envId := strings.TrimSpace(req.EnvironmentId)
	parentId := strings.TrimSpace(req.FolderParentId)

	// 两种模式:
	//   - environmentId 非空:列 env 下所有 level=1 (parent_id IS NULL) folder
	//   - folderParentId 非空:列该父 folder 下所有 level=2 folder;env 由后端从父 folder 反查
	// (反查是路径定位,不是 authz)
	_ = envId
	if parentId != "" {
		if _, err := ctrl.repo.GetFolderEnvId(c.Request.Context(), parentId); err != nil {
			ctrl.write(c, nil, err)
			return
		}
	}

	ctrl.log(c, "ListFolders", logging.F("environment_id", envId), logging.F("folder_parent_id", parentId))
	pagination := paginationFromRequest(req.PageRequest)
	userId := auth.UserFromContext(c).UserId
	result, err := ctrl.repo.ListFolders(c.Request.Context(), userId, envId, parentId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
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
