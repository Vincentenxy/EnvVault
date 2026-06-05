package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/auth/ratelimit"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	"envVault/internal/service"
)

// ---- Request types ----

type authRegisterRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type authLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authChangePasswordRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// ---- 4 个 handler ----

// Register 自助注册。匿名可调;首用户自动 platform_admin。
func (ctrl *Controller) Register(c *gin.Context) {
	if ctrl.auth == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "auth service is not configured")
		return
	}
	var req authRegisterRequest
	if !ctrl.bind(c, &req) {
		return
	}
	tok, err := ctrl.auth.Register(c.Request.Context(), req.Email, req.Name, req.Password)
	if err != nil {
		writeAuthError(c, "Register", err)
		return
	}
	response.OK(c, gin.H{
		"userId":    tok.UserId,
		"email":     tok.Email,
		"name":      tok.Name,
		"token":     tok.Token,
		"issuedAt":  tok.IssuedAt,
		"expiresAt": tok.ExpiresAt,
	})
}

// Login 邮箱 + 密码登录。匿名可调;走 IP 频控。
func (ctrl *Controller) Login(c *gin.Context) {
	if ctrl.auth == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "auth service is not configured")
		return
	}
	var req authLoginRequest
	if !ctrl.bind(c, &req) {
		return
	}
	ip := c.ClientIP()
	tok, err := ctrl.auth.Login(c.Request.Context(), req.Email, req.Password, ip)
	if err != nil {
		writeAuthError(c, "Login", err)
		return
	}
	response.OK(c, gin.H{
		"userId":    tok.UserId,
		"email":     tok.Email,
		"name":      tok.Name,
		"token":     tok.Token,
		"issuedAt":  tok.IssuedAt,
		"expiresAt": tok.ExpiresAt,
	})
}

// Logout 强制登出。RequireAuth。
func (ctrl *Controller) Logout(c *gin.Context) {
	if ctrl.auth == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "auth service is not configured")
		return
	}
	user := auth.UserFromContext(c)
	if err := ctrl.auth.Logout(c.Request.Context(), user.UserId); err != nil {
		writeAuthError(c, "Logout", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ChangePassword 改密。RequireAuth。成功后旧 token 全部失效。
func (ctrl *Controller) ChangePassword(c *gin.Context) {
	if ctrl.auth == nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeStoreUnavailable, "auth service is not configured")
		return
	}
	user := auth.UserFromContext(c)
	var req authChangePasswordRequest
	if !ctrl.bind(c, &req) {
		return
	}
	if err := ctrl.auth.ChangePassword(c.Request.Context(), user.UserId, req.OldPassword, req.NewPassword); err != nil {
		writeAuthError(c, "ChangePassword", err)
		return
	}
	response.OK(c, gin.H{"changed": true})
}

// ---- 错误码映射 ----

// writeAuthError 把 service 层 sentinel 错误映射为 HTTP 状态码 + 业务码。
// 与 ctrl.write 不重复:writeAuthError 关心的是「认证类语义」,不与通用 resource 错误混用。
func writeAuthError(c *gin.Context, method string, err error) {
	logging.Error(c.Request.Context(), method, "auth request failed", logging.F("error", err))
	switch {
	case errors.Is(err, ratelimit.ErrRateLimited):
		response.Fail(c, http.StatusTooManyRequests, response.CodeRateLimited, err.Error())
	case errors.Is(err, service.ErrInvalidArgument):
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, err.Error())
	case errors.Is(err, service.ErrEmailAlreadyExists):
		response.Fail(c, http.StatusConflict, response.CodeConflict, err.Error())
	case errors.Is(err, service.ErrBadCredentials):
		response.Fail(c, http.StatusUnauthorized, response.CodeUnauthorized, err.Error())
	default:
		response.FailWithMsg(c, err.Error())
	}
}
