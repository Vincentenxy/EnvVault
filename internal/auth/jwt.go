package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"

	"envVault/internal/http/response"
	"envVault/internal/logging"
)

const claimsContextKey = "envvault.jwt.claims"

var (
	ErrMissingBearerToken = errors.New("missing bearer token")
	ErrMissingPublicKey   = errors.New("jwt public key is not configured")
	ErrInvalidPublicKey   = errors.New("jwt public key is invalid")
	ErrMissingPrivateKey  = errors.New("jwt private key is not configured")
	ErrInvalidPrivateKey  = errors.New("jwt private key is invalid")
)

type Claims struct {
	UserId string `json:"userId,omitempty"`
	Name   string `json:"name,omitempty"`
	JWT    string `json:"jwt,omitempty"`
	Cookie string `json:"cookie,omitempty"`
	jwt.RegisteredClaims
}

type JWTConfig struct {
	PublicKey string
}

func UserFromContext(c *gin.Context) UserInfo {
	claims := ClaimsFromContext(c)
	return UserInfo{
		UserId: claims.UserId,
		Name:   claims.Name,
		JWT:    claims.JWT,
		Cookie: claims.Cookie,
	}
}

func JWTMiddleware(cfg JWTConfig) gin.HandlerFunc {
	publicKey, keyErr := parsePublicKey(cfg.PublicKey)
	return func(c *gin.Context) {
		if keyErr != nil {
			logging.Error(c.Request.Context(), "JWTMiddleware", "jwt public key is not configured or invalid")
			abort(c, http.StatusServiceUnavailable, keyErr)
			return
		}

		tokenString, err := bearerToken(c.GetHeader("Authorization"))
		if err != nil {
			logging.Warn(c.Request.Context(), "JWTMiddleware", "missing bearer token")
			abort(c, http.StatusUnauthorized, err)
			return
		}

		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
			if !matchesPublicKeySigningMethod(token.Method, publicKey) {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return publicKey, nil
		})
		if err != nil || !token.Valid {
			logging.Warn(c.Request.Context(), "JWTMiddleware", "invalid jwt token", logging.F("error", err))
			abort(c, http.StatusUnauthorized, jwt.ErrTokenInvalidClaims)
			return
		}

		logging.Info(c.Request.Context(), "JWTMiddleware", "jwt token accepted", logging.F("user_id", claims.UserId))
		c.Set(claimsContextKey, claims)
		c.Next()
	}
}

func StaticUserMiddleware(user UserInfo) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := &Claims{
			UserId: user.UserId,
			Name:   user.Name,
		}
		c.Set(claimsContextKey, claims)
		c.Next()
	}
}

func SignToken(privateKeyPEM string, claims Claims) (string, error) {
	privateKey, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	method, err := signingMethodForPrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	return jwt.NewWithClaims(method, claims).SignedString(privateKey)
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

func parsePublicKey(value string) (any, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\n`, "\n"))
	if value == "" {
		return nil, ErrMissingPublicKey
	}
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, ErrInvalidPublicKey
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		return cert.PublicKey, nil
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, ErrInvalidPublicKey
}

func parsePrivateKey(value string) (any, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\n`, "\n"))
	if value == "" {
		return nil, ErrMissingPrivateKey
	}
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, ErrInvalidPrivateKey
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, ErrInvalidPrivateKey
}

func matchesPublicKeySigningMethod(method jwt.SigningMethod, publicKey any) bool {
	switch publicKey.(type) {
	case *rsa.PublicKey:
		_, rsaOK := method.(*jwt.SigningMethodRSA)
		_, pssOK := method.(*jwt.SigningMethodRSAPSS)
		return rsaOK || pssOK
	case *ecdsa.PublicKey:
		_, ok := method.(*jwt.SigningMethodECDSA)
		return ok
	case ed25519.PublicKey:
		_, ok := method.(*jwt.SigningMethodEd25519)
		return ok
	default:
		return false
	}
}

func signingMethodForPrivateKey(privateKey any) (jwt.SigningMethod, error) {
	switch key := privateKey.(type) {
	case *rsa.PrivateKey:
		return jwt.SigningMethodRS256, nil
	case *ecdsa.PrivateKey:
		switch key.Curve.Params().BitSize {
		case 256:
			return jwt.SigningMethodES256, nil
		case 384:
			return jwt.SigningMethodES384, nil
		case 521:
			return jwt.SigningMethodES512, nil
		default:
			return nil, ErrInvalidPrivateKey
		}
	case ed25519.PrivateKey:
		return jwt.SigningMethodEdDSA, nil
	default:
		return nil, ErrInvalidPrivateKey
	}
}

func abort(c *gin.Context, status int, err error) {
	c.AbortWithStatusJSON(status, response.Body{
		Code: status,
		Msg:  err.Error(),
		Data: nil,
	})
}
