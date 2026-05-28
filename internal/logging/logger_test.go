package logging

import "testing"

func TestFormatValueMasksSensitiveFields(t *testing.T) {
	tests := []Field{
		F("value", "plain-secret"),
		F("jwt", "token"),
		F("cookie", "session"),
		F("database_password", "password"),
		F("encryption_secret", "secret"),
	}

	for _, tt := range tests {
		if got := formatValue(tt); got != "***" {
			t.Fatalf("formatValue(%q) = %q, want ***", tt.Key, got)
		}
	}
}

func TestFormatValueAllowsNonSensitiveFields(t *testing.T) {
	if got := formatValue(F("key", "DATABASE_URL")); got != "DATABASE_URL" {
		t.Fatalf("formatValue(key) = %q, want DATABASE_URL", got)
	}
}
