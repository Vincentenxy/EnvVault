package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

const claimsContextKey = "envvault.jwt.claims"

var (
	ErrMissingBearerToken = errors.New("missing bearer token")
	ErrMissingJWTSecret   = errors.New("jwt secret is not configured")
)

type Claims struct {
	StaffUserID string `json:"staffUserId,omitempty"`
	GxjId       string `json:"gxjId,omitempty"`
	StaffNo     string `json:"staffNo,omitempty"`
	Name        string `json:"name,omitempty"`
	JWT         string `json:"jwt,omitempty"`
	Cookie      string `json:"cookie,omitempty"`
	jwt.RegisteredClaims
}

type JWTConfig struct {
	Secret []byte
}

func UserFromContext(c *gin.Context) UserInfo {
	claims := ClaimsFromContext(c)
	return UserInfo{
		StaffUserID: claims.StaffUserID,
		GxjId:       claims.GxjId,
		StaffNo:     claims.StaffNo,
		Name:        claims.Name,
		JWT:         claims.JWT,
		Cookie:      claims.Cookie,
	}
}

func JWTMiddleware(cfg JWTConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(cfg.Secret) == 0 {
			abort(c, http.StatusServiceUnavailable, ErrMissingJWTSecret)
			return
		}

		tokenString, err := bearerToken(c.GetHeader("Authorization"))
		if err != nil {
			abort(c, http.StatusUnauthorized, err)
			return
		}

		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return cfg.Secret, nil
		})
		if err != nil || !token.Valid {
			abort(c, http.StatusUnauthorized, jwt.ErrTokenInvalidClaims)
			return
		}

		c.Set(claimsContextKey, claims)
		c.Next()
	}
}

func ClaimsFromContext(c *gin.Context) *Claims {
	value, ok := c.Get(claimsContextKey)
	if !ok {
		return &Claims{}
	}
	claims, ok := value.(*Claims)
	if !ok {
		return &Claims{}
	}
	return claims
}

func bearerToken(header string) (string, error) {
	if header == "" {
		return "", ErrMissingBearerToken
	}

	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", ErrMissingBearerToken
	}

	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", ErrMissingBearerToken
	}
	return token, nil
}

func abort(c *gin.Context, status int, err error) {
	c.AbortWithStatusJSON(status, gin.H{
		"code": status,
		"msg":  err.Error(),
		"data": nil,
	})
}
