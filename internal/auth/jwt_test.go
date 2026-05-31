package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

func TestJWTMiddlewareAcceptsValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	privateKey, publicKeyPEM := testRSAKeyPair(t)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, Claims{
		UserId: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "user-1",
		},
	})
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	router := gin.New()
	router.Use(JWTMiddleware(JWTConfig{PublicKey: publicKeyPEM}))
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

func TestJWTMiddlewareRejectsHMACToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	_, publicKeyPEM := testRSAKeyPair(t)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{UserId: "user-1"})
	tokenString, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	router := gin.New()
	router.Use(JWTMiddleware(JWTConfig{PublicKey: publicKeyPEM}))
	router.GET("/me", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestJWTMiddlewareRejectsMissingPublicKey(t *testing.T) {
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

func TestSignTokenUsesPrivateKey(t *testing.T) {
	privateKey, publicKeyPEM := testRSAKeyPair(t)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	tokenString, err := SignToken(string(privateKeyPEM), Claims{UserId: "user-1"})
	if err != nil {
		t.Fatalf("SignToken() error = %v", err)
	}

	claims := &Claims{}
	publicKey, err := parsePublicKey(publicKeyPEM)
	if err != nil {
		t.Fatalf("parsePublicKey() error = %v", err)
	}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		return publicKey, nil
	})
	if err != nil || !token.Valid {
		t.Fatalf("ParseWithClaims() token valid = %v, err = %v", token.Valid, err)
	}
	if claims.UserId != "user-1" {
		t.Fatalf("claims.UserId = %q, want user-1", claims.UserId)
	}
}

func testRSAKeyPair(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	publicKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKeyDER})
	return privateKey, string(publicKeyPEM)
}
