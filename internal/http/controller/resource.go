package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	secretcrypto "envVault/internal/crypto"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	"envVault/internal/store/postgres"
)

type Entity = postgres.Entity

type createEntityRequest struct {
	ParentID       string   `json:"parentId,omitempty"`
	Code           string   `json:"code"`
	Name           string   `json:"name"`
	Comment        string   `json:"comment"`
	EnvironmentIDs []string `json:"environmentIds,omitempty"`
}

type idOrCodeRequest struct {
	ParentID string `json:"parentId,omitempty"`
	ID       string `json:"id,omitempty"`
	Code     string `json:"code,omitempty"`
}

type updateByIdOrCodeRequest struct {
	ParentID string `json:"parentId,omitempty"`
	ID       string `json:"id,omitempty"`
	Code     string `json:"code,omitempty"`
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
	PageRequest
	OrgID         string `json:"orgId,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	FolderID      string `json:"folderId,omitempty"`
	ResourceType  string `json:"resourceType,omitempty"`
	ResourceID    string `json:"resourceId,omitempty"`
	Keyword       string `json:"keyword,omitempty"`
}

type PageRequest struct {
	PageNum  int `json:"pageNum"`
	PageSize int `json:"pageSize"`
}

type PageResp struct {
	PageNum  int   `json:"pageNum"`
	PageSize int   `json:"pageSize"`
	Total    int64 `json:"total"`
	List     any   `json:"list"`
}

type secretRequest struct {
	ID       string `json:"id,omitempty"`
	FolderID string `json:"folderId,omitempty"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Comment  string `json:"comment"`
}

