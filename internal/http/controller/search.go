package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/http/response"
	"envVault/internal/logging"
	"envVault/internal/store/redis"
)

// globalSearchRequest 全局搜索入参。
//   - keyword:必填,子串匹配 code/name/comment/value
//   - types:可选,限定搜索的 5 类资源子集,默认全选
//   - limit:每类上限,默认 50,最大 200
type globalSearchRequest struct {
	Keyword string   `json:"keyword"`
	Types   []string `json:"types,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

// GlobalSearch 跨 org/project/env/folder/secret 5 类资源做全局搜索。
// 匹配字段:code / name / comment(全 5 类) + secret value(解密后子串匹配)。
// 走 Redis 并发 5 协程扫描,不做 DB 命中,失联返回空。
// RBAC:对每条候选做对应 :read 权限过滤,只返回当前用户能看到的。
func (ctrl *Controller) GlobalSearch(c *gin.Context) {
	var req globalSearchRequest
	if !ctrl.bind(c, &req) {
		return
	}
	keyword := strings.TrimSpace(req.Keyword)
	if keyword == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "keyword is required")
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	requestedTypes := normalizeSearchTypes(req.Types)

	ctrl.log(c, "GlobalSearch", logging.F("keyword", keyword), logging.F("types", requestedTypes), logging.F("limit", limit))
	result, err := ctrl.cache.GlobalSearch(c.Request.Context(), keyword, limit)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}

	// 按请求的 types 过滤 + RBAC 过滤
	filtered := redis.GlobalSearchResult{}
	if containsType(requestedTypes, "org") {
		filtered.Orgs = ctrl.filterOrgHits(c, result.Orgs)
	}
	if containsType(requestedTypes, "project") {
		filtered.Projects = ctrl.filterProjectHits(c, result.Projects)
	}
	if containsType(requestedTypes, "env") {
		filtered.Envs = ctrl.filterEnvHits(c, result.Envs)
	}
	if containsType(requestedTypes, "folder") {
		filtered.Folders = ctrl.filterFolderHits(c, result.Folders)
	}
	if containsType(requestedTypes, "secret") {
		filtered.Secrets = ctrl.filterSecretHits(c, result.Secrets)
	}
	ctrl.write(c, filtered, nil)
}

// normalizeSearchTypes 接受客户端传的 types,空数组 = 5 类全选;未知值丢弃。
func normalizeSearchTypes(input []string) []string {
	allowed := map[string]bool{
		"org": true, "project": true, "env": true, "folder": true, "secret": true,
	}
	if len(input) == 0 {
		return []string{"org", "project", "env", "folder", "secret"}
	}
	out := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, t := range input {
		if allowed[t] && !seen[t] {
			out = append(out, t)
			seen[t] = true
		}
	}
	return out
}

func containsType(types []string, t string) bool {
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

// 每类一条 RBAC 过滤函数:无权限的丢弃。
// 用顺序遍历,候选量小(limit 上限 200)时无性能压力。

func (ctrl *Controller) filterOrgHits(c *gin.Context, hits []redis.GlobalSearchHit) []redis.GlobalSearchHit {
	out := make([]redis.GlobalSearchHit, 0, len(hits))
	for _, h := range hits {
		if ctrl.allowScope(c, "org:read", "organization", h.Id) {
			out = append(out, h)
		}
	}
	return out
}

func (ctrl *Controller) filterProjectHits(c *gin.Context, hits []redis.GlobalSearchHit) []redis.GlobalSearchHit {
	out := make([]redis.GlobalSearchHit, 0, len(hits))
	for _, h := range hits {
		if ctrl.allowScope(c, "project:read", "project", h.Id) {
			out = append(out, h)
		}
	}
	return out
}

func (ctrl *Controller) filterEnvHits(c *gin.Context, hits []redis.GlobalSearchHit) []redis.GlobalSearchHit {
	out := make([]redis.GlobalSearchHit, 0, len(hits))
	for _, h := range hits {
		if ctrl.allowScope(c, "env:read", "environment", h.Id) {
			out = append(out, h)
		}
	}
	return out
}

func (ctrl *Controller) filterFolderHits(c *gin.Context, hits []redis.GlobalSearchHit) []redis.GlobalSearchHit {
	out := make([]redis.GlobalSearchHit, 0, len(hits))
	for _, h := range hits {
		if ctrl.allowScope(c, "folder:read", "folder", h.Id) {
			out = append(out, h)
		}
	}
	return out
}

func (ctrl *Controller) filterSecretHits(c *gin.Context, hits []redis.GlobalSearchHit) []redis.GlobalSearchHit {
	out := make([]redis.GlobalSearchHit, 0, len(hits))
	for _, h := range hits {
		// secret RBAC 走级联链(folder → env → project → org),由 ResourceScopes 自行展开
		if ctrl.allowScope(c, "secret:read", "secret", h.Id) {
			out = append(out, h)
		}
	}
	return out
}
