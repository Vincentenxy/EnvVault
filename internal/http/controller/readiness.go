package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"envVault/internal/http/response"
	"envVault/internal/logging"
)

func (ctrl *Controller) Ready(c *gin.Context) {
	logging.Info(c.Request.Context(), "Ready", "handler called")
	databaseStatus := "not_configured"
	status := http.StatusOK
	if ctrl.database != nil {
		databaseStatus = "ok"
		if err := ctrl.database.PingContext(c.Request.Context()); err != nil {
			databaseStatus = "error"
			status = http.StatusServiceUnavailable
		}
	}

	responseBody := gin.H{
		"status":                  "ok",
		"database":                databaseStatus,
		"jwt_configured":          ctrl.config.Auth.JWTSecret != "",
		"encryption_configured":   ctrl.config.Security.EncryptionKey != "",
		"default_environment_set": []string{"dev", "test", "sim", "prod"},
	}
	if status != http.StatusOK {
		logging.Warn(c.Request.Context(), "Ready", "database is not ready", logging.F("database", databaseStatus))
		response.Fail(c, status, 1503, "database is not ready")
		return
	}
	response.OK(c, responseBody)
}
