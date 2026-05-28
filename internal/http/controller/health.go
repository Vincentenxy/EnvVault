package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/http/response"
)

func (ctrl *Controller) Healthy(c *gin.Context) {
	response.OK(c, gin.H{"status": "ok"})
}
