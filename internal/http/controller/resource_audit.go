package controller

import (
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/logging"
)

func (ctrl *Controller) ListAuditRecords(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	scopeType, scopeId := strings.TrimSpace(req.ResourceType), strings.TrimSpace(req.ResourceId)
	if scopeType == "" || scopeId == "" {
		scopeType, scopeId = "global", ""
	}
	if !ctrl.allowScope(c, "audit:read", scopeType, scopeId) {
		return
	}
	ctrl.log(c, "ListAuditRecords", logging.F("resource_type", req.ResourceType), logging.F("resource_id", req.ResourceId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.repo.ListAuditRecords(c.Request.Context(), req.ResourceType, req.ResourceId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}
