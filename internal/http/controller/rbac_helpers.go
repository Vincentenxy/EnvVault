package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
)

// 通用请求体:RBAC 子树共用

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
	// 旧字段(保留兼容)
	ExternalUserId string     `json:"externalUserId"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	RoleCode       string     `json:"roleCode"`
	ScopeType      string     `json:"scopeType"`
	ScopeId        string     `json:"scopeId"`
	ExpiresAt      *time.Time `json:"expiresAt"`

	// 新 alias 字段(SDK 友好);非空时优先于旧字段
	UserId       string `json:"userId,omitempty"`
	RoleType     string `json:"roleType,omitempty"`
	ResourceType string `json:"resourceType,omitempty"`
	ResourceId   string `json:"resourceId,omitempty"`
}

// resolvedRoleGrant 是 roleGrantRequest 经 alias 解析后的扁平结果。
type resolvedRoleGrant struct {
	UserId       string
	Name         string
	Email        string
	RoleCode     string
	ScopeType    string
	ScopeId      string
	ExpiresAt    *time.Time
	HasExpiresAt bool
}

func (r roleGrantRequest) resolve() resolvedRoleGrant {
	return resolvedRoleGrant{
		UserId:       pickAlias(r.UserId, r.ExternalUserId),
		Name:         r.Name,
		Email:        r.Email,
		RoleCode:     pickAlias(r.RoleType, r.RoleCode),
		ScopeType:    pickAlias(r.ResourceType, r.ScopeType),
		ScopeId:      pickAlias(r.ResourceId, r.ScopeId),
		ExpiresAt:    r.ExpiresAt,
		HasExpiresAt: r.ExpiresAt != nil,
	}
}

type userLookupRequest struct {
	ExternalUserId string `json:"externalUserId"`
	ScopeType      string `json:"scopeType"`
	ScopeId        string `json:"scopeId"`

	// alias
	UserId string `json:"userId,omitempty"`
}

type pagedUserLookupRequest struct {
	PageRequest
	ExternalUserId string `json:"externalUserId"`

	// alias
	UserId string `json:"userId,omitempty"`
}

// pickAlias 在 alias 优先;空时回退到旧字段,空字符串走 TrimSpace 防御。
func pickAlias(newVal, oldVal string) string {
	if v := strings.TrimSpace(newVal); v != "" {
		return v
	}
	return strings.TrimSpace(oldVal)
}

// 通用助手

func (ctrl *Controller) ensureRBAC(c *gin.Context) bool {
	if ctrl.rbac == nil {
		logging.Error(c.Request.Context(), "ensureRBAC", "rbac service is not configured")
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "rbac service is not configured")
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
	if errors.Is(err, domain.ErrNotFound) {
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

func paginationFromRequest(req PageRequest) domain.Pagination {
	return domain.Pagination{PageNum: req.PageNum, PageSize: req.PageSize}.Normalize()
}

func pageData(items any, total int64, pagination domain.Pagination) PageResp {
	pagination = pagination.Normalize()
	return PageResp{
		PageNum:  pagination.PageNum,
		PageSize: pagination.PageSize,
		Total:    total,
		List:     items,
	}
}
