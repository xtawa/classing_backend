package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xtawa/classing-backend/internal/config"
	"github.com/xtawa/classing-backend/internal/store"
)

func newSecurityTestServer(t *testing.T, allowedOrigins []string) (*httptest.Server, testClient) {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, "sqlite", "file:"+t.TempDir()+"/test.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if _, err := data.BootstrapAdmin(ctx, "admin", "admin@classing.test", "AdminPass123!"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Environment: "test", JWTSecret: []byte("01234567890123456789012345678901"),
		AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, ResetTokenTTL: time.Minute,
		EmailVerificationTTL: time.Minute, ExposeResetToken: true, ExposeVerificationCode: true,
		MaxCloudDocumentSize: 1024 * 1024, AllowedOrigins: allowedOrigins,
	}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Classing</title>")}}
	testServer := httptest.NewServer(New(cfg, data, web, slog.Default()).Handler())
	t.Cleanup(func() { testServer.Close() })
	return testServer, testClient{base: testServer.URL, client: testServer.Client()}
}

func TestSecurityHeaders(t *testing.T) {
	_, client := newSecurityTestServer(t, nil)
	resp, err := client.client.Get(client.base + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: expected 200, got %d", resp.StatusCode)
	}
	checks := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
		"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self' https://challenges.cloudflare.com; connect-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; frame-ancestors 'none'; base-uri 'self'; form-action 'self'",
	}
	for header, want := range checks {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header is missing")
	}
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS should not be set without X-Forwarded-Proto: https")
	}
}

func TestHSTSConditionalOnHTTPS(t *testing.T) {
	_, client := newSecurityTestServer(t, nil)
	req, _ := http.NewRequest(http.MethodGet, client.base+"/health/live", nil)
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		t.Fatalf("HSTS without https: expected empty, got %q", hsts)
	}
	req, _ = http.NewRequest(http.MethodGet, client.base+"/health/live", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err = client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := "max-age=31536000; includeSubDomains"
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != want {
		t.Fatalf("HSTS with https: expected %q, got %q", want, hsts)
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	_, client := newSecurityTestServer(t, []string{"https://app.example.com"})
	req, _ := http.NewRequest(http.MethodGet, client.base+"/health/live", nil)
	req.Header.Set("Origin", "https://app.example.com")
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want https://app.example.com", got)
	}
	if got := resp.Header.Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Errorf("Allow-Headers missing Authorization: %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Allow-Methods missing POST: %q", got)
	}
}

func TestCORSDeniedOrigin(t *testing.T) {
	_, client := newSecurityTestServer(t, []string{"https://app.example.com"})
	req, _ := http.NewRequest(http.MethodGet, client.base+"/health/live", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin for denied origin = %q, want empty", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "" {
		t.Errorf("Allow-Headers for denied origin = %q, want empty", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	_, client := newSecurityTestServer(t, []string{"https://app.example.com"})
	req, _ := http.NewRequest(http.MethodOptions, client.base+"/api/v1/auth/login", nil)
	req.Header.Set("Origin", "https://app.example.com")
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status: expected 204, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want https://app.example.com", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") || !strings.Contains(got, "Idempotency-Key") {
		t.Errorf("Allow-Headers missing Authorization or Idempotency-Key: %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") || !strings.Contains(got, "DELETE") {
		t.Errorf("Allow-Methods missing POST or DELETE: %q", got)
	}
}

func TestJSONBodySizeLimit(t *testing.T) {
	_, client := newSecurityTestServer(t, nil)
	oversized := bytes.Repeat([]byte("a"), 4*1024*1024+100)
	body := append([]byte(`{"identifier":"`), oversized...)
	body = append(body, []byte(`"}`)...)
	req, _ := http.NewRequest(http.MethodPost, client.base+"/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON body: expected 400 or 413, got %d", resp.StatusCode)
	}
}

func TestRequestIDLengthLimit(t *testing.T) {
	_, client := newSecurityTestServer(t, nil)
	validID := "req_abcdefghijklmnopqrstuvwxyz012345"
	req, _ := http.NewRequest(http.MethodGet, client.base+"/health/live", nil)
	req.Header.Set("X-Request-ID", validID)
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got != validID {
		t.Errorf("valid X-Request-ID: expected %q, got %q", validID, got)
	}
	oversized := strings.Repeat("x", maxHeaderIDLen+1)
	req, _ = http.NewRequest(http.MethodGet, client.base+"/health/live", nil)
	req.Header.Set("X-Request-ID", oversized)
	resp, err = client.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got := resp.Header.Get("X-Request-ID")
	if got == oversized {
		t.Error("oversized X-Request-ID was echoed back instead of being replaced")
	}
	if !strings.HasPrefix(got, "req_") {
		t.Errorf("oversized X-Request-ID replacement = %q, want prefix req_", got)
	}
}

func TestIdempotencyKeyLengthLimit(t *testing.T) {
	_, client := newSecurityTestServer(t, nil)
	access := registerTestUser(t, client, "alice", "alice@idem.test", "AlicePass123!")
	doc := map[string]any{"format": "classing_cloud_sync_v2", "updatedAt": 0, "records": map[string]any{}, "changes": []any{}, "devices": []any{}}
	oversizedKey := strings.Repeat("k", maxHeaderIDLen+1)
	status, body := client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", access, doc, map[string]string{"Idempotency-Key": oversizedKey, "If-Match": `"0"`})
	if status != http.StatusBadRequest || body["code"] != "IDEMPOTENCY_KEY_TOO_LONG" {
		t.Fatalf("oversized idempotency key: expected 400 IDEMPOTENCY_KEY_TOO_LONG, got %d %+v", status, body)
	}
	validKey := strings.Repeat("k", maxHeaderIDLen)
	status, body = client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", access, doc, map[string]string{"Idempotency-Key": validKey, "If-Match": `"0"`})
	if status == http.StatusBadRequest && body["code"] == "IDEMPOTENCY_KEY_TOO_LONG" {
		t.Fatalf("valid-length (128) idempotency key was rejected as too long")
	}
}
