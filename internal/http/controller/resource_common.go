package controller

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
)

type Entity = domain.Entity

type createEntityRequest struct {
	ParentId     string           `json:"parentId,omitempty"`
	Code         string           `json:"code"`
	Name         string           `json:"name"`
	Comment      string           `json:"comment"`
	Environments []EnvSpecRequest `json:"environments,omitempty"`
}

// EnvSpecRequest 在创建 project/env 时,描述一个 env 的最小信息。
type EnvSpecRequest struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Comment   string `json:"comment"`
	SortOrder int    `json:"sortOrder,omitempty"`
}

type idOrCodeRequest struct {
	ParentId string `json:"parentId,omitempty"`
	Id       string `json:"id,omitempty"`
	Code     string `json:"code,omitempty"`
}

type updateByIdOrCodeRequest struct {
	ParentId string `json:"parentId,omitempty"`
	Id       string `json:"id,omitempty"`
	Code     string `json:"code,omitempty"`
	Name     string `json:"name"`
	Comment  string `json:"comment"`
}

type idRequest struct {
	Id string `json:"id"`
}

type updateEntityRequest struct {
	Id      string `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

type listRequest struct {
	PageRequest
	OrgId          string `json:"orgId,omitempty"`
	ProjectId      string `json:"projectId,omitempty"`
	EnvironmentId  string `json:"environmentId,omitempty"`
	FolderId       string `json:"folderId,omitempty"`
	FolderParentId string `json:"folderParentId,omitempty"`
	ResourceType   string `json:"resourceType,omitempty"`
	ResourceId     string `json:"resourceId,omitempty"`
	Keyword        string `json:"keyword,omitempty"`
}

type PageRequest struct {
	PageNum  int `json:"pageNum"`
	PageSize int `json:"pageSize"`
}

// PageResp 是 list 接口的统一分页响应载体。
//
// 当 list 为空(即本次查询无数据返回)时,pageNum / pageSize 字段会被 `pageData`
// 设为 0 并由 `omitempty` 省略;响应体退化为 `{total: 0, list: []}`,避免误导
// 调用方「我请求了 page 1,size 20 怎么回我的 pageNum 字段不见了」。
// 详细规则见 `design/DESIGN.md`「分页响应 - 空数据形态」节。
type PageResp struct {
	PageNum  int   `json:"pageNum,omitempty"`
	PageSize int   `json:"pageSize,omitempty"`
	Total    int64 `json:"total"`
	List     any   `json:"list"`
}

type secretRequest struct {
	Id       string `json:"id,omitempty"`
	FolderId string `json:"folderId,omitempty"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Comment  string `json:"comment"`
}

var (
	codePattern      = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	secretKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// bind 将请求体绑定到 target，并在失败时直接写回错误响应。
// 任一底层依赖为 nil 都视作未配置(503)。
func (ctrl *Controller) bind(c *gin.Context, target any) bool {
	if ctrl.repo == nil || ctrl.secret == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "services are not configured")
		logging.Error(c.Request.Context(), "bind", "services are not configured")
		return false
	}
	if err := c.ShouldBindJSON(target); err != nil {
		logging.Error(c.Request.Context(), "bind", "invalid request body", logging.F("error", err))
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, err.Error())
		return false
	}
	return true
}

// write 统一写入响应:成功直接返回 data;错误时根据错误类型映射到不同的 HTTP 状态码。
func (ctrl *Controller) write(c *gin.Context, data any, err error) {
	if err == nil {
		response.OK(c, data)
		return
	}
	logging.Error(c.Request.Context(), "controller.write", "request failed", logging.F("error", err))
	if errors.Is(err, auth.ErrPermissionDenied) {
		response.Fail(c, http.StatusForbidden, response.CodeForbidden, err.Error())
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, err.Error())
		return
	}
	if errors.Is(err, domain.ErrConflict) {
		response.Fail(c, http.StatusConflict, response.CodeConflict, err.Error())
		return
	}
	response.FailWithMsg(c, err.Error())
}

// delete 是删除接口的通用包装:绑定 idRequest 后调用 fn 执行真正的删除逻辑。
func (ctrl *Controller) delete(c *gin.Context, fn func(idRequest) error) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.write(c, gin.H{"deleted": true}, fn(req))
}

// actor 返回当前请求的操作者用户 ID,并顺便把用户标签缓存起来。
// 缓存预热失败不影响主流程,后续 audit 渲染会按需兜底。
func (ctrl *Controller) actor(c *gin.Context) string {
	user := auth.UserFromContext(c)
	if ctrl.repo != nil {
		ctrl.repo.CacheUserLabel(user.UserId, user.Name)
	}
	return user.UserId
}

// log 是 handler 入口的统一访问日志。
func (ctrl *Controller) log(c *gin.Context, method string, fields ...logging.Field) {
	logging.Info(c.Request.Context(), method, "handler called", fields...)
}

func validateCode(c *gin.Context, code string) bool {
	if !codePattern.MatchString(code) {
		logging.Warn(c.Request.Context(), "validateCode", "invalid code format", logging.F("code", code))
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "code must match ^[a-z0-9]+(-[a-z0-9]+)*$")
		return false
	}
	return true
}

