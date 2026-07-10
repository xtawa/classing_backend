package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xtawa/classing-backend/internal/config"
	"github.com/xtawa/classing-backend/internal/store"
)

type testClient struct {
	base   string
	client *http.Client
}

func (c testClient) request(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	request, err := http.NewRequest(method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result := map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&result)
	return response.StatusCode, result
}

func TestAccountMembershipAndSessionRevocation(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, "sqlite", "file:"+t.TempDir()+"/test.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.BootstrapAdmin(ctx, "admin", "admin@classing.test", "AdminPass123!"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Environment: "test", JWTSecret: []byte("01234567890123456789012345678901"), AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, ResetTokenTTL: time.Minute, MaxCloudDocumentSize: 1024 * 1024}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Classing</title>")}}
	var files fs.FS = web
	testServer := httptest.NewServer(New(cfg, data, files, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	status, body := client.request(t, http.MethodPost, "/api/v1/auth/register", "", map[string]any{"username": "alice", "email": "alice@example.com", "password": "UserPass123!"})
	if status != http.StatusCreated {
		t.Fatalf("register: %d %+v", status, body)
	}
	session := body["session"].(map[string]any)
	access := session["accessToken"].(string)
	refresh := session["refreshToken"].(string)

	status, body = client.request(t, http.MethodPost, "/api/v1/timetables", access, map[string]any{"name": "Autumn", "timezone": "Asia/Shanghai", "weekCount": 20, "document": map[string]any{"lessons": []any{}}})
	if status != http.StatusCreated {
		t.Fatalf("create timetable: %d %+v", status, body)
	}

	status, adminBody := client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "admin@classing.test", "password": "AdminPass123!"})
	if status != http.StatusOK {
		t.Fatalf("admin login: %d %+v", status, adminBody)
	}
	adminAccess := adminBody["session"].(map[string]any)["accessToken"].(string)
	status, codesBody := client.request(t, http.MethodPost, "/api/v1/admin/redeem-codes/generate", adminAccess, map[string]any{"codeType": "UNIQUE", "count": 1, "grantDays": 30, "maxRedemptions": 1})
	if status != http.StatusCreated {
		t.Fatalf("generate code: %d %+v", status, codesBody)
	}
	code := codesBody["codes"].([]any)[0].(map[string]any)["code"].(string)
	status, body = client.request(t, http.MethodPost, "/api/v1/membership/redeem", access, map[string]any{"code": code})
	if status != http.StatusOK || body["membership"].(map[string]any)["isMember"] != true {
		t.Fatalf("redeem: %d %+v", status, body)
	}

	status, body = client.request(t, http.MethodPut, "/api/v1/account/password", access, map[string]any{"currentPassword": "UserPass123!", "newPassword": "UserPass456!"})
	if status != http.StatusOK {
		t.Fatalf("password change: %d %+v", status, body)
	}
	status, _ = client.request(t, http.MethodGet, "/api/v1/account/me", access, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("old access token status = %d", status)
	}
	status, _ = client.request(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refreshToken": refresh})
	if status != http.StatusUnauthorized {
		t.Fatalf("old refresh token status = %d", status)
	}
	status, _ = client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "alice@example.com", "password": "UserPass456!"})
	if status != http.StatusOK {
		t.Fatalf("new password login status = %d", status)
	}
}
