package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
)

type secretPathRequest struct {
	Path string `json:"path"`
}

type secretPathBatchRevealRequest struct {
	Path string   `json:"path"`
	Keys []string `json:"keys,omitempty"`
}

type secretCodeBatchRevealRequest struct {
	OrgCode         string `json:"orgCode"`
	ProjectCode     string `json:"projectCode"`
	EnvironmentCode string `json:"environmentCode"`
	FolderCode      string `json:"folderCode"`
}

func (ctrl *Controller) CreateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateSecretKey(c, req.Key) {
		return
	}
	ctrl.log(c, "CreateSecret", logging.F("folder_id", req.FolderId), logging.F("key", req.Key))
	user := auth.UserFromContext(c)
	item, err := ctrl.secret.Create(c.Request.Context(), user, req.FolderId, req.Key, req.Value, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateSecretKey(c, req.Key) {
		return
	}
	ctrl.log(c, "UpdateSecret", logging.F("id", req.Id), logging.F("key", req.Key))
	user := auth.UserFromContext(c)
	item, err := ctrl.secret.Update(c.Request.Context(), user, req.Id, req.Key, req.Value, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) RevealSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "RevealSecret", logging.F("id", req.Id))
	user := auth.UserFromContext(c)
	secret, err := ctrl.secret.Reveal(c.Request.Context(), user, req.Id, ctrl.actor(c))
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, secret, nil)
}

func (ctrl *Controller) GetSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "GetSecret", logging.F("id", req.Id))
	user := auth.UserFromContext(c)
	item, err := ctrl.secret.Get(c.Request.Context(), user, req.Id)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListSecrets(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListSecrets(c, req) {
		return
	}
	ctrl.log(c, "ListSecrets",
		logging.F("project_id", req.ProjectId),
		logging.F("environment_id", req.EnvironmentId),
		logging.F("folder_id", req.FolderId),
		logging.F("keyword", req.Keyword))
	user := auth.UserFromContext(c)
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.secret.List(c.Request.Context(), user, domain.ListFilter{
		ProjectId:     req.ProjectId,
		EnvironmentId: req.EnvironmentId,
		FolderId:      req.FolderId,
		Keyword:       req.Keyword,
	}, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) SearchSecrets(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateSearchSecrets(c, req) {
		return
	}
	ctrl.log(c, "SearchSecrets",
		logging.F("project_id", req.ProjectId),
		logging.F("environment_id", req.EnvironmentId),
		logging.F("folder_id", req.FolderId),
		logging.F("keyword", req.Keyword))
	user := auth.UserFromContext(c)
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.secret.Search(c.Request.Context(), user, domain.ListFilter{
		ProjectId:     req.ProjectId,
		EnvironmentId: req.EnvironmentId,
		FolderId:      req.FolderId,
		Keyword:       req.Keyword,
	}, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) DeleteSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "DeleteSecret", logging.F("id", req.Id))
	user := auth.UserFromContext(c)
	ctrl.write(c, gin.H{"deleted": true}, ctrl.secret.Delete(c.Request.Context(), user, req.Id, ctrl.actor(c)))
}

func (ctrl *Controller) GetSecretByPath(c *gin.Context) {
	var req secretPathRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "path is required")
		return
	}
	user := auth.UserFromContext(c)
	item, err := ctrl.secret.GetByPath(c.Request.Context(), user, req.Path)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) RevealSecretByPath(c *gin.Context) {
	var req secretPathRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "path is required")
		return
	}
	user := auth.UserFromContext(c)
	secret, err := ctrl.secret.RevealByPath(c.Request.Context(), user, req.Path, ctrl.actor(c))
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, secret, nil)
}

