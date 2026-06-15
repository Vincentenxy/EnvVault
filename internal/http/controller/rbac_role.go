package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
)

// ListRoles 列出系统中所有未删除的角色(含 system 和 custom)。
//
// v12 起:
//   - 改为 GET 方法,无请求体、无入参(去掉 scopeType / scopeId / 分页)。
//   - 权限闸门仅需 JWT 已认证(对齐 ListPermissions);角色定义本身不敏感,
//     供任何已登录用户做"选授权角色"等 UI 场景使用。
//   - 响应体 `data` 直接是数组,不再嵌套 `items` / 分页字段。
func (ctrl *Controller) ListRoles(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	items, err := ctrl.rbac.ListRoles(c.Request.Context())
	ctrl.write(c, items, err)
}

func (ctrl *Controller) GetRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleInfoRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	item, err := ctrl.rbac.GetRole(c.Request.Context(), user, req.Id, req.Code)
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
	user := auth.UserFromContext(c)
	item, err := ctrl.rbac.CreateRole(c.Request.Context(), user,
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
	user := auth.UserFromContext(c)
	item, err := ctrl.rbac.UpdateRole(c.Request.Context(), user,
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
	user := auth.UserFromContext(c)
	ctrl.write(c, gin.H{"deleted": true}, ctrl.rbac.DeleteRole(c.Request.Context(), user, req.Id, ctrl.actor(c)))
}
