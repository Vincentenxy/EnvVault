package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/domain"
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
		specs = append(specs, domain.EnvSpec{Code: e.Code, Name: e.Name, Comment: e.Comment})
	}
	ctrl.log(c, "CreateProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code), logging.F("env_count", len(specs)))
	item, err := ctrl.repo.CreateProject(c.Request.Context(), req.ParentId, req.Code, req.Name, req.Comment, ctrl.actor(c), specs)
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
	if !ctrl.allowScope(c, "project:read", "organization", req.OrgId) {
		return
	}
	ctrl.log(c, "ListProjects", logging.F("org_id", req.OrgId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.repo.ListProjects(c.Request.Context(), req.OrgId, pagination)
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
	var item domain.Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.repo.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "project:read", "project", item.Id) {
			return
		}
	} else {
		if !ctrl.allowScope(c, "project:read", "project", req.Id) {
			return
		}
		ctrl.log(c, "GetProject", logging.F("id", req.Id))
		item, err = ctrl.repo.GetProject(c.Request.Context(), req.Id)
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
	var item domain.Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateProject", logging.F("org_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
		var proj domain.Entity
		if proj, err = ctrl.repo.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code); err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "project:update", "project", proj.Id) {
			return
		}
		item, err = ctrl.repo.UpdateProject(c.Request.Context(), proj.Id, req.Name, req.Comment, ctrl.actor(c))
	} else {
		if !ctrl.allowScope(c, "project:update", "project", req.Id) {
			return
		}
		ctrl.log(c, "UpdateProject", logging.F("id", req.Id), logging.F("name", req.Name))
		item, err = ctrl.repo.UpdateProject(c.Request.Context(), req.Id, req.Name, req.Comment, ctrl.actor(c))
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
		proj, err := ctrl.repo.GetProjectByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "project:delete", "project", proj.Id) {
			return
		}
		err = ctrl.repo.DeleteProject(c.Request.Context(), proj.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
		return
	}
	if !ctrl.allowScope(c, "project:delete", "project", req.Id) {
		return
	}
	err := ctrl.repo.DeleteProject(c.Request.Context(), req.Id, ctrl.actor(c))
	ctrl.write(c, gin.H{"deleted": true}, err)
}
