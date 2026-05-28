package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Body struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{
		Code: 0,
		Msg:  "success",
		Data: data,
	})
}

func Fail(c *gin.Context, status int, code int, msg string) {
	c.JSON(status, Body{
		Code: code,
		Msg:  msg,
		Data: nil,
	})
}
