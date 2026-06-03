package controller

import (
	"github.com/gin-gonic/gin"
)

func (ctrl *Controller) ListRoles(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pageScopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:read", req.ScopeType, req.ScopeId) {
		return
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListRoles(c.Request.Context(), req.ScopeType, req.ScopeId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleInfoRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:read", "global", "") {
		return
	}
	item, err := ctrl.rbac.GetRole(c.Request.Context(), req.Id, req.Code)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) CreateRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:manage", req.ScopeType, req.ScopeId) {
		return
	}
	item, err := ctrl.rbac.CreateRole(c.Request.Context(),
		req.Code, req.Name, req.Description, req.ScopeType, req.ScopeId, req.Permissions, ctrl.actor(c),
	)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:manage", req.ScopeType, req.ScopeId) {
		return
	}
	item, err := ctrl.rbac.UpdateRole(c.Request.Context(),
		req.Id, req.Code, req.Name, req.Description, req.ScopeType, req.ScopeId, req.Permissions, ctrl.actor(c),
	)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:manage", "global", "") {
		return
	}
	ctrl.write(c, gin.H{"deleted": true}, ctrl.rbac.DeleteRole(c.Request.Context(), req.Id, ctrl.actor(c)))
}
