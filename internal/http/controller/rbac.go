package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/http/response"
	"envVault/internal/store/postgres"
)

type scopeRequest struct {
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	PageNum   int    `json:"pageNum"`
	PageSize  int    `json:"pageSize"`
}

type roleInfoRequest struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

type roleRequest struct {
	ID          string   `json:"id,omitempty"`
	ScopeType   string   `json:"scope_type"`
	ScopeID     string   `json:"scope_id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type roleGrantRequest struct {
	ExternalUserID string     `json:"external_user_id"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	RoleCode       string     `json:"role_code"`
	ScopeType      string     `json:"scope_type"`
	ScopeID        string     `json:"scope_id"`
	ExpiresAt      *time.Time `json:"expires_at"`
}

type userLookupRequest struct {
	ExternalUserID string `json:"external_user_id"`
	ScopeType      string `json:"scope_type"`
	ScopeID        string `json:"scope_id"`
	PageNum        int    `json:"pageNum"`
	PageSize       int    `json:"pageSize"`
}

func (ctrl *Controller) ListPermissions(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	pagination := paginationFromQuery(c)
	result, err := ctrl.rbac.ListPermissions(c.Request.Context(), pagination)
	ctrl.write(c, paginatedData("permissions", result.Items, result.Total, pagination), err)
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
	var req scopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:role:read", req.ScopeType, req.ScopeID) {
		return
	}
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.rbac.ListRoles(c.Request.Context(), req.ScopeType, req.ScopeID, pagination)
	ctrl.write(c, paginatedData("roles", result.Items, result.Total, pagination), err)
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
	var req scopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:read", req.ScopeType, req.ScopeID) {
		return
	}
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.rbac.ListRoleBindings(c.Request.Context(), req.ScopeType, req.ScopeID, pagination)
	ctrl.write(c, paginatedData("bindings", result.Items, result.Total, pagination), err)
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
	user := auth.UserFromContext(c)
	item, err := ctrl.rbac.SyncUser(c.Request.Context(), user.UserId, user.Name, "")
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	pagination := paginationFromQuery(c)
	grants, err := ctrl.rbac.ListUserGrants(c.Request.Context(), user.UserId, pagination)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, paginatedDataWithExtra("grants", grants.Items, grants.Total, pagination, gin.H{"user": item}), nil)
}

func (ctrl *Controller) ListRBACUsers(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req scopeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:read", req.ScopeType, req.ScopeID) {
		return
	}
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.rbac.ListUsers(c.Request.Context(), req.ScopeType, req.ScopeID, pagination)
	ctrl.write(c, paginatedData("users", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) ListUserGrants(c *gin.Context) {
	if !ctrl.ensureRBAC(c) {
		return
	}
	var req userLookupRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "rbac:binding:read", "global", "") {
		return
	}
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.rbac.ListUserGrants(c.Request.Context(), req.ExternalUserID, pagination)
	ctrl.write(c, paginatedData("grants", result.Items, result.Total, pagination), err)
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
		response.Fail(c, http.StatusServiceUnavailable, 1001, "rbac store is not configured")
		return false
	}
	return true
}

func (ctrl *Controller) allowScope(c *gin.Context, permission, scopeType, scopeID string) bool {
	if ctrl.authorizer == nil {
		response.Fail(c, http.StatusForbidden, 1403, auth.ErrPermissionDenied.Error())
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
		response.Fail(c, http.StatusNotFound, 1404, err.Error())
		return false
	}
	if errors.Is(err, auth.ErrPermissionDenied) {
		response.Fail(c, http.StatusForbidden, 1403, err.Error())
		return false
	}
	ctrl.write(c, nil, err)
	return false
}

func paginationFromQuery(c *gin.Context) postgres.Pagination {
	pageNum, _ := strconv.Atoi(c.DefaultQuery("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	return paginationFromRequest(pageNum, pageSize)
}

func paginationFromRequest(pageNum, pageSize int) postgres.Pagination {
	return postgres.Pagination{PageNum: pageNum, PageSize: pageSize}.Normalize()
}

func paginatedData(key string, items any, total int64, pagination postgres.Pagination) gin.H {
	return paginatedDataWithExtra(key, items, total, pagination, nil)
}

func paginatedDataWithExtra(key string, items any, total int64, pagination postgres.Pagination, extra gin.H) gin.H {
	data := paginationData(pagination, total)
	for extraKey, extraValue := range extra {
		data[extraKey] = extraValue
	}
	data[key] = items
	return data
}

func paginationData(pagination postgres.Pagination, total int64) gin.H {
	pagination = pagination.Normalize()
	return gin.H{
		"pageNum":  pagination.PageNum,
		"pageSize": pagination.PageSize,
		"total":    total,
	}
}
