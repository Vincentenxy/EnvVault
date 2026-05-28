package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/http/response"
	"envVault/internal/logging"
)

func (ctrl *Controller) Me(c *gin.Context) {
	logging.Info(c.Request.Context(), "Me", "handler called")
	response.OK(c, auth.UserFromContext(c))
}
