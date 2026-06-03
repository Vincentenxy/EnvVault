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
	if !ctrl.allowScope(c, "folder:create", "environment", req.ParentId) {
		return
	}
	ctrl.log(c, "CreateFolder", logging.F("environment_id", req.ParentId), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.repo.CreateFolder(c.Request.Context(), req.ParentId, req.Code, req.Name, req.Comment, ctrl.actor(c))
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
	if !ctrl.allowScope(c, "folder:read", "environment", req.EnvironmentId) {
		return
	}
	ctrl.log(c, "ListFolders", logging.F("environment_id", req.EnvironmentId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.repo.ListFolders(c.Request.Context(), req.EnvironmentId, pagination)
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
		item, err = ctrl.repo.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "folder:read", "folder", item.Id) {
			return
		}
	} else {
		if !ctrl.allowScope(c, "folder:read", "folder", req.Id) {
			return
		}
		ctrl.log(c, "GetFolder", logging.F("id", req.Id))
		item, err = ctrl.repo.GetFolder(c.Request.Context(), req.Id)
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
		var folder Entity
		if folder, err = ctrl.repo.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code); err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "folder:update", "folder", folder.Id) {
			return
		}
		item, err = ctrl.repo.UpdateFolder(c.Request.Context(), folder.Id, req.Name, req.Comment, ctrl.actor(c))
	} else {
		if !ctrl.allowScope(c, "folder:update", "folder", req.Id) {
			return
		}
		ctrl.log(c, "UpdateFolder", logging.F("id", req.Id), logging.F("name", req.Name))
		item, err = ctrl.repo.UpdateFolder(c.Request.Context(), req.Id, req.Name, req.Comment, ctrl.actor(c))
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
		folder, err := ctrl.repo.GetFolderByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "folder:delete", "folder", folder.Id) {
			return
		}
		err = ctrl.repo.DeleteFolder(c.Request.Context(), folder.Id, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
		return
	}
	if !ctrl.allowScope(c, "folder:delete", "folder", req.Id) {
		return
	}
	err := ctrl.repo.DeleteFolder(c.Request.Context(), req.Id, ctrl.actor(c))
	ctrl.write(c, gin.H{"deleted": true}, err)
}
