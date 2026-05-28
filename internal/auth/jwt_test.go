package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

func TestJWTMiddlewareAcceptsValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	secret := []byte("test-secret")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserId: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "user-1",
		},
	})
	tokenString, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	router := gin.New()
	router.Use(JWTMiddleware(JWTConfig{Secret: secret}))
	router.GET("/me", func(c *gin.Context) {
		claims := ClaimsFromContext(c)
		c.JSON(http.StatusOK, gin.H{
			"subject": claims.Subject,
			"userId":  claims.UserId,
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestJWTMiddlewareRejectsMissingSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(JWTMiddleware(JWTConfig{}))
	router.GET("/me", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
