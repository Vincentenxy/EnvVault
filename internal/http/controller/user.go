package controller

import (
	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/http/response"
)

func (ctrl *Controller) Me(c *gin.Context) {
	response.OK(c, auth.UserFromContext(c))
}
