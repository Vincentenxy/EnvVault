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
	CodeBatchCreateError   = -1 // v11:batchCreate 业务码;与 CodeFailure 同值(-1),语义别名
	CodeStoreUnavailable   = 1001
	CodeInvalidRequest     = 1002
	CodeUnauthorized       = 1401
	CodeForbidden          = 1403
	CodeNotFound           = 1404
	CodeConflict           = 1409
	CodeRateLimited        = 1429
	CodeInternal           = 1500
	CodeServiceUnavailable = 1503
)

func OK(c *gin.Context, data any) {
	OKWithCode(c, CodeSuccess, "success", data)
}

// OKWithCode 允许自定义业务码的 OK 响应(用于 batchCreate 这种「HTTP 200 + 自定义 body code」端点)。
// HTTP status 仍为 200(与 OK 保持一致),只覆盖 body.code 和 body.msg。
func OKWithCode(c *gin.Context, code int, msg string, data any) {
	c.JSON(http.StatusOK, Body{
		Code: code,
		Msg:  msg,
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
