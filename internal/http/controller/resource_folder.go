package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/logging"
)

func (ctrl *Controller) CreateFolder(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	ctrl.log(c, "CreateFolder", logging.F("environment_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateFolder(c.Request.Context(), req.ParentId, req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListFolders(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListFolders(c, req) {
		return
	}
	ctrl.log(c, "ListFolders", logging.F("environment_id", req.EnvironmentId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListFolders(c.Request.Context(), req.EnvironmentId, pagination)
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
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetFolder", logging.F("environment_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.store.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
	} else {
		ctrl.log(c, "GetFolder", logging.F("id", req.Id))
		item, err = ctrl.store.GetFolder(c.Request.Context(), req.Id)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateFolder(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "folder") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateFolder", logging.F("environment_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
		folder, getErr := ctrl.store.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateFolder(c.Request.Context(), folder.Id, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateFolder", logging.F("id", req.Id), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateFolder(c.Request.Context(), req.Id, req.Name, req.Comment, ctrl.actor(c))
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
	if req.Code != "" {
		folder, err := ctrl.store.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteFolder(c.Request.Context(), folder.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteFolder(c.Request.Context(), req.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}
