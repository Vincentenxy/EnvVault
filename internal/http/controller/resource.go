package controller

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	secretcrypto "envVault/internal/crypto"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	"envVault/internal/store/postgres"
)

type createEntityRequest struct {
	ParentID string `json:"parent_id,omitempty"`
	Name     string `json:"name"`
	Comment  string `json:"comment"`
}

type idRequest struct {
	ID string `json:"id"`
}

type updateEntityRequest struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

type listRequest struct {
	OrgID         string `json:"org_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	FolderID      string `json:"folder_id,omitempty"`
	ResourceType  string `json:"resource_type,omitempty"`
	ResourceID    string `json:"resource_id,omitempty"`
	Keyword       string `json:"keyword,omitempty"`
	PageNum       int    `json:"pageNum"`
	PageSize      int    `json:"pageSize"`
}

type secretRequest struct {
	ID       string `json:"id,omitempty"`
	FolderID string `json:"folder_id,omitempty"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Comment  string `json:"comment"`
}

func (ctrl *Controller) CreateOrganization(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "CreateOrganization", logging.F("name", req.Name))
	item, err := ctrl.store.CreateOrganization(c.Request.Context(), req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListOrganizations(c *gin.Context) {
	ctrl.log(c, "ListOrganizations")
	pagination := paginationFromQuery(c)
	result, err := ctrl.store.ListOrganizations(c.Request.Context(), pagination)
	ctrl.write(c, paginatedData("organizations", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetOrganization(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "GetOrganization", logging.F("id", req.ID))
	item, err := ctrl.store.GetOrganization(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateOrganization(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "UpdateOrganization", logging.F("id", req.ID), logging.F("name", req.Name))
	item, err := ctrl.store.UpdateOrganization(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteOrganization(c *gin.Context) {
	ctrl.log(c, "DeleteOrganization")
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteOrganization(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateProject(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "CreateProject", logging.F("org_id", req.ParentID), logging.F("name", req.Name))
	item, err := ctrl.store.CreateProject(c.Request.Context(), req.ParentID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListProjects(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListProjects", logging.F("org_id", req.OrgID))
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.store.ListProjects(c.Request.Context(), req.OrgID, pagination)
	ctrl.write(c, paginatedData("projects", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetProject(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "GetProject", logging.F("id", req.ID))
	item, err := ctrl.store.GetProject(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateProject(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "UpdateProject", logging.F("id", req.ID), logging.F("name", req.Name))
	item, err := ctrl.store.UpdateProject(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteProject(c *gin.Context) {
	ctrl.log(c, "DeleteProject")
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteProject(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateEnvironment(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "CreateEnvironment", logging.F("project_id", req.ParentID), logging.F("name", req.Name))
	item, err := ctrl.store.CreateEnvironment(c.Request.Context(), req.ParentID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListEnvironments(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListEnvironments", logging.F("project_id", req.ProjectID))
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.store.ListEnvironments(c.Request.Context(), req.ProjectID, pagination)
	ctrl.write(c, paginatedData("environments", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetEnvironment(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "GetEnvironment", logging.F("id", req.ID))
	item, err := ctrl.store.GetEnvironment(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateEnvironment(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "UpdateEnvironment", logging.F("id", req.ID), logging.F("name", req.Name))
	item, err := ctrl.store.UpdateEnvironment(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteEnvironment(c *gin.Context) {
	ctrl.log(c, "DeleteEnvironment")
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteEnvironment(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateFolder(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "CreateFolder", logging.F("environment_id", req.ParentID), logging.F("name", req.Name))
	item, err := ctrl.store.CreateFolder(c.Request.Context(), req.ParentID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListFolders(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListFolders", logging.F("environment_id", req.EnvironmentID))
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.store.ListFolders(c.Request.Context(), req.EnvironmentID, pagination)
	ctrl.write(c, paginatedData("folders", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetFolder(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "GetFolder", logging.F("id", req.ID))
	item, err := ctrl.store.GetFolder(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateFolder(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "UpdateFolder", logging.F("id", req.ID), logging.F("name", req.Name))
	item, err := ctrl.store.UpdateFolder(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteFolder(c *gin.Context) {
	ctrl.log(c, "DeleteFolder")
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteFolder(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "CreateSecret", logging.F("folder_id", req.FolderID), logging.F("key", req.Key), logging.F("value", req.Value))
	ciphertext, err := ctrl.encryptSecret(c, req.Value)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	item, err := ctrl.store.CreateSecret(c.Request.Context(), req.FolderID, req.Key, req.Comment, ctrl.actor(c), ciphertext)
	if err == nil {
		ctrl.cacheSecret(c, item.ID, ciphertext)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "UpdateSecret", logging.F("id", req.ID), logging.F("key", req.Key), logging.F("value", req.Value))
	ciphertext, err := ctrl.encryptSecret(c, req.Value)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	item, err := ctrl.store.UpdateSecret(c.Request.Context(), req.ID, req.Key, req.Comment, ctrl.actor(c), ciphertext)
	if err == nil {
		ctrl.cacheSecret(c, item.ID, ciphertext)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) RevealSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "RevealSecret", logging.F("id", req.ID))
	secret, ciphertext, err := ctrl.store.GetSecretCiphertext(c.Request.Context(), req.ID)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	value, err := ctrl.decryptSecret(c, ciphertext)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	if err := ctrl.store.RecordAudit(c.Request.Context(), ctrl.actor(c), "secret", secret.ID, "reveal"); err != nil {
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
	ctrl.log(c, "GetSecret", logging.F("id", req.ID))
	item, err := ctrl.store.GetSecret(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListSecrets(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListSecrets", logging.F("org_id", req.OrgID), logging.F("project_id", req.ProjectID), logging.F("environment_id", req.EnvironmentID), logging.F("folder_id", req.FolderID))
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgID:         req.OrgID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		FolderID:      req.FolderID,
	}, pagination)
	ctrl.write(c, paginatedData("secrets", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) SearchSecrets(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "SearchSecrets", logging.F("org_id", req.OrgID), logging.F("project_id", req.ProjectID), logging.F("environment_id", req.EnvironmentID), logging.F("folder_id", req.FolderID), logging.F("keyword", req.Keyword))
	filter := postgres.ListFilter{
		OrgID:         req.OrgID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		FolderID:      req.FolderID,
		Keyword:       req.Keyword,
	}
	if ctrl.cache != nil {
		items, err := ctrl.cache.SearchSecrets(c.Request.Context(), filter)
		if err == nil {
			pagination := paginationFromRequest(req.PageNum, req.PageSize)
			pagedItems, total := paginateSecrets(items, pagination)
			ctrl.write(c, paginatedData("secrets", pagedItems, total, pagination), nil)
			return
		}
		logging.Warn(c.Request.Context(), "SearchSecrets", "redis search failed, fallback to postgres", logging.F("error", err))
	}
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgID:         req.OrgID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		FolderID:      req.FolderID,
		Keyword:       req.Keyword,
	}, pagination)
	ctrl.write(c, paginatedData("secrets", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) DeleteSecret(c *gin.Context) {
	ctrl.log(c, "DeleteSecret")
	ctrl.delete(c, func(req idRequest) error {
		err := ctrl.store.DeleteSecret(c.Request.Context(), req.ID, ctrl.actor(c))
		if err == nil && ctrl.cache != nil {
			if cacheErr := ctrl.cache.DeleteSecret(c.Request.Context(), req.ID); cacheErr != nil {
				logging.Warn(c.Request.Context(), "DeleteSecret", "redis delete failed", logging.F("error", cacheErr), logging.F("id", req.ID))
			}
		}
		return err
	})
}

func (ctrl *Controller) ListAuditRecords(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListAuditRecords", logging.F("resource_type", req.ResourceType), logging.F("resource_id", req.ResourceID))
	pagination := paginationFromRequest(req.PageNum, req.PageSize)
	result, err := ctrl.store.ListAuditRecords(c.Request.Context(), req.ResourceType, req.ResourceID, pagination)
	ctrl.write(c, paginatedData("audit_records", result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) bind(c *gin.Context, target any) bool {
	if ctrl.store == nil {
		response.Fail(c, http.StatusServiceUnavailable, 1001, "store is not configured")
		return false
	}
	if err := c.ShouldBindJSON(target); err != nil {
		response.Fail(c, http.StatusBadRequest, 1002, err.Error())
		return false
	}
	return true
}

func (ctrl *Controller) write(c *gin.Context, data any, err error) {
	if err == nil {
		response.OK(c, data)
		return
	}
	logging.Error(c.Request.Context(), "controller.write", "request failed", logging.F("error", err))
	if errors.Is(err, auth.ErrPermissionDenied) {
		response.Fail(c, http.StatusForbidden, 1403, err.Error())
		return
	}
	if errors.Is(err, postgres.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 1404, err.Error())
		return
	}
	response.Fail(c, http.StatusInternalServerError, 1500, err.Error())
}

func (ctrl *Controller) delete(c *gin.Context, fn func(idRequest) error) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.write(c, gin.H{"deleted": true}, fn(req))
}

func (ctrl *Controller) actor(c *gin.Context) string {
	user := auth.UserFromContext(c)
	return user.UserId
}

func (ctrl *Controller) encryptSecret(c *gin.Context, value string) (postgres.SecretCiphertext, error) {
	if ctrl.encryptor == nil {
		return postgres.SecretCiphertext{}, errors.New("encryptor is not configured")
	}
	ciphertext, err := ctrl.encryptor.Encrypt(c.Request.Context(), []byte(value))
	if err != nil {
		return postgres.SecretCiphertext{}, err
	}
	return postgres.SecretCiphertext{
		Algorithm: ciphertext.Algorithm,
		Nonce:     ciphertext.Nonce,
		Data:      ciphertext.Data,
	}, nil
}

func (ctrl *Controller) decryptSecret(c *gin.Context, ciphertext postgres.SecretCiphertext) (string, error) {
	if ctrl.encryptor == nil {
		return "", errors.New("encryptor is not configured")
	}
	plaintext, err := ctrl.encryptor.Decrypt(c.Request.Context(), secretcrypto.Ciphertext{
		Algorithm: ciphertext.Algorithm,
		Nonce:     ciphertext.Nonce,
		Data:      ciphertext.Data,
	})
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (ctrl *Controller) cacheSecret(c *gin.Context, id string, ciphertext postgres.SecretCiphertext) {
	if ctrl.cache == nil {
		return
	}

	secret, err := ctrl.store.GetSecret(c.Request.Context(), id)
	if err != nil {
		logging.Warn(c.Request.Context(), "cacheSecret", "load secret for redis failed", logging.F("error", err), logging.F("id", id))
		return
	}

	payload, err := json.Marshal(ciphertext)
	if err != nil {
		logging.Warn(c.Request.Context(), "cacheSecret", "marshal ciphertext failed", logging.F("error", err), logging.F("id", id))
		return
	}

	err = ctrl.cache.UpsertSecret(c.Request.Context(), postgres.SecretCacheRecord{
		Secret:          secret,
		ValueCiphertext: payload,
	})
	if err != nil {
		logging.Warn(c.Request.Context(), "cacheSecret", "redis upsert failed", logging.F("error", err), logging.F("id", id))
	}
}

func (ctrl *Controller) log(c *gin.Context, method string, fields ...logging.Field) {
	logging.Info(c.Request.Context(), method, "handler called", fields...)
}

func paginateSecrets(items []postgres.Secret, pagination postgres.Pagination) ([]postgres.Secret, int64) {
	pagination = pagination.Normalize()
	total := int64(len(items))
	start := pagination.Offset()
	if start >= len(items) {
		return []postgres.Secret{}, total
	}
	end := start + pagination.Limit()
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total
}