func validateIdOrCode(c *gin.Context, req idOrCodeRequest, resourceType string) bool {
	if req.Id == "" && req.Code == "" {
		logging.Warn(c.Request.Context(), "validateIdOrCode", resourceType+" id or code is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id or code is required")
		return false
	}
	// id 与 code 同时给时,id 优先,code 忽略;由 resolveIdOrCode 在 handler 中落地。
	return true
}

func validateUpdateIdOrCode(c *gin.Context, req updateByIdOrCodeRequest, resourceType string) bool {
	if req.Id == "" && req.Code == "" {
		logging.Warn(c.Request.Context(), "validateUpdateIdOrCode", resourceType+" id or code is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id or code is required")
		return false
	}
	// id 与 code 同时给时,id 优先,code 忽略;由 resolveIdOrCode 在 handler 中落地。
	return true
}

// resolveIdOrCode 决定 handler 实际用哪个字段定位资源。
// 规则:id 优先,code 仅在 id 缺失时使用,否则被忽略——code 不可改,只用于 lookup。
// 校验器 validateIdOrCode / validateUpdateIdOrCode 已保证至少一个非空。
// 返回 lookupId 与 useCode:
//   - lookupId 非空时,handler 直接用 lookupId 走更新/删除;
//   - useCode 为 true 时,handler 用 code 二次查询得到 id,再继续。
func resolveIdOrCode(id, code string) (lookupId string, useCode bool) {
	if id != "" {
		return id, false
	}
	return "", true
}

func validateSecretKey(c *gin.Context, key string) bool {
	if !secretKeyPattern.MatchString(key) {
		logging.Warn(c.Request.Context(), "validateSecretKey", "invalid secret key format", logging.F("key", key))
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "key must match ^[A-Z][A-Z0-9_]*$")
		return false
	}
	return true
}

func validateListProjects(c *gin.Context, req listRequest) bool {
	if req.OrgId == "" {
		logging.Warn(c.Request.Context(), "validateListProjects", "orgId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "orgId is required")
		return false
	}
	return true
}

func validateListEnvironments(c *gin.Context, req listRequest) bool {
	if req.ProjectId == "" {
		logging.Warn(c.Request.Context(), "validateListEnvironments", "projectId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "projectId is required")
		return false
	}
	return true
}

func validateListEnvironmentTemplates(c *gin.Context, req listRequest) bool {
	if req.OrgId == "" {
		logging.Warn(c.Request.Context(), "validateListEnvironmentTemplates", "orgId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "orgId is required")
		return false
	}
	return true
}

func validateListFolders(c *gin.Context, req listRequest) bool {
	envId := strings.TrimSpace(req.EnvironmentId)
	parentId := strings.TrimSpace(req.FolderParentId)
	if envId == "" && parentId == "" {
		logging.Warn(c.Request.Context(), "validateListFolders", "environmentId or folderParentId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "environmentId or folderParentId is required")
		return false
	}
	if envId != "" && parentId != "" {
		logging.Warn(c.Request.Context(), "validateListFolders", "environmentId and folderParentId are mutually exclusive")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "environmentId and folderParentId are mutually exclusive")
		return false
	}
	return true
}

// validateListFoldersIncludeSubfolders 校验 includeSubfolders 开关只在 environmentId
// 模式生效。当前 schema 只到 level=2,parent(folder) 模式返回的是 level=2 列表,
// 再嵌一层 level=3 子 folder 没数据,前端也用不上,直接 400 拒绝。
func validateListFoldersIncludeSubfolders(c *gin.Context, req folderListRequest) bool {
	if !req.IncludeSubfolders {
		return true
	}
	envId := strings.TrimSpace(req.EnvironmentId)
	if envId == "" {
		logging.Warn(c.Request.Context(), "validateListFoldersIncludeSubfolders", "includeSubfolders only valid with environmentId")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "includeSubfolders only valid with environmentId")
		return false
	}
	return true
}

// validateListSecrets 与 validateSearchSecrets 行为对齐:所有 scope 字段都可选,
// 唯一动作是 trim 空白字符。service 层会按 folder > env > project 优先级收敛。
// 保留 "ListSecrets" 这个 controller 入口,纯粹是为了不破坏既有路由 ——
// 实际行为已经和 SearchSecrets 共享同一个 ListFilter + 同一种 repo 查询,
// 区别只在 service 走的是 secret:list action(无 values 字段)。
func validateListSecrets(c *gin.Context, req listRequest) bool {
	req.Keyword = strings.TrimSpace(req.Keyword)
	req.ProjectId = strings.TrimSpace(req.ProjectId)
	req.EnvironmentId = strings.TrimSpace(req.EnvironmentId)
	req.FolderId = strings.TrimSpace(req.FolderId)
	return true
}

// validateSearchSecrets 现在所有 scope 字段都可选:keyword 空=scope 内全量,scope
// 全空=走 RBAC 收窄后的全量。优先级(folder > env > project)由 service 层负责。
// 唯一保留的检查是 keyword 空白字符 trim——纯空格 keyword 等同于空 keyword。
func validateSearchSecrets(c *gin.Context, req listRequest) bool {
	req.Keyword = strings.TrimSpace(req.Keyword)
	req.ProjectId = strings.TrimSpace(req.ProjectId)
	req.EnvironmentId = strings.TrimSpace(req.EnvironmentId)
	req.FolderId = strings.TrimSpace(req.FolderId)
	return true
}
