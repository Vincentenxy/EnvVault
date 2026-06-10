package controller

import (
	"errors"
	"net/http"
	"reflect"
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
	// 兼容字段:当前按 users.id(UUID) 解析,不再作为外部用户标识参与授权。
	ExternalUserId string     `json:"externalUserId"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	RoleCode       string     `json:"roleCode"`
	ScopeType      string     `json:"scopeType"`
	ScopeId        string     `json:"scopeId"`
	ExpiresAt      *time.Time `json:"expiresAt"`

	// 新字段(SDK 友好);非空时优先于兼容字段。
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
	// 兼容字段:当前按 users.id(UUID) 解析。
	ExternalUserId string `json:"externalUserId"`
	ScopeType      string `json:"scopeType"`
	ScopeId        string `json:"scopeId"`

	// 推荐字段:users.id(UUID)。
	UserId string `json:"userId,omitempty"`
}

type pagedUserLookupRequest struct {
	PageRequest
	// 兼容字段:当前按 users.id(UUID) 解析。
	ExternalUserId string `json:"externalUserId"`

	// 推荐字段:users.id(UUID)。
	UserId string `json:"userId,omitempty"`
}

// pickAlias 在推荐字段优先;空时回退到兼容字段,空字符串走 TrimSpace 防御。
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

// pageData 把 service 层的 PaginatedResult + 请求分页参数打包成 PageResp。
//
// 当 items 为空(nil / 长度 0)时:
//   - pageNum / pageSize 设为 0(由 `omitempty` 省略)→ 响应退化为 {total, list}
//   - list 用反射转换为同类型空 slice,避免 marshaling 出 `null` 而非 `[]`
//
// 非空时正常返回 pageNum / pageSize,供调用方确认服务端归一化后的分页上下文。
// 规则见 `design/DESIGN.md`「分页响应 - 空数据形态」节。
func pageData(items any, total int64, pagination domain.Pagination) PageResp {
	pagination = pagination.Normalize()
	if isEmptyList(items) {
		return PageResp{
			Total: total,
			List:  emptyListOfSameType(items),
		}
	}
	return PageResp{
		PageNum:  pagination.PageNum,
		PageSize: pagination.PageSize,
		Total:    total,
		List:     items,
	}
}

// isEmptyList 判断 items 是否应该走「空数据形态」。
//   - nil 接口 → 空
//   - 任意 slice 类型的 nil 或长度 0 → 空
//   - 其它类型按非空处理(分页响应只可能接 slice,防御性兜底)
func isEmptyList(items any) bool {
	if items == nil {
		return true
	}
	v := reflect.ValueOf(items)
	if v.Kind() != reflect.Slice {
		return false
	}
	return v.Len() == 0
}

// emptyListOfSameType 把 items 转换为同 slice 类型的空 slice(非 nil),
// 保证 `json.Marshal` 出 `[]` 而非 `null`。
//   - items 为 nil 接口 → 返回 `[]any{}`
//   - items 为 nil slice → 用 reflect.MakeSlice 构造同类型空 slice
//   - 其它类型原样返回(防御性兜底)
func emptyListOfSameType(items any) any {
	if items == nil {
		return []any{}
	}
	v := reflect.ValueOf(items)
	if v.Kind() != reflect.Slice {
		return items
	}
	return reflect.MakeSlice(v.Type(), 0, 0).Interface()
}
