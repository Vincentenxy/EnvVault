package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFromPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`
http:
  addr: ":9090"
auth:
  public_key: "test-public-key"
security:
  encryption_key: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
database:
  host: "127.0.0.1"
  port: 5432
  user: "admin"
  password: "123456"
  name: "envvault"
  ssl_mode: "disable"
  max_open_conns: 10
  max_idle_conns: 2
  conn_max_lifetime: "10m"
  connect_timeout: "3s"
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	if cfg.HTTP.Addr != ":9090" {
		t.Fatalf("HTTP.Addr = %q, want :9090", cfg.HTTP.Addr)
	}
	if !cfg.Auth.Enabled {
		t.Fatal("Auth.Enabled = false, want true")
	}
	if cfg.Database.Name != "envvault" {
		t.Fatalf("Database.Name = %q, want envvault", cfg.Database.Name)
	}
	if !strings.Contains(cfg.Database.DSN(), "sslmode=disable") {
		t.Fatalf("DSN() = %q, want sslmode", cfg.Database.DSN())
	}
}

func TestLoadFromPathAllowsAuthDisabled(t *testing.T) {
	t.Setenv("ENVVAULT_AUTH_ENABLED", "false")
	t.Setenv("ENVVAULT_AUTH_DEV_USER_ID", "local-user")

	cfg, err := LoadFromPath("")
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	if cfg.Auth.Enabled {
		t.Fatal("Auth.Enabled = true, want false")
	}
	if cfg.Auth.DevUserId != "local-user" {
		t.Fatalf("Auth.DevUserId = %q, want local-user", cfg.Auth.DevUserId)
	}
}

func TestLoadFromPathAllowsEnvironmentOverride(t *testing.T) {
	t.Setenv("ENVVAULT_DATABASE_HOST", "db.internal")
	t.Setenv("ENVVAULT_REDIS_ADDRS", "redis-1:6379,redis-2:6379")

	cfg, err := LoadFromPath("")
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	if cfg.Database.Host != "db.internal" {
		t.Fatalf("Database.Host = %q, want db.internal", cfg.Database.Host)
	}
	if len(cfg.Redis.Addrs) != 2 || cfg.Redis.Addrs[1] != "redis-2:6379" {
		t.Fatalf("Redis.Addrs = %#v, want two redis addrs", cfg.Redis.Addrs)
	}
}
