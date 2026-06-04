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
	ctrl.log(c, "ListSecrets", logging.F("environment_id", req.EnvironmentId), logging.F("folder_id", req.FolderId))
	user := auth.UserFromContext(c)
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.secret.List(c.Request.Context(), user, domain.ListFilter{
		EnvironmentId: req.EnvironmentId,
		FolderId:      req.FolderId,
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
	ctrl.log(c, "SearchSecrets", logging.F("environment_id", req.EnvironmentId), logging.F("folder_id", req.FolderId), logging.F("keyword", req.Keyword))
	user := auth.UserFromContext(c)
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.secret.Search(c.Request.Context(), user, domain.ListFilter{
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
