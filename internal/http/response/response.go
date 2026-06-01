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

const (
	CodeSuccess            = 0
	CodeFailure            = -1
	CodeStoreUnavailable   = 1001
	CodeInvalidRequest     = 1002
	CodeUnauthorized       = 1401
	CodeForbidden          = 1403
	CodeNotFound           = 1404
	CodeConflict           = 1409
	CodeInternal           = 1500
	CodeServiceUnavailable = 1503
)

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{
		Code: CodeSuccess,
		Msg:  "success",
		Data: data,
	})
}

func OkWithMsg(c *gin.Context, msg string, data any) {
	c.JSON(http.StatusOK, Body{
		Code: CodeSuccess,
		Msg:  msg,
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

func FailWithMsg(c *gin.Context, msg string) {
	c.JSON(http.StatusOK, Body{
		Code: CodeFailure,
		Msg:  msg,
		Data: nil,
	})
}
