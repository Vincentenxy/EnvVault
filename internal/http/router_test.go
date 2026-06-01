package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"envVault/internal/config"
)

func TestCoreSecretRevealRouteIsRegistered(t *testing.T) {
	router := NewRouter(Dependencies{
		Config: config.Config{
			HTTP: config.HTTPConfig{
				RequestIDHeader: "x-request-id",
			},
			Auth: config.AuthConfig{
				Enabled:     false,
				DevUserID:   "dev-user",
				DevUserName: "Dev User",
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/secret/reveal", strings.NewReader(`{"id":"secret-id"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("route returned 404, want registered route")
	}
}

func TestCoreRoutesAreRegistered(t *testing.T) {
	router := NewRouter(Dependencies{
		Config: config.Config{
			HTTP: config.HTTPConfig{
				RequestIDHeader: "x-request-id",
			},
			Auth: config.AuthConfig{
				Enabled:         false,
				DevTokenEnabled: true,
				DevUserID:       "dev-user",
				DevUserName:     "Dev User",
			},
		},
	})

	expected := map[string]struct{}{
		"GET /healthz":                {},
		"GET /api/v1/readyz":          {},
		"POST /api/v1/auth/dev/token": {},
		"GET /api/v1/me":              {},
		"POST /api/v1/org/list":       {},
		"POST /api/v1/org/create":     {},
		"POST /api/v1/org/info":       {},
		"POST /api/v1/org/update":     {},
		"POST /api/v1/org/delete":     {},
		"POST /api/v1/project/list":   {},
		"POST /api/v1/project/create": {},
		"POST /api/v1/project/info":   {},
		"POST /api/v1/project/update": {},
		"POST /api/v1/project/delete": {},
		"POST /api/v1/env/list":       {},
		"POST /api/v1/env/create":     {},
		"POST /api/v1/env/info":       {},
		"POST /api/v1/env/update":     {},
		"POST /api/v1/env/delete":     {},
		"POST /api/v1/folder/list":    {},
		"POST /api/v1/folder/create":  {},
		"POST /api/v1/folder/info":    {},
		"POST /api/v1/folder/update":  {},
		"POST /api/v1/folder/delete":  {},
		"POST /api/v1/secret/list":    {},
		"POST /api/v1/secret/search":  {},
		"POST /api/v1/secret/create":  {},
		"POST /api/v1/secret/info":    {},
		"POST /api/v1/secret/reveal":  {},
		"POST /api/v1/secret/update":  {},
		"POST /api/v1/secret/delete":  {},
		"POST /api/v1/audit/list":     {},
	}

	for _, route := range router.Routes() {
		delete(expected, route.Method+" "+route.Path)
	}
	if len(expected) > 0 {
		t.Fatalf("missing routes: %#v", expected)
	}
}
