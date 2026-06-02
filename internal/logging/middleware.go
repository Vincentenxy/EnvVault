package logging

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"envVault/internal/id"
)

func RequestIdMiddleware(headerName string) gin.HandlerFunc {
	headerName = RequestIdHeader(headerName)

	return func(c *gin.Context) {
		requestId := c.GetHeader(headerName)
		if requestId == "" {
			generated, err := id.NewUUID()
			if err != nil {
				generated = time.Now().Format("20060102150405.000000000")
			}
			requestId = generated
		}

		c.Request.Header.Set(headerName, requestId)
		c.Header(headerName, requestId)
		c.Set(string(requestIdContextKey), requestId)
		c.Request = c.Request.WithContext(WithRequestId(c.Request.Context(), requestId))
		c.Next()
	}
}

func AccessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		latency := time.Since(start)
		ctx := c.Request.Context()
		Info(ctx, c.Request.Method, "http request completed",
			F("path", c.Request.URL.Path),
			F("route", c.FullPath()),
			F("status", c.Writer.Status()),
			F("latency", latency.String()),
			F("client_ip", c.ClientIP()),
			F("errors", c.Errors.String()),
		)
	}
}

func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				Error(c.Request.Context(), "panic", "http request panic",
					F("path", c.Request.URL.Path),
					F("panic", recovered),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code": 1500,
					"msg":  "internal server error",
					"data": nil,
				})
			}
		}()

		c.Next()
	}
}
