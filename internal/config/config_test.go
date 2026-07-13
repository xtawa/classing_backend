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

func TestLoadDefaultsAllowedOriginFromPublicBaseURL(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://api-classing.underflo.ink/app")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "https://api-classing.underflo.ink" {
		t.Fatalf("AllowedOrigins = %#v", cfg.AllowedOrigins)
	}
}

func TestLoadKeepsExplicitAllowedOrigins(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://api-classing.underflo.ink")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://console.example.com, https://ops.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://console.example.com", "https://ops.example.com"}
	if strings.Join(cfg.AllowedOrigins, ",") != strings.Join(want, ",") {
		t.Fatalf("AllowedOrigins = %#v, want %#v", cfg.AllowedOrigins, want)
	}
}