// BatchRevealSecretByPath 批量按 folder 路径 + 可选 keys 列表 reveal。
// 请求体: { "path": "o1.p1.dev.globals", "keys": ["DATABASE_URL", "API_KEY"] }
// 响应: { "path": ..., "list": [Secret...], "notFound": [...] }
// keys 缺省/空数组 = 该 folder 下所有 secret(无分页)。
func (ctrl *Controller) BatchRevealSecretByPath(c *gin.Context) {
	var req secretPathBatchRevealRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "path is required")
		return
	}
	ctrl.log(c, "BatchRevealSecretByPath", logging.F("path", req.Path), logging.F("keys_count", len(req.Keys)))
	user := auth.UserFromContext(c)
	items, notFound, err := ctrl.secret.BatchRevealByPath(c.Request.Context(), user, req.Path, req.Keys, ctrl.actor(c))
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, gin.H{
		"path":     req.Path,
		"list":     items,
		"notFound": notFound,
	}, nil)
}

// BatchRevealSecretByCode 按 4 级 code 批量 reveal folder 下所有 secret。
// 请求体: { "orgCode":"...", "projectCode":"...", "environmentCode":"...", "folderCode":"..." }
// 响应:   { "secretList":[Secret...] }
// 与 /secret/path/batchReveal 等价(永远 keys=nil),只是入参用结构化 code 替代字符串 path。
func (ctrl *Controller) BatchRevealSecretByCode(c *gin.Context) {
	var req secretCodeBatchRevealRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if strings.TrimSpace(req.OrgCode) == "" ||
		strings.TrimSpace(req.ProjectCode) == "" ||
		strings.TrimSpace(req.EnvironmentCode) == "" ||
		strings.TrimSpace(req.FolderCode) == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "orgCode, projectCode, environmentCode, folderCode are all required")
		return
	}
	ctrl.log(c, "BatchRevealSecretByCode",
		logging.F("org_code", req.OrgCode),
		logging.F("project_code", req.ProjectCode),
		logging.F("environment_code", req.EnvironmentCode),
		logging.F("folder_code", req.FolderCode))
	user := auth.UserFromContext(c)
	items, err := ctrl.secret.BatchRevealByCode(c.Request.Context(), user,
		req.OrgCode, req.ProjectCode, req.EnvironmentCode, req.FolderCode, ctrl.actor(c))
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, gin.H{
		"secretList": items,
	}, nil)
}

type secretListAcrossEnvsRequest struct {
	ProjectId  string   `json:"projectId"`
	FolderCode string   `json:"folderCode,omitempty"`
	Key        string   `json:"key,omitempty"`
	EnvList    []string `json:"envList"`
}

// ListSecretsAcrossEnvs 按 (projectId, [folderCode], [key]) 跨 envList 一次性 reveal。
// 请求体: { "projectId":"...", "folderCode":"...", "key":"...", "envList":["dev","test",...] }
//   - key 非空:精确查 (folderCode, key) 跨 envList,folderCode 必填
//   - key 为空:列项目下所有 (folder, key) 跨 envList,folderCode 忽略
//
// 响应: data 永远是 SecretAcrossEnvs 数组(1 元素或 N 元素,无命中可能为空数组)
// 响应里不携带 projectId / folderCode(不回显请求参数)。
func (ctrl *Controller) ListSecretsAcrossEnvs(c *gin.Context) {
	var req secretListAcrossEnvsRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if strings.TrimSpace(req.ProjectId) == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest,
			"projectId is required")
		return
	}
	// key 非空时校验格式 + 校验 folderCode 必填(key 空时 folderCode 允许为空)
	if strings.TrimSpace(req.Key) != "" {
		if !validateSecretKey(c, req.Key) {
			return
		}
		if strings.TrimSpace(req.FolderCode) == "" {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest,
				"folderCode is required when key is provided")
			return
		}
	}
	if len(req.EnvList) == 0 {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest,
			"envList is required and must contain at least one env code")
		return
	}
	ctrl.log(c, "ListSecretsAcrossEnvs",
		logging.F("project_id", req.ProjectId),
		logging.F("folder_code", req.FolderCode),
		logging.F("key", req.Key),
		logging.F("env_count", len(req.EnvList)))
	user := auth.UserFromContext(c)
	data, err := ctrl.secret.ListAcrossEnvs(c.Request.Context(), user,
		req.ProjectId, req.FolderCode, req.Key, req.EnvList, ctrl.actor(c))
	ctrl.write(c, data, err)
}
