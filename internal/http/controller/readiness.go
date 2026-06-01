package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"envVault/internal/http/response"
	"envVault/internal/logging"
)

func (ctrl *Controller) Ready(c *gin.Context) {
	logging.Info(c.Request.Context(), "Ready", "handler called")
	databaseStatus := "notConfigured"
	status := http.StatusOK
	if ctrl.database != nil {
		databaseStatus = "ok"
		if err := ctrl.database.PingContext(c.Request.Context()); err != nil {
			databaseStatus = "error"
			status = http.StatusServiceUnavailable
		}
	}

	responseBody := gin.H{
		"status":                "ok",
		"database":              databaseStatus,
		"jwtConfigured":         ctrl.config.Auth.PublicKey != "",
		"encryptionConfigured":  ctrl.config.Security.EncryptionKey != "",
		"defaultEnvironmentSet": []string{"dev", "test", "sim", "prod"},
	}
	if status != http.StatusOK {
		logging.Warn(c.Request.Context(), "Ready", "database is not ready", logging.F("database", databaseStatus))
		response.Fail(c, status, response.CodeServiceUnavailable, "database is not ready")
		return
	}
	response.OK(c, responseBody)
}
