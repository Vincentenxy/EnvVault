package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/logging"
)

func (ctrl *Controller) CreateEnvironment(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	if !ctrl.allowScope(c, "env:create", "project", req.ParentId) {
		return
	}
	ctrl.log(c, "CreateEnvironment", logging.F("project_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.repo.CreateEnvironment(c.Request.Context(), req.ParentId, req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

// ListEnvironments v7 起不再走 allowScope 入口;repo SQL 按 caller.UserId 自动收窄可见 env。
// parent 过滤(同 project 内)继续保留,projectId 入参仍由 validateListEnvironments 校验非空。
func (ctrl *Controller) ListEnvironments(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListEnvironments(c, req) {
		return
	}
	ctrl.log(c, "ListEnvironments", logging.F("project_id", req.ProjectId))
	pagination := paginationFromRequest(req.PageRequest)
	userId := auth.UserFromContext(c).UserId
	result, err := ctrl.repo.ListEnvironments(c.Request.Context(), userId, req.ProjectId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetEnvironment(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "environment") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	var item Entity
	var err error
	if useCode {
		ctrl.log(c, "GetEnvironment", logging.F("project_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.repo.GetEnvironmentByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = item.Id
	} else {
		ctrl.log(c, "GetEnvironment", logging.F("id", req.Id))
		item, err = ctrl.repo.GetEnvironment(c.Request.Context(), rid)
	}
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if !ctrl.allowScope(c, "env:read", "environment", rid) {
		return
	}
	ctrl.write(c, item, nil)
}

func (ctrl *Controller) UpdateEnvironment(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "environment") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		ctrl.log(c, "UpdateEnvironment", logging.F("project_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
		env, err := ctrl.repo.GetEnvironmentByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = env.Id
	} else {
		ctrl.log(c, "UpdateEnvironment", logging.F("id", req.Id), logging.F("name", req.Name))
	}
	if !ctrl.allowScope(c, "env:update", "environment", rid) {
		return
	}
	item, err := ctrl.repo.UpdateEnvironment(c.Request.Context(), rid, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteEnvironment(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "environment") {
		return
	}
	ctrl.log(c, "DeleteEnvironment")
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		env, err := ctrl.repo.GetEnvironmentByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = env.Id
	}
	if !ctrl.allowScope(c, "env:delete", "environment", rid) {
		return
	}
	ctrl.write(c, gin.H{"deleted": true}, ctrl.repo.DeleteEnvironment(c.Request.Context(), rid, ctrl.actor(c)))
}
