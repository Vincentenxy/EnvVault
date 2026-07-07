package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
)

// ---- 请求 / 响应结构体 ----

type patCreateRequest struct {
	Name      string     `json:"name" binding:"required"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

type patDeleteRequest struct {
	Id string `json:"id" binding:"required"`
}

type patCreateResponse struct {
	Id          string     `json:"id"`
	Name        string     `json:"name"`
	Token       string     `json:"token"` // 明文,仅此一次
	TokenPrefix string     `json:"tokenPrefix"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// ---- Handler ----

// CreatePAT 生成新的 Personal Access Token。
//
// POST /api/v1/accessToken/create
// 响应中 token 字段为明文,仅在创建时返回一次。
func (ctrl *Controller) CreatePAT(c *gin.Context) {
	if ctrl.pat == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeServiceUnavailable, "pat service is not configured")
		return
	}
	user := auth.UserFromContext(c)
	var req patCreateRequest
	if !ctrl.bind(c, &req) {
		return
	}
	tok, plain, err := ctrl.pat.CreatePAT(c.Request.Context(), user.UserId, req.Name, req.ExpiresAt)
	if err != nil {
		logging.Error(c.Request.Context(), "CreatePAT", "create pat failed", logging.F("error", err))
		writePATEror(c, err)
		return
	}
	response.OK(c, patCreateResponse{
		Id:          tok.Id,
		Name:        tok.Name,
		Token:       plain,
		TokenPrefix: tok.TokenPrefix,
		ExpiresAt:   tok.ExpiresAt,
		CreatedAt:   tok.CreatedAt,
	})
}

// ListPATs 列出当前用户的所有未删除 token。
//
// GET /api/v1/accessToken/list
func (ctrl *Controller) ListPATs(c *gin.Context) {
	if ctrl.pat == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeServiceUnavailable, "pat service is not configured")
		return
	}
	user := auth.UserFromContext(c)
	tokens, err := ctrl.pat.ListPATs(c.Request.Context(), user.UserId)
	if err != nil {
		logging.Error(c.Request.Context(), "ListPATs", "list pats failed", logging.F("error", err))
		response.FailWithMsg(c, "list access tokens failed: "+err.Error())
		return
	}
	if tokens == nil {
		response.OK(c, []domain.AccessToken{})
		return
	}
	response.OK(c, tokens)
}

// DeletePAT 软删除指定 token(只能删自己的)。
//
// DELETE /api/v1/accessToken/delete
func (ctrl *Controller) DeletePAT(c *gin.Context) {
	if ctrl.pat == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeServiceUnavailable, "pat service is not configured")
		return
	}
	user := auth.UserFromContext(c)
	var req patDeleteRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if err := ctrl.pat.DeletePAT(c.Request.Context(), user.UserId, req.Id); err != nil {
		logging.Error(c.Request.Context(), "DeletePAT", "delete pat failed", logging.F("error", err))
		writePATEror(c, err)
		return
	}
	response.OK(c, gin.H{"deleted": true})
}

// writePATEror 把 PAT service 层错误映射为 HTTP 响应。
func writePATEror(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, err.Error())
	default:
		response.FailWithMsg(c, err.Error())
	}
}
