package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsInvalidExplicitValues(t *testing.T) {
	t.Setenv("ACCESS_TOKEN_TTL", "not-a-duration")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ACCESS_TOKEN_TTL") {
		t.Fatalf("expected strict duration error, got %v", err)
	}
}

func TestLoadRequiresExplicitProductionDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_DRIVER") {
		t.Fatalf("expected explicit database error, got %v", err)
	}
}

func TestLoadRequiresTurnstileCredentialsWhenRequired(t *testing.T) {
	t.Setenv("TURNSTILE_REQUIRED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TURNSTILE_SITE_KEY") {
		t.Fatalf("expected required Turnstile credentials error, got %v", err)
	}
}
