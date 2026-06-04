package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/logging"
)

// ListEnvironmentTemplates v7 起不再走 allowScope 入口;repo SQL 按 caller.UserId 自动收窄可见 env_template。
// parent 过滤(同 org 内)继续保留,orgId 入参仍由 validateListEnvironmentTemplates 校验非空。
func (ctrl *Controller) ListEnvironmentTemplates(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListEnvironmentTemplates(c, req) {
		return
	}
	ctrl.log(c, "ListEnvironmentTemplates", logging.F("org_id", req.OrgId))
	pagination := paginationFromRequest(req.PageRequest)
	userId := auth.UserFromContext(c).UserId
	result, err := ctrl.repo.ListEnvironmentTemplates(c.Request.Context(), userId, req.OrgId, pagination)
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
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	var item domain.EnvironmentTemplate
	var err error
	if useCode {
		ctrl.log(c, "GetEnvironmentTemplate", logging.F("org_id", req.ParentId), logging.F("code", req.Code))
		item, err = ctrl.repo.GetEnvironmentTemplateByCode(c.Request.Context(), req.ParentId, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = item.Id
	} else {
		ctrl.log(c, "GetEnvironmentTemplate", logging.F("id", req.Id))
		item, err = ctrl.repo.GetEnvironmentTemplate(c.Request.Context(), rid)
	}
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if !ctrl.allowScope(c, "env:template:read", "env_template", rid) {
		return
	}
	ctrl.write(c, item, nil)
}
