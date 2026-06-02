package logging

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

const defaultRequestIdHeader = "x-request-id"

type contextKey string

const requestIdContextKey contextKey = "envvault.request_id"

var std = log.New(os.Stdout, "", 0)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

type Field struct {
	Key   string
	Value any
}

func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

func WithRequestId(ctx context.Context, requestId string) context.Context {
	return context.WithValue(ctx, requestIdContextKey, requestId)
}

func RequestIdFromContext(ctx context.Context) string {
	if ctx == nil {
		return "-"
	}
	requestId, ok := ctx.Value(requestIdContextKey).(string)
	if !ok || requestId == "" {
		return "-"
	}
	return requestId
}

func RequestIdHeader(header string) string {
	if strings.TrimSpace(header) == "" {
		return defaultRequestIdHeader
	}
	return header
}

func Info(ctx context.Context, method string, message string, fields ...Field) {
	write(ctx, "INFO", colorGreen, method, message, fields...)
}

func Warn(ctx context.Context, method string, message string, fields ...Field) {
	write(ctx, "WARN", colorYellow, method, message, fields...)
}

func Error(ctx context.Context, method string, message string, fields ...Field) {
	write(ctx, "ERROR", colorRed, method, message, fields...)
}

func write(ctx context.Context, level string, color string, method string, message string, fields ...Field) {
	requestId := sanitize(RequestIdFromContext(ctx))
	timestamp := time.Now().Format("2006-01-02 15:04:05.000000000")

	var fieldsStr string
	if len(fields) > 0 {
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			parts = append(parts, sanitize(field.Key)+"="+quote(formatValue(field)))
		}
		fieldsStr = "  " + strings.Join(parts, "  ")
	}

	logLine := fmt.Sprintf("%s %s %s  request_id=%s  method=%s  %s  msg=%s %s",
		colorize(timestamp, colorGray),
		colorize(level, color),
		colorize(requestId, colorCyan),
		colorize(method, colorCyan),
		fieldsStr,
		colorize("msg", colorGray),
		colorize(quote(message), colorReset),
		colorReset,
	)

	std.Println(logLine)
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

func colorize(s string, color string) string {
	return color + s + colorReset
}
