package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
)

func (ctrl *Controller) ListPermissions(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req PageRequest
	if !ctrl.bind(c, &req) {
		return
	}
	pagination := paginationFromRequest(req)
	result, err := ctrl.rbac.ListPermissions(c.Request.Context(), pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetMyPermissions(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req scopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	result, err := ctrl.rbac.EffectivePermissions(c.Request.Context(), user.UserId, req.ScopeType, req.ScopeId)
	ctrl.write(c, gin.H{"permissions": result.Permissions}, err)
}
