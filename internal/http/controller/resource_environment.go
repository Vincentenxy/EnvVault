package controller

import (
	"github.com/gin-gonic/gin"

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
	ctrl.log(c, "CreateEnvironment", logging.F("project_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateEnvironment(c.Request.Context(), req.ParentId, req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListEnvironments(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListEnvironments(c, req) {
		return
	}
	ctrl.log(c, "ListEnvironments", logging.F("org_id", req.OrgId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListEnvironments(c.Request.Context(), req.OrgId, pagination)
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
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetEnvironment", logging.F("org_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.store.GetEnvironmentByCode(c.Request.Context(), req.ParentId, req.Code)
	} else {
		ctrl.log(c, "GetEnvironment", logging.F("id", req.Id))
		item, err = ctrl.store.GetEnvironment(c.Request.Context(), req.Id)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateEnvironment(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "environment") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateEnvironment", logging.F("org_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
		env, getErr := ctrl.store.GetEnvironmentByCode(c.Request.Context(), req.ParentId, req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateEnvironment(c.Request.Context(), env.Id, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateEnvironment", logging.F("id", req.Id), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateEnvironment(c.Request.Context(), req.Id, req.Name, req.Comment, ctrl.actor(c))
	}
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
	if req.Code != "" {
		env, err := ctrl.store.GetEnvironmentByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteEnvironment(c.Request.Context(), env.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteEnvironment(c.Request.Context(), req.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}
