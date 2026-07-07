package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/logging"
)

// PATConfig 配置 Personal Access Token 中间件。
//
// ValidatePAT 按 SHA256 hash 查找未删除且未过期的 token,返回关联 userId。
// UserNameLoader 可选:从 userId 获取显示名。
type PATConfig struct {
	ValidatePAT    func(ctx context.Context, tokenHash string) (userId string, err error)
	UserNameLoader func(ctx context.Context, userId string) string
}

// PATPrefix 所有 PAT 的统一前缀,供中间件和 service 共享。
const PATPrefix = "envvault-"

// IsPAT 判断 Authorization header 是否为 PAT 格式("Bearer envvault-xxx")。
func IsPAT(authHeader string) bool {
	return strings.HasPrefix(authHeader, "Bearer "+PATPrefix)
}

// ExtractPAT 从 Authorization header 提取 PAT 明文。
func ExtractPAT(authHeader string) string {
	return strings.TrimPrefix(authHeader, "Bearer ")
}

// HashPAT 对 token 明文做 SHA256 哈希,供 service 和 middleware 共享。
func HashPAT(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return fmt.Sprintf("%x", h)
}

// CombinedAuthMiddleware 统一认证中间件:先尝试 PAT,失败再走 JWT。
//
// 判断逻辑:Authorization header 以 "Bearer envvault-" 开头 → PAT,否则 → JWT。
// PAT 认证通过后设置与 JWT 相同格式的 Claims,下游 handler 无感知。
func CombinedAuthMiddleware(patCfg PATConfig, jwtCfg JWTConfig) gin.HandlerFunc {
	jwtHandler := JWTMiddleware(jwtCfg)

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if IsPAT(authHeader) {
			plain := ExtractPAT(authHeader)
			userId, err := validatePAT(c, patCfg, plain)
			if err != nil {
				return // validatePAT 已经 abort
			}
			setPATClaims(c, patCfg, userId)
			return
		}
		// 非 PAT → 走 JWT
		jwtHandler(c)
	}
}

// validatePAT 调用 validator 验证 token,失败时 abort 并返回非 nil error。
func validatePAT(c *gin.Context, cfg PATConfig, plain string) (string, error) {
	if cfg.ValidatePAT == nil {
		logging.Error(c.Request.Context(), "PATMiddleware", "pat validator is not configured")
		abort(c, http.StatusServiceUnavailable, ErrMissingPublicKey)
		return "", ErrMissingPublicKey
	}
	hash := HashPAT(plain)
	userId, err := cfg.ValidatePAT(c.Request.Context(), hash)
	if err != nil {
		logging.Warn(c.Request.Context(), "PATMiddleware", "invalid or expired access token",
			logging.F("error", err))
		abort(c, http.StatusUnauthorized, ErrInvalidPAT)
		return "", err
	}
	return userId, nil
}

// setPATClaims 从 userId 构造 Claims 并写入 gin.Context。
func setPATClaims(c *gin.Context, cfg PATConfig, userId string) {
	name := ""
	if cfg.UserNameLoader != nil {
		name = cfg.UserNameLoader(c.Request.Context(), userId)
	}
	claims := &Claims{
		UserId: userId,
		Name:   name,
	}
	c.Set(claimsContextKey, claims)
	logging.Info(c.Request.Context(), "PATMiddleware", "access token accepted",
		logging.F("user_id", userId))
	c.Next()
}

// ErrInvalidPAT 在 PAT 无效或已过期时由 middleware 返回。
var ErrInvalidPAT = &invalidPATError{}

type invalidPATError struct{}

func (invalidPATError) Error() string { return "invalid or expired access token" }
