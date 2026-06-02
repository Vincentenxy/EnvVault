package controller

import (
	"github.com/gin-gonic/gin"

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
	ctrl.log(c, "CreateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateOrganization(c.Request.Context(), req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListOrganizations(c *gin.Context) {
	var req PageRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListOrganizations")
	pagination := paginationFromRequest(req)
	result, err := ctrl.store.ListOrganizations(c.Request.Context(), pagination)
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
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetOrganization", logging.F("code", req.Code))
		item, err = ctrl.store.GetOrganizationByCode(c.Request.Context(), req.Code)
	} else {
		ctrl.log(c, "GetOrganization", logging.F("id", req.Id))
		item, err = ctrl.store.GetOrganization(c.Request.Context(), req.Id)
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
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
		org, getErr := ctrl.store.GetOrganizationByCode(c.Request.Context(), req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateOrganization(c.Request.Context(), org.Id, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateOrganization", logging.F("id", req.Id), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateOrganization(c.Request.Context(), req.Id, req.Name, req.Comment, ctrl.actor(c))
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
		org, err := ctrl.store.GetOrganizationByCode(c.Request.Context(), req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteOrganization(c.Request.Context(), org.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteOrganization(c.Request.Context(), req.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}
