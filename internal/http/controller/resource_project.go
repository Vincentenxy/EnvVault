package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/logging"
)

func (ctrl *Controller) CreateProject(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	ctrl.log(c, "CreateProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateProject(c.Request.Context(), req.ParentId, req.Code, req.Name, req.Comment, ctrl.actor(c), req.EnvironmentIds)
	ctrl.write(c, item, err)
}

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
	result, err := ctrl.store.ListProjects(c.Request.Context(), req.OrgId, pagination)
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
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.store.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
	} else {
		ctrl.log(c, "GetProject", logging.F("id", req.Id))
		item, err = ctrl.store.GetProject(c.Request.Context(), req.Id)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateProject(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "project") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
		proj, getErr := ctrl.store.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateProject(c.Request.Context(), proj.Id, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateProject", logging.F("id", req.Id), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateProject(c.Request.Context(), req.Id, req.Name, req.Comment, ctrl.actor(c))
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
	if req.Code != "" {
		proj, err := ctrl.store.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteProject(c.Request.Context(), proj.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteProject(c.Request.Context(), req.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}
