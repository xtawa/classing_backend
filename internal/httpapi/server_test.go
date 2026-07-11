package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestAnnouncementsAndReleaseUploadDownload(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, "sqlite", "file:"+t.TempDir()+"/test.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.BootstrapAdmin(ctx, "admin", "admin@classing.test", "AdminPass123!"); err != nil {
		t.Fatal(err)
	}
	storageDir := filepath.Join(t.TempDir(), "releases")
	cfg := config.Config{Environment: "test", JWTSecret: []byte("01234567890123456789012345678901"), AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, ResetTokenTTL: time.Minute, MaxCloudDocumentSize: 1024 * 1024, ReleaseStorageDir: storageDir, MaxReleaseArtifactSize: 10 * 1024 * 1024}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Classing</title>")}}
	testServer := httptest.NewServer(New(cfg, data, web, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	status, body := client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "admin", "password": "AdminPass123!"})
	if status != http.StatusOK {
		t.Fatalf("admin login: %d %+v", status, body)
	}
	access := body["session"].(map[string]any)["accessToken"].(string)
	status, body = client.request(t, http.MethodPost, "/api/v1/admin/announcements", access, map[string]any{"title": "维护通知", "content": "今晚进行短时维护。", "platform": "ANDROID_MOBILE", "priority": 10, "active": true})
	if status != http.StatusCreated {
		t.Fatalf("create announcement: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodGet, "/api/v1/client/announcements?platform=ANDROID_MOBILE", "", nil)
	if status != http.StatusOK || len(body["announcements"].([]any)) != 1 {
		t.Fatalf("public announcements: %d %+v", status, body)
	}

	apk := buildTestAPK(t)
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	for key, value := range map[string]string{"platform": "ANDROID_MOBILE", "channel": "STABLE", "versionCode": "105", "versionName": "1.0.5", "title": "Classing 1.0.5", "changelog": "修复更新链路", "mandatory": "false", "publish": "true"} {
		_ = writer.WriteField(key, value)
	}
	part, err := writer.CreateFormFile("artifact", "classing-1.0.5.apk")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(apk)
	_ = writer.Close()
	request, _ := http.NewRequest(http.MethodPost, testServer.URL+"/api/v1/admin/releases", &upload)
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := testServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	uploadBody := map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&uploadBody)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload release: %d %+v", response.StatusCode, uploadBody)
	}
	release := uploadBody["release"].(map[string]any)
	releaseID := release["releaseId"].(string)
	if release["status"] != "PUBLISHED" || release["sha256"] == "" {
		t.Fatalf("uploaded release payload: %+v", release)
	}

	status, body = client.request(t, http.MethodGet, "/api/v1/client/releases/latest?platform=ANDROID_MOBILE&channel=STABLE&versionCode=104", "", nil)
	if status != http.StatusOK || body["updateAvailable"] != true {
		t.Fatalf("latest release: %d %+v", status, body)
	}
	download, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/v1/client/releases/"+releaseID+"/download", nil)
	download.Header.Set("Range", "bytes=0-7")
	downloadResponse, err := testServer.Client().Do(download)
	if err != nil {
		t.Fatal(err)
	}
	defer downloadResponse.Body.Close()
	if downloadResponse.StatusCode != http.StatusPartialContent || downloadResponse.Header.Get("Content-Length") != "8" {
		t.Fatalf("range download status=%d headers=%v", downloadResponse.StatusCode, downloadResponse.Header)
	}
	status, _ = client.request(t, http.MethodDelete, "/api/v1/admin/releases/"+releaseID, access, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete release status = %d", status)
	}
	entries, _ := os.ReadDir(storageDir)
	if len(entries) != 0 {
		t.Fatalf("release artifact was not deleted: %+v", entries)
	}
}

func buildTestAPK(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	manifest, err := writer.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manifest.Write([]byte("binary-manifest-placeholder"))
	classes, _ := writer.Create("classes.dex")
	_, _ = classes.Write([]byte("dex-placeholder"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
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
	cfg := config.Config{Environment: "test", JWTSecret: []byte("01234567890123456789012345678901"), AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, ResetTokenTTL: time.Minute, EmailVerificationTTL: time.Minute, ExposeResetToken: true, ExposeVerificationCode: true, MaxCloudDocumentSize: 1024 * 1024}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Classing</title>")}}
	var files fs.FS = web
	testServer := httptest.NewServer(New(cfg, data, files, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	status, body := client.request(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": "alice", "email": "alice@example.com", "password": "UserPass123!"})
	if status != http.StatusAccepted {
		t.Fatalf("request registration verification: %d %+v", status, body)
	}
	challenge := body["challenge"].(map[string]any)
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/register/email/confirm", "", map[string]any{"challengeId": challenge["challengeId"], "verificationCode": body["devVerificationCode"]})
	if status != http.StatusCreated {
		t.Fatalf("confirm registration verification: %d %+v", status, body)
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
	status, body = client.request(t, http.MethodGet, "/api/v1/cloud/official/ping", access, nil)
	if status != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("official cloud ping: %d %+v", status, body)
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

	status, body = client.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", "", map[string]any{"email": "alice@example.com"})
	if status != http.StatusAccepted {
		t.Fatalf("password reset request: %d %+v", status, body)
	}
	resetToken, _ := body["devResetToken"].(string)
	if resetToken == "" {
		t.Fatal("password reset token was not exposed in test mode")
	}
	jobs, total, err := data.ListBriefingJobs(ctx, 10, 0)
	hasResetJob := false
	for _, job := range jobs {
		if job.Channel == "PASSWORD_RESET" {
			hasResetJob = true
		}
	}
	if err != nil || total != 2 || !hasResetJob {
		t.Fatalf("password reset email job: total=%d jobs=%+v err=%v", total, jobs, err)
	}
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/password/reset/confirm", "", map[string]any{"token": resetToken, "newPassword": "UserPass789!"})
	if status != http.StatusOK {
		t.Fatalf("password reset confirm: %d %+v", status, body)
	}
	status, _ = client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "alice", "password": "UserPass789!"})
	if status != http.StatusOK {
		t.Fatalf("reset password login status = %d", status)
	}
}

func TestBriefingRejectsInvalidTimeAndTimezone(t *testing.T) {
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
	testServer := httptest.NewServer(New(cfg, data, web, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	status, body := client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "admin", "password": "AdminPass123!"})
	if status != http.StatusOK {
		t.Fatalf("login: %d %+v", status, body)
	}
	access := body["session"].(map[string]any)["accessToken"].(string)
	for _, request := range []map[string]any{
		{"enabled": true, "channel": "EMAIL", "time": "99:99", "timezone": "Asia/Shanghai"},
		{"enabled": true, "channel": "EMAIL", "time": "20:00", "timezone": "Invalid/Timezone"},
	} {
		status, _ = client.request(t, http.MethodPut, "/api/v1/briefings/daily", access, request)
		if status != http.StatusBadRequest {
			t.Fatalf("invalid briefing request status = %d request=%+v", status, request)
		}
	}
}
