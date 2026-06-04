package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/logging"
)

func (ctrl *Controller) CreateOrganization(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	if !ctrl.allowScope(c, "org:create", "global", "") {
		return
	}
	ctrl.log(c, "CreateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.repo.CreateOrganization(c.Request.Context(), req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

// ListOrganizations v7 起不再走 allowScope 入口;repo SQL 按 caller.UserId 自动收窄可见 org。
// 入口校验剔除后,`org_admin` 绑在 (org, X) 的 caller 也能 ListOrganizations 看到 X。
func (ctrl *Controller) ListOrganizations(c *gin.Context) {
	var req PageRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListOrganizations")
	pagination := paginationFromRequest(req)
	userId := auth.UserFromContext(c).UserId
	result, err := ctrl.repo.ListOrganizations(c.Request.Context(), userId, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetOrganization(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "organization") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	var item domain.Entity
	var err error
	if useCode {
		ctrl.log(c, "GetOrganization", logging.F("code", req.Code))
		item, err = ctrl.repo.GetOrganizationByCode(c.Request.Context(), req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = item.Id
	} else {
		ctrl.log(c, "GetOrganization", logging.F("id", req.Id))
		item, err = ctrl.repo.GetOrganization(c.Request.Context(), rid)
	}
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if !ctrl.allowScope(c, "org:read", "organization", rid) {
		return
	}
	ctrl.write(c, item, nil)
}

func (ctrl *Controller) UpdateOrganization(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "organization") {
		return
	}
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		ctrl.log(c, "UpdateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
		org, err := ctrl.repo.GetOrganizationByCode(c.Request.Context(), req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = org.Id
	} else {
		ctrl.log(c, "UpdateOrganization", logging.F("id", req.Id), logging.F("name", req.Name))
	}
	if !ctrl.allowScope(c, "org:update", "organization", rid) {
		return
	}
	item, err := ctrl.repo.UpdateOrganization(c.Request.Context(), rid, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

// deleteOrganizationRequest 在 idOrCodeRequest 基础上加 force 字段。
// force=true 时触发 4 级级联软删(必须额外拥有 org:force_delete 权限)。
type deleteOrganizationRequest struct {
	idOrCodeRequest
	Force bool `json:"force"`
}

func (ctrl *Controller) DeleteOrganization(c *gin.Context) {
	var req deleteOrganizationRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req.idOrCodeRequest, "organization") {
		return
	}
	ctrl.log(c, "DeleteOrganization", logging.F("force", req.Force))
	rid, useCode := resolveIdOrCode(req.Id, req.Code)
	if useCode {
		org, err := ctrl.repo.GetOrganizationByCode(c.Request.Context(), req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		rid = org.Id
	}
	// 删除权限是基础门槛;force 路径再额外校验 org:force_delete。
	if !ctrl.allowScope(c, "org:delete", "organization", rid) {
		return
	}
	if req.Force && !ctrl.allowScope(c, "org:force_delete", "organization", rid) {
		return
	}
	ctrl.write(c, gin.H{"deleted": true}, ctrl.repo.DeleteOrganization(c.Request.Context(), rid, ctrl.actor(c), req.Force))
}
