package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment            string
	HTTPAddr               string
	DatabaseDriver         string
	DatabaseURL            string
	JWTSecret              []byte
	AccessTokenTTL         time.Duration
	RefreshTokenTTL        time.Duration
	ResetTokenTTL          time.Duration
	AllowedOrigins         []string
	BootstrapAdminUser     string
	BootstrapAdminEmail    string
	BootstrapAdminPass     string
	ExposeResetToken       bool
	MaxCloudDocumentSize   int64
	PublicBaseURL          string
	SchedulerEnabled       bool
	ReleaseStorageDir      string
	MaxReleaseArtifactSize int64
}

func Load() (Config, error) {
	cfg := Config{
		Environment:            env("APP_ENV", "development"),
		HTTPAddr:               env("HTTP_ADDR", ":8080"),
		DatabaseURL:            env("DATABASE_URL", "file:classing.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"),
		AccessTokenTTL:         duration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:        duration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		ResetTokenTTL:          duration("RESET_TOKEN_TTL", 30*time.Minute),
		AllowedOrigins:         csv("CORS_ALLOWED_ORIGINS"),
		BootstrapAdminUser:     env("BOOTSTRAP_ADMIN_USERNAME", "admin"),
		BootstrapAdminEmail:    strings.ToLower(strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_EMAIL"))),
		BootstrapAdminPass:     os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		ExposeResetToken:       boolean("EXPOSE_RESET_TOKEN", false),
		MaxCloudDocumentSize:   int64(integer("MAX_CLOUD_DOCUMENT_BYTES", 2*1024*1024)),
		PublicBaseURL:          strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		SchedulerEnabled:       boolean("SCHEDULER_ENABLED", true),
		ReleaseStorageDir:      env("RELEASE_STORAGE_DIR", "data/releases"),
		MaxReleaseArtifactSize: int64(integer("MAX_RELEASE_ARTIFACT_BYTES", 250*1024*1024)),
	}

	cfg.DatabaseDriver = strings.ToLower(strings.TrimSpace(os.Getenv("DATABASE_DRIVER")))
	if cfg.DatabaseDriver == "" {
		if strings.HasPrefix(cfg.DatabaseURL, "postgres://") || strings.HasPrefix(cfg.DatabaseURL, "postgresql://") {
			cfg.DatabaseDriver = "pgx"
		} else {
			cfg.DatabaseDriver = "sqlite"
		}
	}
	if cfg.DatabaseDriver != "pgx" && cfg.DatabaseDriver != "sqlite" {
		return Config{}, fmt.Errorf("DATABASE_DRIVER must be pgx or sqlite")
	}

	secret, err := loadSecret()
	if err != nil {
		return Config{}, err
	}
	if len(secret) < 32 {
		if cfg.Environment != "development" && cfg.Environment != "test" {
			return Config{}, errors.New("JWT_SECRET must contain at least 32 bytes outside development")
		}
		secret = []byte("classing-development-secret-change-me")
	}
	cfg.JWTSecret = secret

	if cfg.Environment == "production" && cfg.BootstrapAdminEmail != "" && len(cfg.BootstrapAdminPass) < 12 {
		return Config{}, errors.New("BOOTSTRAP_ADMIN_PASSWORD must contain at least 12 characters in production")
	}
	return cfg, nil
}

func loadSecret() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "base64:") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, "base64:"))
		if err != nil {
			return nil, fmt.Errorf("decode JWT_SECRET: %w", err)
		}
		return decoded, nil
	}
	return []byte(raw), nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func duration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func integer(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func boolean(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func csv(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	return result
}
