package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/store/postgres"
)

func (ctrl *Controller) ListRoleBindings(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pageScopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:read", req.ScopeType, req.ScopeId) {
		return
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListRoleBindings(c.Request.Context(), req.ScopeType, req.ScopeId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GrantRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleGrantRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:manage", req.ScopeType, req.ScopeId) {
		return
	}
	item, err := ctrl.rbac.GrantRole(c.Request.Context(), postgres.GrantInput{
		ExternalUserId: req.ExternalUserId,
		Name:           req.Name,
		Email:          req.Email,
		RoleCode:       req.RoleCode,
		ScopeType:      req.ScopeType,
		ScopeId:        req.ScopeId,
		ExpiresAt:      req.ExpiresAt,
		Actor:          ctrl.actor(c),
	})
	ctrl.write(c, item, err)
}

func (ctrl *Controller) RevokeRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleGrantRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:manage", req.ScopeType, req.ScopeId) {
		return
	}
	err := ctrl.rbac.RevokeRole(c.Request.Context(), postgres.GrantInput{
		ExternalUserId: req.ExternalUserId,
		RoleCode:       req.RoleCode,
		ScopeType:      req.ScopeType,
		ScopeId:        req.ScopeId,
		Actor:          ctrl.actor(c),
	})
	ctrl.write(c, gin.H{"deleted": true}, err)
}
