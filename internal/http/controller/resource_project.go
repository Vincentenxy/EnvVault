package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/logging"
	rediscache "envVault/internal/store/redis"
)

func (ctrl *Controller) CreateProject(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	if !ctrl.allowScope(c, "project:create", "organization", req.ParentId) {
		return
	}
	for _, e := range req.Environments {
		if !validateCode(c, e.Code) {
			return
		}
	}
	specs := make([]domain.EnvSpec, 0, len(req.Environments))
	for _, e := range req.Environments {
		specs = append(specs, domain.EnvSpec{Code: e.Code, Name: e.Name, Comment: e.Comment, SortOrder: e.SortOrder})
	}
	ctrl.log(c, "CreateProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code), logging.F("env_count", len(specs)))
	item, err := ctrl.repo.CreateProject(c.Request.Context(), req.ParentId, req.Code, req.Name, req.Comment, ctrl.actor(c), specs)
	if err == nil {
		ctrl.cacheUpsert(c, func(rc *rediscache.Cache) error { return rc.UpsertProject(c.Request.Context(), item) })
		// 注:CreateProject 顺带创建了若干 env,但 env ids 在 CreateProject 返回值里
		// 不可见(只返回 project)。env 的 cache 同步依赖下次 env:list/tree:warm 触发。
		// 这里为了准确性,先不主动补 env;若一致性要求更严可改成 repo 返回 project+envs。
	}
	ctrl.write(c, item, err)
}

// ListProjects v7 起不再走 allowScope 入口;repo SQL 按 caller.UserId 自动收窄可见 project。
// parent 过滤(同 org 内)继续保留,org_id 入参仍由 validateListProjects 校验非空。
func (ctrl *Controller) ListProjects(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListProjects(c, req) {
		return
	}
	ctrl.log(c, "ListProjects", logging.F("org_id", req.OrgId))
	pagination := paginationFromRequest(req.PageRequest)
	userId := auth.UserFromContext(c).UserId
	result, err := ctrl.repo.ListProjects(c.Request.Context(), userId, req.OrgId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetProject(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "project") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	var item domain.Entity
	var err error
	if useCode {
		ctrl.log(c, "GetProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.repo.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = item.Id
	} else {
		ctrl.log(c, "GetProject", logging.F("id", req.Id))
		item, err = ctrl.repo.GetProject(c.Request.Context(), rid)
	}
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if !ctrl.allowScope(c, "project:read", "project", rid) {
		return
	}
	ctrl.write(c, item, nil)
}

func (ctrl *Controller) UpdateProject(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "project") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		ctrl.log(c, "UpdateProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
		proj, err := ctrl.repo.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = proj.Id
	} else {
		ctrl.log(c, "UpdateProject", logging.F("id", req.Id), logging.F("name", req.Name))
	}
	if !ctrl.allowScope(c, "project:update", "project", rid) {
		return
	}
	item, err := ctrl.repo.UpdateProject(c.Request.Context(), rid, req.Name, req.Comment, ctrl.actor(c))
	if err == nil {
		ctrl.cacheUpsert(c, func(rc *rediscache.Cache) error { return rc.UpsertProject(c.Request.Context(), item) })
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteProject(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "project") {
		return
	}
	ctrl.log(c, "DeleteProject")
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		proj, err := ctrl.repo.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = proj.Id
	}
	if !ctrl.allowScope(c, "project:delete", "project", rid) {
		return
	}
	scope, err := ctrl.repo.DeleteProject(c.Request.Context(), rid, ctrl.actor(c))
	if err == nil {
		ctrl.cacheInvalidateCascade(c, scope)
	}
	ctrl.write(c, gin.H{"deleted": true}, err)
}
