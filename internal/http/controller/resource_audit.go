package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/logging"
)

func (ctrl *Controller) ListAuditRecords(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListAuditRecords", logging.F("resource_type", req.ResourceType), logging.F("resource_id", req.ResourceId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListAuditRecords(c.Request.Context(), req.ResourceType, req.ResourceId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}