var (
	codePattern      = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	secretKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

func (ctrl *Controller) CreateOrganization(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	ctrl.log(c, "CreateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateOrganization(c.Request.Context(), req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListOrganizations(c *gin.Context) {
	var req PageRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.log(c, "ListOrganizations")
	pagination := paginationFromRequest(req)
	result, err := ctrl.store.ListOrganizations(c.Request.Context(), pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetOrganization(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "organization") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetOrganization", logging.F("code", req.Code))
		item, err = ctrl.store.GetOrganizationByCode(c.Request.Context(), req.Code)
	} else {
		ctrl.log(c, "GetOrganization", logging.F("id", req.ID))
		item, err = ctrl.store.GetOrganization(c.Request.Context(), req.ID)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateOrganization(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "organization") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateOrganization", logging.F("code", req.Code), logging.F("name", req.Name))
		org, getErr := ctrl.store.GetOrganizationByCode(c.Request.Context(), req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateOrganization(c.Request.Context(), org.ID, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateOrganization", logging.F("id", req.ID), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateOrganization(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteOrganization(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "organization") {
		return
	}
	ctrl.log(c, "DeleteOrganization")
	if req.Code != "" {
		org, err := ctrl.store.GetOrganizationByCode(c.Request.Context(), req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteOrganization(c.Request.Context(), org.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteOrganization(c.Request.Context(), req.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}

func (ctrl *Controller) CreateProject(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	ctrl.log(c, "CreateProject", logging.F("org_id", req.ParentID), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateProject(c.Request.Context(), req.ParentID, req.Code, req.Name, req.Comment, ctrl.actor(c), req.EnvironmentIDs)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListProjects(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListProjects(c, req) {
		return
	}
	ctrl.log(c, "ListProjects", logging.F("org_id", req.OrgID))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListProjects(c.Request.Context(), req.OrgID, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetProject(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "project") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetProject", logging.F("org_id", req.ParentID), logging.F("code", req.Code))
		item, err = ctrl.store.GetProjectByCode(c.Request.Context(), req.ParentID, req.Code)
	} else {
		ctrl.log(c, "GetProject", logging.F("id", req.ID))
		item, err = ctrl.store.GetProject(c.Request.Context(), req.ID)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateProject(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "project") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateProject", logging.F("org_id", req.ParentID), logging.F("code", req.Code), logging.F("name", req.Name))
		proj, getErr := ctrl.store.GetProjectByCode(c.Request.Context(), req.ParentID, req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateProject(c.Request.Context(), proj.ID, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateProject", logging.F("id", req.ID), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateProject(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteProject(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "project") {
		return
	}
	ctrl.log(c, "DeleteProject")
	if req.Code != "" {
		proj, err := ctrl.store.GetProjectByCode(c.Request.Context(), req.ParentID, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteProject(c.Request.Context(), proj.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteProject(c.Request.Context(), req.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}

func (ctrl *Controller) CreateEnvironment(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	ctrl.log(c, "CreateEnvironment", logging.F("project_id", req.ParentID), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateEnvironment(c.Request.Context(), req.ParentID, req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListEnvironments(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListEnvironments(c, req) {
		return
	}
	ctrl.log(c, "ListEnvironments", logging.F("org_id", req.OrgID))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListEnvironments(c.Request.Context(), req.OrgID, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetEnvironment(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "environment") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetEnvironment", logging.F("org_id", req.ParentID), logging.F("code", req.Code))
		item, err = ctrl.store.GetEnvironmentByCode(c.Request.Context(), req.ParentID, req.Code)
	} else {
		ctrl.log(c, "GetEnvironment", logging.F("id", req.ID))
		item, err = ctrl.store.GetEnvironment(c.Request.Context(), req.ID)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateEnvironment(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "environment") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateEnvironment", logging.F("org_id", req.ParentID), logging.F("code", req.Code), logging.F("name", req.Name))
		env, getErr := ctrl.store.GetEnvironmentByCode(c.Request.Context(), req.ParentID, req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateEnvironment(c.Request.Context(), env.ID, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateEnvironment", logging.F("id", req.ID), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateEnvironment(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteEnvironment(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "environment") {
		return
	}
	ctrl.log(c, "DeleteEnvironment")
	if req.Code != "" {
		env, err := ctrl.store.GetEnvironmentByCode(c.Request.Context(), req.ParentID, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteEnvironment(c.Request.Context(), env.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteEnvironment(c.Request.Context(), req.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}

func (ctrl *Controller) CreateFolder(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateCode(c, req.Code) {
		return
	}
	ctrl.log(c, "CreateFolder", logging.F("environment_id", req.ParentID), logging.F("code", req.Code), logging.F("name", req.Name))
	item, err := ctrl.store.CreateFolder(c.Request.Context(), req.ParentID, req.Code, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListFolders(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateListFolders(c, req) {
		return
	}
	ctrl.log(c, "ListFolders", logging.F("environment_id", req.EnvironmentID))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListFolders(c.Request.Context(), req.EnvironmentID, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) GetFolder(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "folder") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "GetFolder", logging.F("environment_id", req.ParentID), logging.F("code", req.Code))
		item, err = ctrl.store.GetFolderByCode(c.Request.Context(), req.ParentID, req.Code)
	} else {
		ctrl.log(c, "GetFolder", logging.F("id", req.ID))
		item, err = ctrl.store.GetFolder(c.Request.Context(), req.ID)
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateFolder(c *gin.Context) {
	var req updateByIdOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateUpdateIdOrCode(c, req, "folder") {
		return
	}
	var item Entity
	var err error
	if req.Code != "" {
		ctrl.log(c, "UpdateFolder", logging.F("environment_id", req.ParentID), logging.F("code", req.Code), logging.F("name", req.Name))
		folder, getErr := ctrl.store.GetFolderByCode(c.Request.Context(), req.ParentID, req.Code)
		if getErr != nil {
			ctrl.write(c, nil, getErr)
			return
		}
		item, err = ctrl.store.UpdateFolder(c.Request.Context(), folder.ID, req.Name, req.Comment, ctrl.actor(c))
	} else {
		ctrl.log(c, "UpdateFolder", logging.F("id", req.ID), logging.F("name", req.Name))
		item, err = ctrl.store.UpdateFolder(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	}
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteFolder(c *gin.Context) {
	var req idOrCodeRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateIdOrCode(c, req, "folder") {
		return
	}
	ctrl.log(c, "DeleteFolder")
	if req.Code != "" {
		folder, err := ctrl.store.GetFolderByCode(c.Request.Context(), req.ParentID, req.Code)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		err = ctrl.store.DeleteFolder(c.Request.Context(), folder.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	} else {
		err := ctrl.store.DeleteFolder(c.Request.Context(), req.ID, ctrl.actor(c))
		ctrl.write(c, gin.H{"deleted": true}, err)
	}
}

func (ctrl *Controller) CreateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if !validateSecretKey(c, req.Key) {
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
	if !validateSecretKey(c, req.Key) {
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
	if !validateListSecrets(c, req) {
		return
	}
	ctrl.log(c, "ListSecrets", logging.F("org_id", req.OrgID), logging.F("project_id", req.ProjectID), logging.F("environment_id", req.EnvironmentID), logging.F("folder_id", req.FolderID))
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgID:         req.OrgID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		FolderID:      req.FolderID,
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
			pagination := paginationFromRequest(req.PageRequest)
			pagedItems, total := paginateSecrets(items, pagination)
			ctrl.write(c, pageData(pagedItems, total, pagination), nil)
			return
		}
		logging.Warn(c.Request.Context(), "SearchSecrets", "redis search failed, fallback to postgres", logging.F("error", err))
	}
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgID:         req.OrgID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		FolderID:      req.FolderID,
		Keyword:       req.Keyword,
	}, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
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
	pagination := paginationFromRequest(req.PageRequest)
	result, err := ctrl.store.ListAuditRecords(c.Request.Context(), req.ResourceType, req.ResourceID, pagination)
	ctrl.write(c, pageData(result.Items, result.Total, pagination), err)
}

func (ctrl *Controller) bind(c *gin.Context, target any) bool {
	if ctrl.store == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "store is not configured")
		logging.Error(c.Request.Context(), "bind", "store is not configured")
		return false
	}
	if err := c.ShouldBindJSON(target); err != nil {
		logging.Error(c.Request.Context(), "bind", "invalid request body", logging.F("error", err))
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, err.Error())
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
		response.Fail(c, http.StatusForbidden, response.CodeForbidden, err.Error())
		return
	}
	if errors.Is(err, postgres.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, err.Error())
		return
	}
	response.FailWithMsg(c, err.Error())
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
	if ctrl.store != nil {
		ctrl.store.CacheUserLabel(user.UserId, user.Name)
	}
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

func validateCode(c *gin.Context, code string) bool {
	if !codePattern.MatchString(code) {
		logging.Warn(c.Request.Context(), "validateCode", "invalid code format", logging.F("code", code))
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "code must match ^[a-z0-9]+(-[a-z0-9]+)*$")
		return false
	}
	return true
}

func validateIdOrCode(c *gin.Context, req idOrCodeRequest, resourceType string) bool {
	if req.ID == "" && req.Code == "" {
		logging.Warn(c.Request.Context(), "validateIdOrCode", resourceType+" id or code is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id or code is required")
		return false
	}
	if req.ID != "" && req.Code != "" {
		logging.Warn(c.Request.Context(), "validateIdOrCode", resourceType+" id and code are mutually exclusive")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id and code are mutually exclusive")
		return false
	}
	return true
}

func validateUpdateIdOrCode(c *gin.Context, req updateByIdOrCodeRequest, resourceType string) bool {
	if req.ID == "" && req.Code == "" {
		logging.Warn(c.Request.Context(), "validateUpdateIdOrCode", resourceType+" id or code is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id or code is required")
		return false
	}
	if req.ID != "" && req.Code != "" {
		logging.Warn(c.Request.Context(), "validateUpdateIdOrCode", resourceType+" id and code are mutually exclusive")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id and code are mutually exclusive")
		return false
	}
	return true
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
	if req.OrgID == "" {
		logging.Warn(c.Request.Context(), "validateListProjects", "orgId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "orgId is required")
		return false
	}
	return true
}

func validateListEnvironments(c *gin.Context, req listRequest) bool {
	if req.OrgID == "" {
		logging.Warn(c.Request.Context(), "validateListEnvironments", "orgId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "orgId is required")
		return false
	}
	return true
}

func validateListFolders(c *gin.Context, req listRequest) bool {
	if req.EnvironmentID == "" {
		logging.Warn(c.Request.Context(), "validateListFolders", "environmentId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "environmentId is required")
		return false
	}
	return true
}

func validateListSecrets(c *gin.Context, req listRequest) bool {
	if req.EnvironmentID == "" && req.FolderID == "" {
		logging.Warn(c.Request.Context(), "validateListSecrets", "environmentId or folderId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "environmentId or folderId is required")
		return false
	}
	return true
}

func validateSearchSecrets(c *gin.Context, req listRequest) bool {
	if req.Keyword == "" {
		logging.Warn(c.Request.Context(), "validateSearchSecrets", "keyword is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "keyword is required")
		return false
	}
	if req.EnvironmentID == "" && req.FolderID == "" {
		logging.Warn(c.Request.Context(), "validateSearchSecrets", "environmentId or folderId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "environmentId or folderId is required")
		return false
	}
	return true
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
