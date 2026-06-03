package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/domain"
	"envVault/internal/logging"
)

func (ctrl *Controller) ListEnvironmentTemplates(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListEnvironmentTemplates(c, req) {
		return
	}
	if !ctrl.allowScope(c, "env:template:read", "organization", req.OrgId) {
		return
	}
	ctrl.log(c, "ListEnvironmentTemplates", logging.F("org_id", req.OrgId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.repo.ListEnvironmentTemplates(c.Request.Context(), req.OrgId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetEnvironmentTemplate(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "env_template") {
		return
	}
	var item domain.EnvironmentTemplate
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetEnvironmentTemplate", logging.F("org_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.repo.GetEnvironmentTemplateByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		if !ctrl.allowScope(c, "env:template:read", "env_template", item.Id) {
			return
		}
	} else {
		if !ctrl.allowScope(c, "env:template:read", "env_template", req.Id) {
			return
		}
		ctrl.log(c, "GetEnvironmentTemplate", logging.F("id", req.Id))
		item, err = ctrl.repo.GetEnvironmentTemplate(c.Request.Context(), req.Id)
	}
	ctrl.write(c, item, err)
}
