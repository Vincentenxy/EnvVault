package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
)

func (ctrl *Controller) ListRoleBindings(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pageScopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListRoleBindings(c.Request.Context(), user, req.ScopeType, req.ScopeId, pagination)
	ctrl.write(c, pageData(toGrants(result.Items), result.Total, pagination), err)
}

func (ctrl *Controller) GrantRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleGrantRequest
	if !ctrl.bind(c, &req) {
		return
	}
	r := req.resolve()
	user := auth.UserFromContext(c)
	item, err := ctrl.rbac.GrantRole(c.Request.Context(), user,
		r.UserId, r.Name, r.Email, r.RoleCode,
		r.ScopeType, r.ScopeId, r.ExpiresAt, ctrl.actor(c),
	)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, item.ToGrant(), nil)
}

func (ctrl *Controller) RevokeRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleGrantRequest
	if !ctrl.bind(c, &req) {
		return
	}
	r := req.resolve()
	user := auth.UserFromContext(c)
	err := ctrl.rbac.RevokeRole(c.Request.Context(), user,
		r.UserId, r.RoleCode, r.ScopeType, r.ScopeId, ctrl.actor(c),
	)
	ctrl.write(c, gin.H{"deleted": true}, err)
}

// toGrants 把 RoleBinding 列表转成 RoleGrant 列表,controller 出口字段 SDK 友好。
func toGrants(bindings []domain.RoleBinding) []domain.RoleGrant {
	out := make([]domain.RoleGrant, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.ToGrant())
	}
	return out
}
