package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
)

type secretPathRequest struct {
	Path string `json:"path"`
}

func (ctrl *Controller) CreateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateSecretKey(c, req.Key) {
		return
	}
	if !ctrl.allowScope(c, "secret:create", "folder", req.FolderId) {
		return
	}
	ctrl.log(c, "CreateSecret", logging.F("folder_id", req.FolderId), logging.F("key", req.Key))
	item, err := ctrl.secret.Create(c.Request.Context(), req.FolderId, req.Key, req.Value, req.Comment, ctrl.actor(c))
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
	if !ctrl.allowScope(c, "secret:update", "secret", req.Id) {
		return
	}
	ctrl.log(c, "UpdateSecret", logging.F("id", req.Id), logging.F("key", req.Key))
	item, err := ctrl.secret.Update(c.Request.Context(), req.Id, req.Key, req.Value, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) RevealSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !ctrl.allowScope(c, "secret:reveal", "secret", req.Id) {
		return
	}
	ctrl.log(c, "RevealSecret", logging.F("id", req.Id))
	secret, err := ctrl.secret.Reveal(c.Request.Context(), req.Id, ctrl.actor(c))
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
	if !ctrl.allowScope(c, "secret:read", "secret", req.Id) {
		return
	}
	ctrl.log(c, "GetSecret", logging.F("id", req.Id))
	item, err := ctrl.secret.Get(c.Request.Context(), req.Id)
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
	if !ctrl.secretListAllowScope(c, req) {
		return
	}
	ctrl.log(c, "ListSecrets", logging.F("environment_id", req.EnvironmentId), logging.F("folder_id", req.FolderId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.secret.List(c.Request.Context(), domain.ListFilter{
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
	if !ctrl.secretListAllowScope(c, req) {
		return
	}
	ctrl.log(c, "SearchSecrets", logging.F("environment_id", req.EnvironmentId), logging.F("folder_id", req.FolderId), logging.F("keyword", req.Keyword))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.secret.Search(c.Request.Context(), domain.ListFilter{
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
	if !ctrl.allowScope(c, "secret:delete", "secret", req.Id) {
		return
	}
	ctrl.log(c, "DeleteSecret", logging.F("id", req.Id))
	ctrl.write(c, gin.H{"deleted": true}, ctrl.secret.Delete(c.Request.Context(), req.Id, ctrl.actor(c)))
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
	// 先解析拿 id,再走 secret:read 校验(沿 secret 继承链 org→project→env→folder)。
	item, err := ctrl.secret.GetByPath(c.Request.Context(), req.Path)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if !ctrl.allowScope(c, "secret:read", "secret", item.Id) {
		return
	}
	ctrl.write(c, item, nil)
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
	item, err := ctrl.secret.GetByPath(c.Request.Context(), req.Path)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if !ctrl.allowScope(c, "secret:reveal", "secret", item.Id) {
		return
	}
	secret, err := ctrl.secret.Reveal(c.Request.Context(), item.Id, ctrl.actor(c))
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	ctrl.write(c, secret, nil)
}

// secretListAllowScope 实现 ListSecrets / SearchSecrets 的"最深 scope"策略:
// FolderId 优先 → folder scope;否则 EnvironmentId → environment scope。
// RBAC 走 secret scope 继承链(folder → env → project → org),由 ResourceScopes 自行展开。
func (ctrl *Controller) secretListAllowScope(c *gin.Context, req listRequest) bool {
	if strings.TrimSpace(req.FolderId) != "" {
		return ctrl.allowScope(c, "secret:list", "folder", req.FolderId)
	}
	if strings.TrimSpace(req.EnvironmentId) != "" {
		return ctrl.allowScope(c, "secret:list", "environment", req.EnvironmentId)
	}
	// 校验已经保证至少有一个非空,这里走不到。
	response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "environmentId or folderId is required")
	return false
}
