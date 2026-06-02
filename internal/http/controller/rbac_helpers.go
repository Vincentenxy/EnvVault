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
	ScopeId   string `json:"scopeId"`
}

type pageScopeRequest struct {
	PageRequest
	ScopeType string `json:"scopeType"`
	ScopeId   string `json:"scopeId"`
}

type roleInfoRequest struct {
	Id   string `json:"id"`
	Code string `json:"code"`
}

type roleRequest struct {
	Id          string   `json:"id,omitempty"`
	ScopeType   string   `json:"scopeType"`
	ScopeId     string   `json:"scopeId"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type roleGrantRequest struct {
	ExternalUserId string     `json:"externalUserId"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	RoleCode       string     `json:"roleCode"`
	ScopeType      string     `json:"scopeType"`
	ScopeId        string     `json:"scopeId"`
	ExpiresAt      *time.Time `json:"expiresAt"`
}

type userLookupRequest struct {
	ExternalUserId string `json:"externalUserId"`
	ScopeType      string `json:"scopeType"`
	ScopeId        string `json:"scopeId"`
}

type pagedUserLookupRequest struct {
	PageRequest
	ExternalUserId string `json:"externalUserId"`
}

func (ctrl *Controller) ensureRBAC(c *gin.Context) bool {
	if ctrl.rbac == nil {
		logging.Error(c.Request.Context(), "ensureRBAC", "rbac store is not configured")
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "rbac store is not configured")
		return false
	}
	return true
}

func (ctrl *Controller) allowScope(c *gin.Context, permission, scopeType, scopeId string) bool {
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
		Id:   scopeId,
	})
	if err == nil {
		return true
	}
	if errors.Is(err, postgres.ErrNotFound) {
		logging.Warn(c.Request.Context(), "allowScope", "resource not found", logging.F("scopeType", scopeType), logging.F("scopeId", scopeId))
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, err.Error())
		return false
	}
	if errors.Is(err, auth.ErrPermissionDenied) {
		logging.Warn(c.Request.Context(), "allowScope", "permission denied", logging.F("permission", permission), logging.F("scopeType", scopeType), logging.F("scopeId", scopeId))
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
