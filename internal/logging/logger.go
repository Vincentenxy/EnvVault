package logging

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

const defaultRequestIDHeader = "x-request-id"

type contextKey string

const requestIDContextKey contextKey = "envvault.request_id"

var std = log.New(os.Stdout, "", 0)

type Field struct {
	Key   string
	Value any
}

func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return "-"
	}
	requestID, ok := ctx.Value(requestIDContextKey).(string)
	if !ok || requestID == "" {
		return "-"
	}
	return requestID
}

func RequestIDHeader(header string) string {
	if strings.TrimSpace(header) == "" {
		return defaultRequestIDHeader
	}
	return header
}

func Info(ctx context.Context, method string, message string, fields ...Field) {
	write(ctx, "INFO", method, message, fields...)
}

func Warn(ctx context.Context, method string, message string, fields ...Field) {
	write(ctx, "WARN", method, message, fields...)
}

func Error(ctx context.Context, method string, message string, fields ...Field) {
	write(ctx, "ERROR", method, message, fields...)
}

func write(ctx context.Context, level string, method string, message string, fields ...Field) {
	parts := []string{
		time.Now().Format(time.RFC3339Nano),
		level,
		"x-request-id=" + sanitize(RequestIDFromContext(ctx)),
		"method=" + sanitize(method),
		"msg=" + quote(message),
	}
	for _, field := range fields {
		parts = append(parts, sanitize(field.Key)+"="+quote(formatValue(field)))
	}
	std.Println(strings.Join(parts, " "))
}

func formatValue(field Field) string {
	if isSensitiveField(field.Key) {
		return "***"
	}
	return fmt.Sprint(field.Value)
}

func isSensitiveField(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
	return strings.Contains(normalized, "value") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "cookie") ||
		strings.Contains(normalized, "jwt")
}

func quote(value string) string {
	return fmt.Sprintf("%q", value)
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return strings.ReplaceAll(value, " ", "_")
}
