package controller

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"

	"envVault/internal/auth"
	"envVault/internal/http/response"
)

type devJWTRequest struct {
	UserID           string `json:"userId"`
	Name             string `json:"name"`
	ExpiresInSeconds int64  `json:"expiresInSeconds"`
}

func (ctrl *Controller) CreateDevJWT(c *gin.Context) {
	if !ctrl.config.Auth.DevTokenEnabled {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "not found")
		return
	}

	req := devJWTRequest{
		UserID:           ctrl.config.Auth.DevUserID,
		Name:             ctrl.config.Auth.DevUserName,
		ExpiresInSeconds: 3600,
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, err.Error())
			return
		}
	}
	if req.UserID == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "userId is required")
		return
	}
	if req.ExpiresInSeconds <= 0 {
		req.ExpiresInSeconds = 3600
	}
	if req.ExpiresInSeconds > 86400 {
		req.ExpiresInSeconds = 86400
	}

	now := time.Now()
	expiresAt := now.Add(time.Duration(req.ExpiresInSeconds) * time.Second)
	token, err := auth.SignToken(ctrl.config.Auth.DevPrivateKey, auth.Claims{
		UserId: req.UserID,
		Name:   req.Name,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   req.UserID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	})
	if err != nil {
		response.Fail(c, http.StatusServiceUnavailable, response.CodeServiceUnavailable, err.Error())
		return
	}

	response.OK(c, gin.H{
		"tokenType": "Bearer",
		"token":     token,
		"expiresAt": expiresAt,
	})
}
