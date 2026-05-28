package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/http/response"
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
	item, err := ctrl.store.CreateOrganization(c.Request.Context(), req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListOrganizations(c *gin.Context) {
	items, err := ctrl.store.ListOrganizations(c.Request.Context())
	ctrl.write(c, items, err)
}

func (ctrl *Controller) GetOrganization(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.GetOrganization(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateOrganization(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.UpdateOrganization(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteOrganization(c *gin.Context) {
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteOrganization(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateProject(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.CreateProject(c.Request.Context(), req.ParentID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListProjects(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	items, err := ctrl.store.ListProjects(c.Request.Context(), req.OrgID)
	ctrl.write(c, items, err)
}

func (ctrl *Controller) GetProject(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.GetProject(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateProject(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.UpdateProject(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteProject(c *gin.Context) {
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteProject(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateEnvironment(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.CreateEnvironment(c.Request.Context(), req.ParentID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListEnvironments(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	items, err := ctrl.store.ListEnvironments(c.Request.Context(), req.ProjectID)
	ctrl.write(c, items, err)
}

func (ctrl *Controller) GetEnvironment(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.GetEnvironment(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateEnvironment(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.UpdateEnvironment(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteEnvironment(c *gin.Context) {
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteEnvironment(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateFolder(c *gin.Context) {
	var req createEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.CreateFolder(c.Request.Context(), req.ParentID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListFolders(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	items, err := ctrl.store.ListFolders(c.Request.Context(), req.EnvironmentID)
	ctrl.write(c, items, err)
}

func (ctrl *Controller) GetFolder(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.GetFolder(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateFolder(c *gin.Context) {
	var req updateEntityRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.UpdateFolder(c.Request.Context(), req.ID, req.Name, req.Comment, ctrl.actor(c))
	ctrl.write(c, item, err)
}

func (ctrl *Controller) DeleteFolder(c *gin.Context) {
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteFolder(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) CreateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ciphertext, err := ctrl.encryptSecret(c, req.Value)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	item, err := ctrl.store.CreateSecret(c.Request.Context(), req.FolderID, req.Key, req.Comment, ctrl.actor(c), ciphertext)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) UpdateSecret(c *gin.Context) {
	var req secretRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ciphertext, err := ctrl.encryptSecret(c, req.Value)
	if err != nil {
		ctrl.write(c, nil, err)
		return
	}
	item, err := ctrl.store.UpdateSecret(c.Request.Context(), req.ID, req.Key, req.Comment, ctrl.actor(c), ciphertext)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) GetSecret(c *gin.Context) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	item, err := ctrl.store.GetSecret(c.Request.Context(), req.ID)
	ctrl.write(c, item, err)
}

func (ctrl *Controller) ListSecrets(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	items, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgID:         req.OrgID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		FolderID:      req.FolderID,
	})
	ctrl.write(c, items, err)
}

func (ctrl *Controller) SearchSecrets(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	items, err := ctrl.store.ListSecrets(c.Request.Context(), postgres.ListFilter{
		OrgID:         req.OrgID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		FolderID:      req.FolderID,
		Keyword:       req.Keyword,
	})
	ctrl.write(c, items, err)
}

func (ctrl *Controller) DeleteSecret(c *gin.Context) {
	ctrl.delete(c, func(req idRequest) error {
		return ctrl.store.DeleteSecret(c.Request.Context(), req.ID, ctrl.actor(c))
	})
}

func (ctrl *Controller) ListAuditRecords(c *gin.Context) {
	var req listRequest
	if !ctrl.bind(c, &req) {
		return
	}
	items, err := ctrl.store.ListAuditRecords(c.Request.Context(), req.ResourceType, req.ResourceID)
	ctrl.write(c, items, err)
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
	if user.StaffUserID != "" {
		return user.StaffUserID
	}
	return user.StaffNo
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
