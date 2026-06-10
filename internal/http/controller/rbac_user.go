package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
)

func (ctrl *Controller) GetCurrentRBACUser(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req PageRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	if _, err := ctrl.rbac.SyncUser(c.Request.Context(), user, user.UserId, user.Name, ""); err != nil {
		ctrl.write(c, nil, err)
		return
	}
	pagination := paginationFromRequest(req)
	grants, err := ctrl.rbac.ListUserGrants(c.Request.Context(), user, user.UserId, pagination)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, pageData(toGrants(grants.Items), grants.Total, pagination), nil)
}

func (ctrl *Controller) ListRBACUsers(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pageScopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListUsers(c.Request.Context(), user, req.ScopeType, req.ScopeId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) ListUserGrants(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pagedUserLookupRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	pagination := paginationFromRequest(req.PageRequest)
	userId := pickAlias(req.UserId, req.ExternalUserId)
	result, err := ctrl.rbac.ListUserGrants(c.Request.Context(), user, userId, pagination)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, pageData(toGrants(result.Items), result.Total, pagination), nil)
}

func (ctrl *Controller) GetUserEffectivePermissions(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req userLookupRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	userId := pickAlias(req.UserId, req.ExternalUserId)
	item, err := ctrl.rbac.EffectivePermissions(c.Request.Context(), user, userId, req.ScopeType, req.ScopeId)
	ctrl.write(c, item, err)
}
