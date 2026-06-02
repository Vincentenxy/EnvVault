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
	ParentId       string   `json:"parentId,omitempty"`
	Code           string   `json:"code"`
	Name           string   `json:"name"`
	Comment        string   `json:"comment"`
	EnvironmentIds []string `json:"environmentIds,omitempty"`
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
	OrgId         string `json:"orgId,omitempty"`
	ProjectId     string `json:"projectId,omitempty"`
	EnvironmentId string `json:"environmentId,omitempty"`
	FolderId      string `json:"folderId,omitempty"`
	ResourceType  string `json:"resourceType,omitempty"`
	ResourceId    string `json:"resourceId,omitempty"`
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

// write 统一写入响应：成功直接返回 data；错误时根据错误类型映射到不同的 HTTP 状态码。
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

// delete 是删除接口的通用包装：绑定 idRequest 后调用 fn 执行真正的删除逻辑。
func (ctrl *Controller) delete(c *gin.Context, fn func(idRequest) error) {
	var req idRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ctrl.write(c, gin.H{"deleted": true}, fn(req))
}

// actor 返回当前请求的操作者用户 ID，并顺便把用户标签缓存到 store。
func (ctrl *Controller) actor(c *gin.Context) string {
	user := auth.UserFromContext(c)
	if ctrl.store != nil {
		ctrl.store.CacheUserLabel(user.UserId, user.Name)
	}
	return user.UserId
}

// encryptSecret 将明文 value 加密为可持久化的 ciphertext。
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

// decryptSecret 把 ciphertext 解密为明文字符串。
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

// cacheSecret 把 secret 密文写回 Redis，便于后续走缓存搜索。
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
	if req.Id != "" && req.Code != "" {
		logging.Warn(c.Request.Context(), "validateIdOrCode", resourceType+" id and code are mutually exclusive")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id and code are mutually exclusive")
		return false
	}
	return true
}

func validateUpdateIdOrCode(c *gin.Context, req updateByIdOrCodeRequest, resourceType string) bool {
	if req.Id == "" && req.Code == "" {
		logging.Warn(c.Request.Context(), "validateUpdateIdOrCode", resourceType+" id or code is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, resourceType+" id or code is required")
		return false
	}
	if req.Id != "" && req.Code != "" {
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
	if req.OrgId == "" {
		logging.Warn(c.Request.Context(), "validateListProjects", "orgId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "orgId is required")
		return false
	}
	return true
}

func validateListEnvironments(c *gin.Context, req listRequest) bool {
	if req.OrgId == "" {
		logging.Warn(c.Request.Context(), "validateListEnvironments", "orgId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "orgId is required")
		return false
	}
	return true
}

func validateListFolders(c *gin.Context, req listRequest) bool {
	if req.EnvironmentId == "" {
		logging.Warn(c.Request.Context(), "validateListFolders", "environmentId is required")
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "environmentId is required")
		return false
	}
	return true
}

func validateListSecrets(c *gin.Context, req listRequest) bool {
	if req.EnvironmentId == "" && req.FolderId == "" {
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
	if req.EnvironmentId == "" && req.FolderId == "" {
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
