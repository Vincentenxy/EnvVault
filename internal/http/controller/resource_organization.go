package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/domain"
	"envVault/internal/logging"
)

func (ctrl *Controller) CreateOrganization(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	if !ctrl.allowScope(c, "org:create", "global", "") {
		return
	}
	ctrl.log(c, "CreateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.repo.CreateOrganization(c.Request.Context(), req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListOrganizations(c *gin.Context) {
	var req PageRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "org:read", "global", "") {
		return
	}
	ctrl.log(c, "ListOrganizations")
	pagination := paginationFromRequest(req)
	result, err := ctrl.repo.ListOrganizations(c.Request.Context(), pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetOrganization(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "organization") {
		return
	}
	var item domain.Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetOrganization", logging.F("code", req.Code))
		item, err = ctrl.repo.GetOrganizationByCode(c.Request.Context(), req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "org:read", "organization", item.Id) {
			return
		}
	} else {
		if !ctrl.allowScope(c, "org:read", "organization", req.Id) {
			return
		}
		ctrl.log(c, "GetOrganization", logging.F("id", req.Id))
		item, err = ctrl.repo.GetOrganization(c.Request.Context(), req.Id)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateOrganization(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "organization") {
		return
	}
	var item domain.Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
		var org domain.Entity
		if org, err = ctrl.repo.GetOrganizationByCode(c.Request.Context(), req.Code); err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "org:update", "organization", org.Id) {
			return
		}
		item, err = ctrl.repo.UpdateOrganization(c.Request.Context(), org.Id, req.Name, req.Comment, ctrl.actor(c))
	} else {
		if !ctrl.allowScope(c, "org:update", "organization", req.Id) {
			return
		}
		ctrl.log(c, "UpdateOrganization", logging.F("id", req.Id), logging.F("name", req.Name))
		item, err = ctrl.repo.UpdateOrganization(c.Request.Context(), req.Id, req.Name, req.Comment, ctrl.actor(c))
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteOrganization(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "organization") {
		return
	}
	ctrl.log(c, "DeleteOrganization")
	if req.Code != "" {
		org, err := ctrl.repo.GetOrganizationByCode(c.Request.Context(), req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "org:delete", "organization", org.Id) {
			return
		}
		err = ctrl.repo.DeleteOrganization(c.Request.Context(), org.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
		return
	}
	if !ctrl.allowScope(c, "org:delete", "organization", req.Id) {
		return
	}
	err := ctrl.repo.DeleteOrganization(c.Request.Context(), req.Id, ctrl.actor(c))
	ctrl.write(c, gin.H{"deleted": true}, err)
}
