package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/logging"
	"envVault/internal/store/postgres"
)

func (ctrl *Controller) CreateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateSecretKey(c, req.Key) {
		return
	}
	ctrl.log(c, "CreateSecret", logging.F("folder_id", req.FolderId), logging.F("key", req.Key), logging.F("value", req.Value))
	ciphertext, err := ctrl.encryptSecret(c, req.Value)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	item, err := ctrl.store.CreateSecret(c.Request.Context(), req.FolderId, req.Key, req.Comment, ctrl.actor(c), ciphertext)
	if err == nil {
		ctrl.cacheSecret(c, item.Id, ciphertext)
	}
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
	ctrl.log(c, "UpdateSecret", logging.F("id", req.Id), logging.F("key", req.Key), logging.F("value", req.Value))
	ciphertext, err := ctrl.encryptSecret(c, req.Value)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	item, err := ctrl.store.UpdateSecret(c.Request.Context(), req.Id, req.Key, req.Comment, ctrl.actor(c), ciphertext)
	if err == nil {
		ctrl.cacheSecret(c, item.Id, ciphertext)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) RevealSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "RevealSecret", logging.F("id", req.Id))
	secret, ciphertext, err := ctrl.store.GetSecretCiphertext(c.Request.Context(), req.Id)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	value, err := ctrl.decryptSecret(c, ciphertext)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if err := ctrl.store.RecordAudit(c.Request.Context(), ctrl.actor(c), "secret", secret.Id, "reveal"); err != nil {
		ctrl.write(c, nil, err)
		return
	}
	secret.Value = value
	ctrl.write(c, secret, nil)
}

func (ctrl *Controller) GetSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "GetSecret", logging.F("id", req.Id))
	item, err := ctrl.store.GetSecret(c.Request.Context(), req.Id)
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
	ctrl.log(c, "ListSecrets", logging.F("org_id", req.OrgId), logging.F("project_id", req.ProjectId), logging.F("environment_id", req.EnvironmentId), logging.F("folder_id", req.FolderId))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgId:         req.OrgId,
		ProjectId:     req.ProjectId,
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
	ctrl.log(c, "SearchSecrets", logging.F("org_id", req.OrgId), logging.F("project_id", req.ProjectId), logging.F("environment_id", req.EnvironmentId), logging.F("folder_id", req.FolderId), logging.F("keyword", req.Keyword))
	filter := postgres.ListFilter{
		OrgId:         req.OrgId,
		ProjectId:     req.ProjectId,
		EnvironmentId: req.EnvironmentId,
		FolderId:      req.FolderId,
		Keyword:       req.Keyword,
	}
	if ctrl.cache != nil {
		items, err := ctrl.cache.SearchSecrets(c.Request.Context(), filter)
		if err == nil {
			pagination := paginationFromRequest(req.PageRequest)
			pagedItems, total := paginateSecrets(items, pagination)
			ctrl.write(c, pageData(pagedItems, total, pagination), nil)
			return
		}
		logging.Warn(c.Request.Context(), "SearchSecrets", "redis search failed, fallback to postgres", logging.F("error", err))
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgId:         req.OrgId,
		ProjectId:     req.ProjectId,
		EnvironmentId: req.EnvironmentId,
		FolderId:      req.FolderId,
		Keyword:       req.Keyword,
	}, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) DeleteSecret(c *gin.Context) {
	ctrl.log(c, "DeleteSecret")
	ctrl.delete(c, func(req idRequest) error {
		err := ctrl.store.DeleteSecret(c.Request.Context(), req.Id, ctrl.actor(c))
		if err == nil && ctrl.cache != nil {
			if cacheErr := ctrl.cache.DeleteSecret(c.Request.Context(), req.Id); cacheErr != nil {
				logging.Warn(c.Request.Context(), "DeleteSecret", "redis delete failed", logging.F("error", cacheErr), logging.F("id", req.Id))
			}
		}
		return err
	})
}
