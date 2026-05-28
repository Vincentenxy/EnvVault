package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/http/response"
	"envVault/internal/logging"
)

func (ctrl *Controller) Healthy(c *gin.Context) {
	logging.Info(c.Request.Context(), "Healthy", "handler called")
	response.OK(c, gin.H{"status": "ok"})
}
