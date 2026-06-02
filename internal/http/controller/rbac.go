package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	"envVault/internal/store/postgres"
)

type scopeRequest struct {
	ScopeType string `json:"scopeType"`
	ScopeID   string `json:"scopeId"`
}

type pageScopeRequest struct {
	PageRequest
	ScopeType string `json:"scopeType"`
	ScopeID   string `json:"scopeId"`
}

type roleInfoRequest struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

type roleRequest struct {
	ID          string   `json:"id,omitempty"`
	ScopeType   string   `json:"scopeType"`
	ScopeID     string   `json:"scopeId"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type roleGrantRequest struct {
	ExternalUserID string     `json:"externalUserId"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	RoleCode       string     `json:"roleCode"`
	ScopeType      string     `json:"scopeType"`
	ScopeID        string     `json:"scopeId"`
	ExpiresAt      *time.Time `json:"expiresAt"`
}

type userLookupRequest struct {
	ExternalUserID string `json:"externalUserId"`
	ScopeType      string `json:"scopeType"`
	ScopeID        string `json:"scopeId"`
}

type pagedUserLookupRequest struct {
	PageRequest
	ExternalUserID string `json:"externalUserId"`
}

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
	result, err := ctrl.rbac.EffectivePermissions(c.Request.Context(), user.UserId, req.ScopeType, req.ScopeID)
	ctrl.write(c, gin.H{"permissions": result.Permissions}, err)
}

func (ctrl *Controller) ListRoles(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pageScopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:read", req.ScopeType, req.ScopeID) {
		return
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListRoles(c.Request.Context(), req.ScopeType, req.ScopeID, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleInfoRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:read", "global", "") {
		return
	}
	item, err := ctrl.rbac.GetRole(c.Request.Context(), req.ID, req.Code)
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
	if !ctrl.allowScope(c, "rbac:role:manage", req.ScopeType, req.ScopeID) {
		return
	}
	item, err := ctrl.rbac.CreateRole(c.Request.Context(), postgres.RoleInput{
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		ScopeType:   req.ScopeType,
		ScopeID:     req.ScopeID,
		Permissions: req.Permissions,
		Actor:       ctrl.actor(c),
	})
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
	if !ctrl.allowScope(c, "rbac:role:manage", req.ScopeType, req.ScopeID) {
		return
	}
	item, err := ctrl.rbac.UpdateRole(c.Request.Context(), postgres.RoleInput{
		ID:          req.ID,
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		ScopeType:   req.ScopeType,
		ScopeID:     req.ScopeID,
		Permissions: req.Permissions,
		Actor:       ctrl.actor(c),
	})
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
	if !ctrl.allowScope(c, "rbac:role:manage", "global", "") {
		return
	}
	ctrl.write(c, gin.H{"deleted": true}, ctrl.rbac.DeleteRole(c.Request.Context(), req.ID, ctrl.actor(c)))
}

func (ctrl *Controller) ListRoleBindings(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pageScopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:read", req.ScopeType, req.ScopeID) {
		return
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListRoleBindings(c.Request.Context(), req.ScopeType, req.ScopeID, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GrantRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleGrantRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:manage", req.ScopeType, req.ScopeID) {
		return
	}
	item, err := ctrl.rbac.GrantRole(c.Request.Context(), postgres.GrantInput{
		ExternalUserID: req.ExternalUserID,
		Name:           req.Name,
		Email:          req.Email,
		RoleCode:       req.RoleCode,
		ScopeType:      req.ScopeType,
		ScopeID:        req.ScopeID,
		ExpiresAt:      req.ExpiresAt,
		Actor:          ctrl.actor(c),
	})
	ctrl.write(c, item, err)
}

func (ctrl *Controller) RevokeRole(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req roleGrantRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:manage", req.ScopeType, req.ScopeID) {
		return
	}
	err := ctrl.rbac.RevokeRole(c.Request.Context(), postgres.GrantInput{
		ExternalUserID: req.ExternalUserID,
		RoleCode:       req.RoleCode,
		ScopeType:      req.ScopeType,
		ScopeID:        req.ScopeID,
		Actor:          ctrl.actor(c),
	})
	ctrl.write(c, gin.H{"deleted": true}, err)
}

func (ctrl *Controller) GetCurrentRBACUser(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req PageRequest
	if !ctrl.bind(c, &req) {
		return
	}
	user := auth.UserFromContext(c)
	if _, err := ctrl.rbac.SyncUser(c.Request.Context(), user.UserId, user.Name, ""); err != nil {
		ctrl.write(c, nil, err)
		return
	}
	pagination := paginationFromRequest(req)
	grants, err := ctrl.rbac.ListUserGrants(c.Request.Context(), user.UserId, pagination)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, pageData(grants.Items, grants.Total, pagination), nil)
}

func (ctrl *Controller) ListRBACUsers(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req pageScopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:read", req.ScopeType, req.ScopeID) {
		return
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListUsers(c.Request.Context(), req.ScopeType, req.ScopeID, pagination)
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
	if !ctrl.allowScope(c, "rbac:binding:read", "global", "") {
		return
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.rbac.ListUserGrants(c.Request.Context(), req.ExternalUserID, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetUserEffectivePermissions(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req userLookupRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:read", req.ScopeType, req.ScopeID) {
		return
	}
	item, err := ctrl.rbac.EffectivePermissions(c.Request.Context(), req.ExternalUserID, req.ScopeType, req.ScopeID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ensureRBAC(c *gin.Context) bool {
	if ctrl.rbac == nil {
		logging.Error(c.Request.Context(), "ensureRBAC", "rbac store is not configured")
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "rbac store is not configured")
		return false
	}
	return true
}

func (ctrl *Controller) allowScope(c *gin.Context, permission, scopeType, scopeID string) bool {
	if ctrl.authorizer == nil {
		logging.Error(c.Request.Context(), "allowScope", "authorizer is not configured", logging.F("permission", permission), logging.F("scopeType", scopeType))
		response.Fail(c, http.StatusForbidden, response.CodeForbidden, auth.ErrPermissionDenied.Error())
		return false
	}
	resourceType := strings.TrimSpace(scopeType)
	if resourceType == "" {
		resourceType = "global"
	}
	err := ctrl.authorizer.Allow(c.Request.Context(), auth.UserFromContext(c), permission, auth.Resource{
		Type: resourceType,
		ID:   scopeID,
	})
	if err == nil {
		return true
	}
	if errors.Is(err, postgres.ErrNotFound) {
		logging.Warn(c.Request.Context(), "allowScope", "resource not found", logging.F("scopeType", scopeType), logging.F("scopeId", scopeID))
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, err.Error())
		return false
	}
	if errors.Is(err, auth.ErrPermissionDenied) {
		logging.Warn(c.Request.Context(), "allowScope", "permission denied", logging.F("permission", permission), logging.F("scopeType", scopeType), logging.F("scopeId", scopeID))
		response.Fail(c, http.StatusForbidden, response.CodeForbidden, err.Error())
		return false
	}
	ctrl.write(c, nil, err)
	return false
}

func paginationFromRequest(req PageRequest) postgres.Pagination {
	return postgres.Pagination{PageNum: req.PageNum, PageSize: req.PageSize}.Normalize()
}

func pageData(items any, total int64, pagination postgres.Pagination) PageResp {
	return paginationData(items, total, pagination)
}

func paginationData(items any, total int64, pagination postgres.Pagination) PageResp {
	pagination = pagination.Normalize()
	return PageResp{
		PageNum:  pagination.PageNum,
		PageSize: pagination.PageSize,
		Total:    total,
		List:     items,
	}
}
