package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/logging"
)

// GetResourceTree 处理 POST /api/v1/tree/get。
//
// 入口权限:仅 JWT(由 protected group 中间件校验),不下沉到 service 也不新增
// tree:read 权限码。可见范围由 4 个 :read 码在 SQL narrowing 处自然收窄
// (org:read / project:read / env:read / folder:read),与 ListXxx 行为对齐。
func (ctrl *Controller) GetResourceTree(c *gin.Context) {
	var req domain.TreeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if ctrl.tree == nil {
		// tree service 不可用 = 503;bind() 不会抓,这里单独防
		logging.Error(c.Request.Context(), "GetResourceTree", "tree service is not configured")
		c.JSON(503, gin.H{"code": 1503, "msg": "tree service is not configured", "data": nil})
		return
	}
	ctrl.log(c, "GetResourceTree",
		logging.F("maxDepth", req.MaxDepth),
		logging.F("includeOrphans", req.IncludeOrphans))
	user := auth.UserFromContext(c)
	tree, err := ctrl.tree.GetTree(c.Request.Context(), user, req)
	ctrl.write(c, tree, err)
}
