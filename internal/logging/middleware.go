package logging

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"envVault/internal/id"
)

func RequestIDMiddleware(headerName string) gin.HandlerFunc {
	headerName = RequestIDHeader(headerName)

	return func(c *gin.Context) {
		requestID := c.GetHeader(headerName)
		if requestID == "" {
			generated, err := id.NewUUID()
			if err != nil {
				generated = time.Now().Format("20060102150405.000000000")
			}
			requestID = generated
		}

		c.Request.Header.Set(headerName, requestID)
		c.Header(headerName, requestID)
		c.Set(string(requestIDContextKey), requestID)
		c.Request = c.Request.WithContext(WithRequestID(c.Request.Context(), requestID))
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
					"code": http.StatusInternalServerError,
					"msg":  "internal server error",
					"data": nil,
				})
			}
		}()

		c.Next()
	}
}
