package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
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
	EmailVerificationTTL   time.Duration
	AllowedOrigins         []string
	BootstrapAdminUser     string
	BootstrapAdminEmail    string
	BootstrapAdminPass     string
	ExposeResetToken       bool
	ExposeVerificationCode bool
	TurnstileSiteKey       string
	TurnstileSecret        string
	TurnstileRequired      bool
	MaxCloudDocumentSize   int64
	PublicBaseURL          string
	SchedulerEnabled       bool
	ReleaseStorageDir      string
	MaxReleaseArtifactSize int64
	TrustedProxies         []*net.IPNet
	LegalPrivacyURL        string
	LegalTermsURL          string
	LegalCrossBorderURL    string
}

func Load() (Config, error) {
	if err := validateExplicitValues(); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Environment:            env("APP_ENV", "development"),
		HTTPAddr:               env("HTTP_ADDR", ":8080"),
		DatabaseURL:            env("DATABASE_URL", "file:classing.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"),
		AccessTokenTTL:         duration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:        duration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		ResetTokenTTL:          duration("RESET_TOKEN_TTL", 30*time.Minute),
		EmailVerificationTTL:   duration("EMAIL_VERIFICATION_TTL", 10*time.Minute),
		BootstrapAdminUser:     env("BOOTSTRAP_ADMIN_USERNAME", "admin"),
		BootstrapAdminEmail:    strings.ToLower(strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_EMAIL"))),
		BootstrapAdminPass:     os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		ExposeResetToken:       boolean("EXPOSE_RESET_TOKEN", false),
		ExposeVerificationCode: boolean("EXPOSE_VERIFICATION_CODE", false),
		TurnstileSiteKey:       strings.TrimSpace(os.Getenv("TURNSTILE_SITE_KEY")),
		TurnstileSecret:        strings.TrimSpace(os.Getenv("TURNSTILE_SECRET")),
		TurnstileRequired:      boolean("TURNSTILE_REQUIRED", false),
		MaxCloudDocumentSize:   int64(integer("MAX_CLOUD_DOCUMENT_BYTES", 2*1024*1024)),
		PublicBaseURL:          strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		SchedulerEnabled:       boolean("SCHEDULER_ENABLED", true),
		ReleaseStorageDir:      env("RELEASE_STORAGE_DIR", "data/releases"),
		MaxReleaseArtifactSize: int64(integer("MAX_RELEASE_ARTIFACT_BYTES", 250*1024*1024)),
		LegalPrivacyURL:        strings.TrimSpace(os.Getenv("LEGAL_PRIVACY_URL")),
		LegalTermsURL:          strings.TrimSpace(os.Getenv("LEGAL_TERMS_URL")),
		LegalCrossBorderURL:    strings.TrimSpace(os.Getenv("LEGAL_CROSS_BORDER_URL")),
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
	if cfg.Environment == "production" {
		if strings.TrimSpace(os.Getenv("DATABASE_DRIVER")) == "" || strings.TrimSpace(os.Getenv("DATABASE_URL")) == "" {
			return Config{}, errors.New("DATABASE_DRIVER and DATABASE_URL must be explicitly configured in production")
		}
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
	if (cfg.TurnstileSiteKey == "") != (cfg.TurnstileSecret == "") {
		return Config{}, errors.New("TURNSTILE_SITE_KEY and TURNSTILE_SECRET must be configured together")
	}
	if cfg.TurnstileRequired && cfg.TurnstileSecret == "" {
		return Config{}, errors.New("TURNSTILE_SITE_KEY and TURNSTILE_SECRET are required when TURNSTILE_REQUIRED=true")
	}
	if err := validateOptionalURL("LEGAL_PRIVACY_URL", cfg.LegalPrivacyURL); err != nil {
		return Config{}, err
	}
	if err := validateOptionalURL("LEGAL_TERMS_URL", cfg.LegalTermsURL); err != nil {
		return Config{}, err
	}
	if err := validateOptionalURL("LEGAL_CROSS_BORDER_URL", cfg.LegalCrossBorderURL); err != nil {
		return Config{}, err
	}
	trusted, err := parseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, err
	}
	cfg.TrustedProxies = trusted
	cfg.AllowedOrigins = defaultAllowedOrigins(csv("CORS_ALLOWED_ORIGINS"), cfg.PublicBaseURL)
	return cfg, nil
}

func validateOptionalURL(key, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", key)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("%s must use http or https", key)
	}
	return nil
}

func defaultAllowedOrigins(explicit []string, publicBaseURL string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	parsed, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	return []string{parsed.Scheme + "://" + parsed.Host}
}

func validateExplicitValues() error {
	for _, key := range []string{"ACCESS_TOKEN_TTL", "REFRESH_TOKEN_TTL", "RESET_TOKEN_TTL", "EMAIL_VERIFICATION_TTL"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return fmt.Errorf("%s must be a positive duration", key)
			}
		}
	}
	for _, key := range []string{"MAX_CLOUD_DOCUMENT_BYTES", "MAX_RELEASE_ARTIFACT_BYTES"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return fmt.Errorf("%s must be a positive integer", key)
			}
		}
	}
	for _, key := range []string{"EXPOSE_RESET_TOKEN", "EXPOSE_VERIFICATION_CODE", "SCHEDULER_ENABLED", "TURNSTILE_REQUIRED"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			if _, err := strconv.ParseBool(raw); err != nil {
				return fmt.Errorf("%s must be true or false", key)
			}
		}
	}
	return nil
}

func parseTrustedProxies(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []*net.IPNet{
			mustCIDR("127.0.0.0/8"),
			mustCIDR("::1/128"),
		}, nil
	}
	parts := strings.Split(raw, ",")
	result := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES invalid CIDR %q: %w", cidr, err)
		}
		result = append(result, ipNet)
	}
	if len(result) == 0 {
		return nil, errors.New("TRUSTED_PROXIES must contain at least one CIDR, or leave unset for loopback default; use an explicit unreachable value to disable")
	}
	return result, nil
}

func mustCIDR(cidr string) *net.IPNet {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return ipNet
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
