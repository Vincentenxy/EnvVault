package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestClaims_IsRevokedBy(t *testing.T) {
	issued := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		c    *Claims
		tva  time.Time
		want bool
	}{
		{
			name: "no tva set",
			c:    &Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issued)}},
			tva:  time.Time{},
			want: false,
		},
		{
			name: "iat before tva → revoked",
			c:    &Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issued)}},
			tva:  issued.Add(time.Minute),
			want: true,
		},
		{
			name: "iat equal tva → not revoked",
			c:    &Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issued)}},
			tva:  issued,
			want: false,
		},
		{
			name: "iat after tva → not revoked",
			c:    &Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issued)}},
			tva:  issued.Add(-time.Minute),
			want: false,
		},
		{
			name: "iat missing → not revoked (conservative)",
			c:    &Claims{},
			tva:  issued,
			want: false,
		},
		{
			name: "nil claims → not revoked",
			c:    nil,
			tva:  issued,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.IsRevokedBy(tc.tva); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJWTMiddleware_RevokedByTokensValidAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	privateKey, publicKeyPEM := testRSAKeyPair(t)

	iat := time.Now().Add(-time.Hour) // 1h 前签发
	claims := Claims{
		UserId: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			IssuedAt:  jwt.NewNumericDate(iat),
			ExpiresAt: jwt.NewNumericDate(iat.Add(2 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	// cache: tokensValidAfter 在 iat 之后 → 应被 401
	cache := NewTokensCache(TokensCacheOptions{})
	cache.Set("user-1", iat.Add(time.Minute))

	router := gin.New()
	router.Use(JWTMiddleware(JWTConfig{PublicKey: publicKeyPEM, TokensCache: cache}))
	router.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestJWTMiddleware_AllowedWhenTokensValidAfterBeforeIAT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	privateKey, publicKeyPEM := testRSAKeyPair(t)

	iat := time.Now().Add(-time.Hour)
	claims := Claims{
		UserId: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			IssuedAt:  jwt.NewNumericDate(iat),
			ExpiresAt: jwt.NewNumericDate(iat.Add(2 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	// cache: tokensValidAfter 在 iat 之前 → 200
	cache := NewTokensCache(TokensCacheOptions{})
	cache.Set("user-1", iat.Add(-time.Minute))

	router := gin.New()
	router.Use(JWTMiddleware(JWTConfig{PublicKey: publicKeyPEM, TokensCache: cache}))
	router.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestJWTMiddleware_NoCache_Allows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	privateKey, publicKeyPEM := testRSAKeyPair(t)

	iat := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, Claims{
		UserId: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(iat),
			ExpiresAt: jwt.NewNumericDate(iat.Add(time.Hour)),
		},
	})
	tokenString, _ := token.SignedString(privateKey)

	// TokensCache nil → 跳过强制登出校验
	router := gin.New()
	router.Use(JWTMiddleware(JWTConfig{PublicKey: publicKeyPEM}))
	router.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when cache nil, got %d", rec.Code)
	}
}
