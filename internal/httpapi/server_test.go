package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xtawa/classing-backend/internal/config"
	"github.com/xtawa/classing-backend/internal/model"
	"github.com/xtawa/classing-backend/internal/store"
)

type testClient struct {
	base   string
	client *http.Client
}

func TestReadyReportsMailDegradedAndShutdown(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, "sqlite", "file:"+t.TempDir()+"/health.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	cfg := config.Config{Environment: "test", JWTSecret: []byte("01234567890123456789012345678901"), AccessTokenTTL: time.Minute}
	api := New(cfg, data, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, slog.Default())
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	client := testClient{base: server.URL, client: server.Client()}
	status, body := client.request(t, http.MethodGet, "/health/ready", "", nil)
	if status != http.StatusOK || body["status"] != "ready" || body["checks"].(map[string]any)["mail"] != "degraded" {
		t.Fatalf("unexpected degraded readiness: %d %+v", status, body)
	}
	api.MarkShuttingDown()
	status, body = client.request(t, http.MethodGet, "/health/ready", "", nil)
	if status != http.StatusServiceUnavailable || body["status"] != "not_ready" {
		t.Fatalf("shutdown should be not ready: %d %+v", status, body)
	}
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
	return c.requestWithConsentInjection(t, method, path, token, body, nil, true)
}

func (c testClient) requestRaw(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	return c.requestWithConsentInjection(t, method, path, token, body, nil, false)
}

func (c testClient) requestWithHeaders(t *testing.T, method, path, token string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	return c.requestWithConsentInjection(t, method, path, token, body, headers, true)
}

func (c testClient) requestWithHeadersRaw(t *testing.T, method, path, token string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	return c.requestWithConsentInjection(t, method, path, token, body, headers, false)
}

func (c testClient) requestWithConsentInjection(t *testing.T, method, path, token string, body any, headers map[string]string, injectConsent bool) (int, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		if injectConsent {
			body = withTestConsent(path, body)
		}
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
	for key, value := range headers {
		request.Header.Set(key, value)
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

func withTestConsent(path string, body any) any {
	if path != "/api/v1/auth/login" && path != "/api/v1/auth/register/email/request" && path != "/api/v1/auth/register/email/confirm" {
		return body
	}
	payload, ok := body.(map[string]any)
	if !ok {
		return body
	}
	if _, exists := payload["consent"]; exists {
		return body
	}
	copy := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		copy[key] = value
	}
	copy["consent"] = map[string]any{
		"privacyPolicy":       true,
		"termsOfService":      true,
		"crossBorderTransfer": true,
		"acceptedAt":          time.Now().UnixMilli(),
		"client":              "test",
	}
	return copy
}

func TestAuthConsentRequiredAndLegalConfig(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, "sqlite", "file:"+t.TempDir()+"/test.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.BootstrapAdmin(ctx, "admin", "admin@classing.test", "AdminPass123!"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Environment:          "test",
		JWTSecret:            []byte("01234567890123456789012345678901"),
		AccessTokenTTL:       time.Minute,
		RefreshTokenTTL:      time.Hour,
		EmailVerificationTTL: time.Minute,
		LegalPrivacyURL:      "https://legal.example/privacy",
		LegalTermsURL:        "https://legal.example/terms",
		LegalCrossBorderURL:  "https://legal.example/cross-border",
	}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Classing</title>")}}
	testServer := httptest.NewServer(New(cfg, data, web, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	status, body := client.request(t, http.MethodGet, "/api/v1/auth/registration/config", "", nil)
	if status != http.StatusOK {
		t.Fatalf("registration config: %d %+v", status, body)
	}
	urls := body["legalAgreementUrls"].(map[string]any)
	if urls["privacyPolicy"] != cfg.LegalPrivacyURL || urls["termsOfService"] != cfg.LegalTermsURL || urls["crossBorderTransfer"] != cfg.LegalCrossBorderURL {
		t.Fatalf("legal agreement urls not exposed: %+v", urls)
	}

	status, body = client.requestRaw(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "admin", "password": "AdminPass123!"})
	if status != http.StatusBadRequest || body["code"] != "AUTH_CONSENT_REQUIRED" {
		t.Fatalf("login without consent: %d %+v", status, body)
	}
	status, body = client.requestRaw(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": "alice", "email": "alice@test.local", "password": "AlicePass123!"})
	if status != http.StatusBadRequest || body["code"] != "AUTH_CONSENT_REQUIRED" {
		t.Fatalf("register without consent: %d %+v", status, body)
	}
}

func TestAccountDeleteSoftDeletesAndRevokesAllSessions(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, "sqlite", "file:"+t.TempDir()+"/test.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	cfg := config.Config{Environment: "test", JWTSecret: []byte("01234567890123456789012345678901"), AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, EmailVerificationTTL: time.Minute, ExposeVerificationCode: true, MaxCloudDocumentSize: 1024 * 1024}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Classing</title>")}}
	testServer := httptest.NewServer(New(cfg, data, web, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	firstAccess := registerTestUser(t, client, "alice", "alice@delete.test", "AlicePass123!")
	status, body := client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "alice@delete.test", "password": "AlicePass123!"})
	if status != http.StatusOK {
		t.Fatalf("second login: %d %+v", status, body)
	}
	secondAccess := body["session"].(map[string]any)["accessToken"].(string)
	secondRefresh := body["session"].(map[string]any)["refreshToken"].(string)

	status, body = client.request(t, http.MethodPost, "/api/v1/account/delete", firstAccess, map[string]any{"currentPassword": "WrongPass123!", "confirm": "DELETE"})
	if status != http.StatusForbidden || body["code"] != "ACCOUNT_PASSWORD_CURRENT_INVALID" {
		t.Fatalf("delete with wrong password: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodPost, "/api/v1/account/delete", firstAccess, map[string]any{"currentPassword": "AlicePass123!", "confirm": "DELETE"})
	if status != http.StatusOK || body["sessionsRevoked"] != true {
		t.Fatalf("delete account: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodGet, "/api/v1/account/me", secondAccess, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("second access survived account deletion: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refreshToken": secondRefresh})
	if status != http.StatusUnauthorized {
		t.Fatalf("second refresh survived account deletion: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "alice@delete.test", "password": "AlicePass123!"})
	if status != http.StatusUnauthorized {
		t.Fatalf("deleted account login succeeded: %d %+v", status, body)
	}
	users, total, err := data.ListUsers(ctx, 10, 0, "delete")
	if err != nil || total != 1 {
		t.Fatalf("list deleted user: total=%d users=%+v err=%v", total, users, err)
	}
	if users[0].Status != model.StatusDeleted || strings.Contains(users[0].Email, "alice@delete.test") || strings.Contains(users[0].Username, "alice") {
		t.Fatalf("user was not soft-deleted and anonymized: %+v", users[0])
	}
}

func TestAdminDeletesPendingUserAndReleasesIdentity(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, "sqlite", "file:"+t.TempDir()+"/test.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.BootstrapAdmin(ctx, "admin", "admin@classing.test", "AdminPass123!"); err != nil {
		t.Fatal(err)
	}
	admin, err := data.UserByIdentifier(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Environment: "test", JWTSecret: []byte("01234567890123456789012345678901"), AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, EmailVerificationTTL: time.Minute, ExposeVerificationCode: true, MaxCloudDocumentSize: 1024 * 1024}
	testServer := httptest.NewServer(New(cfg, data, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	status, body := client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "admin", "password": "AdminPass123!"})
	if status != http.StatusOK {
		t.Fatalf("admin login: %d %+v", status, body)
	}
	adminAccess := body["session"].(map[string]any)["accessToken"].(string)
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": "pending", "email": "pending@test.local", "password": "PendingPass123!"})
	if status != http.StatusAccepted {
		t.Fatalf("create pending user: %d %+v", status, body)
	}
	pending, err := data.UserByIdentifier(ctx, "pending@test.local")
	if err != nil || pending.Status != model.StatusPending {
		t.Fatalf("pending user missing: user=%+v err=%v", pending, err)
	}
	status, body = client.request(t, http.MethodDelete, "/api/v1/admin/users/"+pending.ID, adminAccess, nil)
	if status != http.StatusOK || body["success"] != true {
		t.Fatalf("admin delete pending user: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": "pending", "email": "pending@test.local", "password": "PendingPass123!"})
	if status != http.StatusAccepted {
		t.Fatalf("deleted identity was not released: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodDelete, "/api/v1/admin/users/"+admin.ID, adminAccess, nil)
	if status != http.StatusBadRequest || body["code"] != "ADMIN_SELF_DELETE" {
		t.Fatalf("admin self-delete not rejected: %d %+v", status, body)
	}
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
	status, firstRefresh := client.request(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refreshToken": refresh})
	if status != http.StatusOK {
		t.Fatalf("first refresh: %d %+v", status, firstRefresh)
	}
	status, replayedRefresh := client.request(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refreshToken": refresh})
	if status != http.StatusOK {
		t.Fatalf("replayed refresh: %d %+v", status, replayedRefresh)
	}
	firstRefreshSession := firstRefresh["session"].(map[string]any)
	replayedRefreshSession := replayedRefresh["session"].(map[string]any)
	if !reflect.DeepEqual(firstRefreshSession, replayedRefreshSession) {
		t.Fatalf("refresh replay returned a different replacement session: first=%+v replay=%+v", firstRefreshSession, replayedRefreshSession)
	}
	access = firstRefreshSession["accessToken"].(string)
	refresh = firstRefreshSession["refreshToken"].(string)

	status, body = client.request(t, http.MethodPost, "/api/v1/timetables", access, map[string]any{"name": "Autumn", "timezone": "Asia/Shanghai", "weekCount": 20, "document": map[string]any{"lessons": []any{}}})
	if status != http.StatusCreated {
		t.Fatalf("create timetable: %d %+v", status, body)
	}

	status, body = client.request(t, http.MethodGet, "/api/v1/cloud/official/ping", access, nil)
	if status != http.StatusOK || body["canSyncSettings"] != true || body["canSyncTimetable"] != false {
		t.Fatalf("non-member official cloud ping: %d %+v", status, body)
	}
	settingsDoc := map[string]any{
		"format":    "classing_cloud_sync_v2",
		"updatedAt": float64(1000),
		"records": map[string]any{
			"mobile.settings":   []any{map[string]any{"id": "showWeekend", "payload": `{"value":false}`, "version": map[string]any{"counter": float64(1), "deviceId": "test", "changedAt": float64(1000)}}},
			"timetable.lessons": []any{map[string]any{"id": "lesson-1", "payload": `{"id":"lesson-1","title":"Math"}`, "version": map[string]any{"counter": float64(2), "deviceId": "test", "changedAt": float64(1000)}}},
		},
		"changes": []any{
			map[string]any{"id": "change-settings-1", "domain": "mobile.settings", "recordId": "showWeekend", "action": "updated", "version": map[string]any{"counter": float64(1), "deviceId": "test", "changedAt": float64(1000)}, "occurredAt": float64(1000)},
			map[string]any{"id": "change-lesson-1", "domain": "timetable.lessons", "recordId": "lesson-1", "action": "created", "version": map[string]any{"counter": float64(2), "deviceId": "test", "changedAt": float64(1000)}, "occurredAt": float64(1000)},
		},
		"devices": []any{},
	}
	status, body = client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", access, settingsDoc, map[string]string{"If-Match": `"0"`})
	if status != http.StatusOK {
		t.Fatalf("non-member official settings put: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodGet, "/api/v1/cloud/official/document", access, nil)
	if status != http.StatusOK {
		t.Fatalf("non-member official settings get: %d %+v", status, body)
	}
	records := body["records"].(map[string]any)
	if _, ok := records["mobile.settings"]; !ok {
		t.Fatalf("non-member cloud document missing settings: %+v", body)
	}
	if _, ok := records["timetable.lessons"]; ok {
		t.Fatalf("non-member cloud document exposed timetable: %+v", body)
	}
	changes, ok := body["changes"].([]any)
	if !ok || len(changes) != 1 {
		t.Fatalf("non-member cloud document changes were not filtered: %+v", body)
	}
	for attempt := 0; attempt < 3; attempt++ {
		status, body = client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", access, body, map[string]string{"If-Match": `"` + strconv.Itoa(attempt+1) + `"`})
		if status != http.StatusOK {
			t.Fatalf("repeated non-member settings put %d: %d %+v", attempt+1, status, body)
		}
		status, body = client.request(t, http.MethodGet, "/api/v1/cloud/official/document", access, nil)
		if status != http.StatusOK {
			t.Fatalf("cloud document after repeated put %d: %d %+v", attempt+1, status, body)
		}
		changes, ok = body["changes"].([]any)
		if !ok || len(changes) != 1 {
			t.Fatalf("repeated put %d grew cloud changes: %+v", attempt+1, body)
		}
	}
	alice, err := data.UserByIdentifier(ctx, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	storedDocument, err := data.CloudDocument(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedPayload := map[string]any{}
	if err := json.Unmarshal([]byte(storedDocument.Payload), &storedPayload); err != nil {
		t.Fatal(err)
	}
	storedChanges, ok := storedPayload["changes"].([]any)
	if !ok || len(storedChanges) != 1 {
		t.Fatalf("repeated puts grew stored cloud changes: %+v", storedPayload)
	}
	status, body = client.request(t, http.MethodPost, "/api/v1/briefings/daily/test", access, map[string]any{"channel": "APP_NOTIFICATION"})
	if status != http.StatusAccepted || body["appNotificationQueued"] != true {
		t.Fatalf("app briefing test: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodGet, "/api/v1/cloud/official/document", access, nil)
	if status != http.StatusOK {
		t.Fatalf("cloud document after app test: %d %+v", status, body)
	}
	records = body["records"].(map[string]any)
	if commands, ok := records["app.commands"].([]any); !ok || len(commands) == 0 {
		t.Fatalf("app briefing command missing: %+v", body)
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

	replayAfterRevocationToken := refresh
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refreshToken": replayAfterRevocationToken})
	if status != http.StatusOK {
		t.Fatalf("refresh before password change: %d %+v", status, body)
	}
	prePasswordChangeSession := body["session"].(map[string]any)
	access = prePasswordChangeSession["accessToken"].(string)
	refresh = prePasswordChangeSession["refreshToken"].(string)
	status, body = client.request(t, http.MethodPut, "/api/v1/account/password", access, map[string]any{"currentPassword": "UserPass123!", "newPassword": "UserPass456!"})
	if status != http.StatusOK {
		t.Fatalf("password change: %d %+v", status, body)
	}
	status, _ = client.request(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refreshToken": replayAfterRevocationToken})
	if status != http.StatusUnauthorized {
		t.Fatalf("cached refresh token after password change status = %d", status)
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

func TestAccountEmailChangeSecurity(t *testing.T) {
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
	testServer := httptest.NewServer(New(cfg, data, web, slog.Default()).Handler())
	defer testServer.Close()
	client := testClient{base: testServer.URL, client: testServer.Client()}

	registerUser := func(username, email, password string) string {
		t.Helper()
		status, body := client.request(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": username, "email": email, "password": password})
		if status != http.StatusAccepted {
			t.Fatalf("register %s: %d %+v", username, status, body)
		}
		challenge := body["challenge"].(map[string]any)
		status, body = client.request(t, http.MethodPost, "/api/v1/auth/register/email/confirm", "", map[string]any{"challengeId": challenge["challengeId"], "verificationCode": body["devVerificationCode"]})
		if status != http.StatusCreated {
			t.Fatalf("confirm %s: %d %+v", username, status, body)
		}
		return body["session"].(map[string]any)["accessToken"].(string)
	}

	// --- Stolen-session: email change without currentPassword ---
	bobAccess := registerUser("bob", "bob@old.test", "UserPass123!")
	status, body := client.request(t, http.MethodPatch, "/api/v1/account/me", bobAccess, map[string]any{"username": "bob", "email": "bob@new.test"})
	if status != http.StatusForbidden || body["code"] != "ACCOUNT_PASSWORD_CURRENT_INVALID" {
		t.Fatalf("email change without password: %d %+v", status, body)
	}
	// --- Stolen-session: wrong currentPassword ---
	status, body = client.request(t, http.MethodPatch, "/api/v1/account/me", bobAccess, map[string]any{"username": "bob", "email": "bob@new.test", "currentPassword": "WrongPass123!"})
	if status != http.StatusForbidden || body["code"] != "ACCOUNT_PASSWORD_CURRENT_INVALID" {
		t.Fatalf("email change with wrong password: %d %+v", status, body)
	}
	// --- Verify email not changed ---
	status, body = client.request(t, http.MethodGet, "/api/v1/account/me", bobAccess, nil)
	if status != http.StatusOK || body["account"].(map[string]any)["email"] != "bob@old.test" {
		t.Fatalf("email should still be old: %d %+v", status, body)
	}

	// --- Duplicate email ---
	registerUser("eve", "eve@test.com", "EvePass123!")
	status, body = client.request(t, http.MethodPatch, "/api/v1/account/me", bobAccess, map[string]any{"username": "bob", "email": "eve@test.com", "currentPassword": "UserPass123!"})
	if status != http.StatusConflict || body["code"] != "ACCOUNT_EMAIL_CONFLICT" {
		t.Fatalf("duplicate email: %d %+v", status, body)
	}

	// --- Full email change flow ---
	status, body = client.request(t, http.MethodPatch, "/api/v1/account/me", bobAccess, map[string]any{"username": "bob", "email": "bob@new.test", "currentPassword": "UserPass123!"})
	if status != http.StatusAccepted {
		t.Fatalf("email change request: %d %+v", status, body)
	}
	emailChange := body["emailChange"].(map[string]any)
	requestID := emailChange["requestId"].(string)
	code := body["devVerificationCode"].(string)
	// --- Pending email visible in account ---
	status, body = client.request(t, http.MethodGet, "/api/v1/account/me", bobAccess, nil)
	if status != http.StatusOK {
		t.Fatalf("account me after change request: %d %+v", status, body)
	}
	pending, ok := body["pendingEmailChange"].(map[string]any)
	if !ok || pending["newEmail"] != "bob@new.test" {
		t.Fatalf("pending email change not shown: %+v", body)
	}
	// --- Queued jobs ---
	jobs, _, err := data.ListBriefingJobs(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	hasVerifyJob := false
	hasNotifyJob := false
	for _, job := range jobs {
		if job.Channel == "EMAIL_CHANGE_VERIFY" {
			hasVerifyJob = true
			if !strings.Contains(job.Payload, "bob@new.test") {
				t.Fatalf("verify job payload missing new email: %s", job.Payload)
			}
		}
		if job.Channel == "EMAIL_CHANGE_NOTIFY" {
			hasNotifyJob = true
			if !strings.Contains(job.Payload, "bob@old.test") {
				t.Fatalf("notify job payload missing old email: %s", job.Payload)
			}
		}
	}
	if !hasVerifyJob || !hasNotifyJob {
		t.Fatalf("email change jobs not queued: verify=%v notify=%v", hasVerifyJob, hasNotifyJob)
	}
	// --- Login with old email still works (before confirm) ---
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "bob@old.test", "password": "UserPass123!"})
	if status != http.StatusOK {
		t.Fatalf("login with old email before confirm: %d %+v", status, body)
	}
	// --- Password reset with new email does nothing (not in DB) ---
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", "", map[string]any{"email": "bob@new.test"})
	if status != http.StatusAccepted {
		t.Fatalf("reset request with new email: %d %+v", status, body)
	}
	if _, ok := body["devResetToken"]; ok {
		t.Fatal("reset token should not be exposed for unconfirmed email")
	}
	// --- Password reset with old email works ---
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/password/reset/request", "", map[string]any{"email": "bob@old.test"})
	if status != http.StatusAccepted {
		t.Fatalf("reset request with old email: %d %+v", status, body)
	}

	// --- Confirm email change ---
	status, body = client.request(t, http.MethodPost, "/api/v1/account/email/confirm", bobAccess, map[string]any{"requestId": requestID, "verificationCode": code})
	if status != http.StatusOK {
		t.Fatalf("confirm email change: %d %+v", status, body)
	}
	if body["account"].(map[string]any)["email"] != "bob@new.test" {
		t.Fatalf("email not updated: %+v", body)
	}
	if body["sessionsRevoked"] != true {
		t.Fatal("sessionsRevoked should be true")
	}
	// --- Old access token revoked (auth_epoch bumped) ---
	status, _ = client.request(t, http.MethodGet, "/api/v1/account/me", bobAccess, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("old access token after email change: status = %d", status)
	}
	// --- Login with new email ---
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "bob@new.test", "password": "UserPass123!"})
	if status != http.StatusOK {
		t.Fatalf("login with new email: %d %+v", status, body)
	}
	bobAccess = body["session"].(map[string]any)["accessToken"].(string)

	// --- Code reuse: confirm again with same request ---
	status, body = client.request(t, http.MethodPost, "/api/v1/account/email/confirm", bobAccess, map[string]any{"requestId": requestID, "verificationCode": code})
	if status != http.StatusBadRequest || body["code"] != "ACCOUNT_EMAIL_VERIFICATION_INVALID" {
		t.Fatalf("code reuse: %d %+v", status, body)
	}

	// --- Verification code expiry ---
	carolAccess := registerUser("carol", "carol@old.test", "CarolPass123!")
	status, body = client.request(t, http.MethodPatch, "/api/v1/account/me", carolAccess, map[string]any{"username": "carol", "email": "carol@new.test", "currentPassword": "CarolPass123!"})
	if status != http.StatusAccepted {
		t.Fatalf("carol email change: %d %+v", status, body)
	}
	expiredRequestID := body["emailChange"].(map[string]any)["requestId"].(string)
	expiredCode := body["devVerificationCode"].(string)
	if _, err := data.DB().Exec(data.Rebind(`UPDATE email_change_requests SET expires_at = ? WHERE id = ?`), 1, expiredRequestID); err != nil {
		t.Fatal(err)
	}
	status, body = client.request(t, http.MethodPost, "/api/v1/account/email/confirm", carolAccess, map[string]any{"requestId": expiredRequestID, "verificationCode": expiredCode})
	if status != http.StatusBadRequest || body["code"] != "ACCOUNT_EMAIL_VERIFICATION_INVALID" {
		t.Fatalf("expired code: %d %+v", status, body)
	}
	// --- Verify email not changed ---
	status, body = client.request(t, http.MethodGet, "/api/v1/account/me", carolAccess, nil)
	if status != http.StatusOK || body["account"].(map[string]any)["email"] != "carol@old.test" {
		t.Fatalf("carol email should still be old after expired confirm: %+v", body)
	}
}

func parseCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("parse CIDR %q: %v", c, err)
		}
		out = append(out, n)
	}
	return out
}

func newTestServerWithTrustedProxies(t *testing.T, trustedProxies []*net.IPNet) (*httptest.Server, *store.Store, testClient) {
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
		MaxCloudDocumentSize: 1024 * 1024, TrustedProxies: trustedProxies,
	}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Classing</title>")}}
	testServer := httptest.NewServer(New(cfg, data, web, slog.Default()).Handler())
	t.Cleanup(func() { testServer.Close() })
	return testServer, data, testClient{base: testServer.URL, client: testServer.Client()}
}

func TestClientIPTrustedProxy(t *testing.T) {
	cases := []struct {
		name       string
		trusted    []*net.IPNet
		remoteAddr string
		xff        string
		xRealIP    string
		want       string
	}{
		{"untrusted remote ignores xff", nil, "203.0.113.9:1234", "9.9.9.9", "", "203.0.113.9"},
		{"trusted remote strips right to left", parseCIDRs(t, "127.0.0.0/8"), "127.0.0.1:1234", "9.9.9.9, 1.2.3.4", "", "1.2.3.4"},
		{"trusted remote single xff value", parseCIDRs(t, "127.0.0.0/8"), "127.0.0.1:1234", "9.9.9.9", "", "9.9.9.9"},
		{"all xff entries trusted falls back to remote", parseCIDRs(t, "127.0.0.0/8", "10.0.0.0/8"), "127.0.0.1:1234", "10.0.0.7", "", "127.0.0.1"},
		{"empty xff uses x real ip", parseCIDRs(t, "127.0.0.0/8"), "127.0.0.1:1234", "", "203.0.113.5", "203.0.113.5"},
		{"malformed xff entry skipped", parseCIDRs(t, "127.0.0.0/8"), "127.0.0.1:1234", "not-an-ip, 198.51.100.7", "", "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{cfg: config.Config{TrustedProxies: tc.trusted}}
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xRealIP != "" {
				r.Header.Set("X-Real-IP", tc.xRealIP)
			}
			if got := srv.clientIP(r); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMultiLevelProxyChain(t *testing.T) {
	trusted := parseCIDRs(t, "127.0.0.0/8", "10.0.0.0/8")
	srv := &Server{cfg: config.Config{TrustedProxies: trusted}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.7")
	got := srv.clientIP(r)
	if got != "203.0.113.5" {
		t.Fatalf("clientIP = %q, want 203.0.113.5 (rightmost non-trusted after stripping 10.0.0.7 and 127.0.0.1)", got)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "127.0.0.1:5000"
	r2.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.7, 10.0.0.8")
	if got := srv.clientIP(r2); got != "203.0.113.5" {
		t.Fatalf("clientIP = %q, want 203.0.113.5 (multiple trusted hops stripped)", got)
	}
}

func TestForgedXFFRateLimitBypass(t *testing.T) {
	t.Run("untrusted remote addr ignores xff", func(t *testing.T) {
		_, _, client := newTestServerWithTrustedProxies(t, nil)
		for i := 0; i < 25; i++ {
			headers := map[string]string{"X-Forwarded-For": fmt.Sprintf("10.%d.%d.%d", i/256, i%256, i%50)}
			status, _ := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/password/reset/request", "", map[string]any{"email": "anyone@test.local"}, headers)
			if i < 20 {
				if status == http.StatusTooManyRequests {
					t.Fatalf("request %d unexpectedly rate limited (untrusted remote should still allow until IP cap)", i)
				}
			} else if status != http.StatusTooManyRequests {
				t.Fatalf("request %d: expected 429 (IP rate limit), got %d", i, status)
			}
		}
	})
	t.Run("trusted remote strips forged leftmost", func(t *testing.T) {
		_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
		for i := 0; i < 25; i++ {
			headers := map[string]string{"X-Forwarded-For": fmt.Sprintf("%d.2.3.4, 203.0.113.55", i)}
			status, _ := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/password/reset/request", "", map[string]any{"email": "anyone@test.local"}, headers)
			if i < 20 {
				if status == http.StatusTooManyRequests {
					t.Fatalf("request %d unexpectedly rate limited before IP cap", i)
				}
			} else if status != http.StatusTooManyRequests {
				t.Fatalf("request %d: expected 429 (forged leftmost must not change key), got %d", i, status)
			}
		}
	})
}

func TestLoginIdentifierRateLimit(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	registerUser := func(username, email, password string) string {
		t.Helper()
		status, body := client.request(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": username, "email": email, "password": password})
		if status != http.StatusAccepted {
			t.Fatalf("register %s: %d %+v", username, status, body)
		}
		challenge := body["challenge"].(map[string]any)
		status, body = client.request(t, http.MethodPost, "/api/v1/auth/register/email/confirm", "", map[string]any{"challengeId": challenge["challengeId"], "verificationCode": body["devVerificationCode"]})
		if status != http.StatusCreated {
			t.Fatalf("confirm %s: %d %+v", username, status, body)
		}
		return body["session"].(map[string]any)["accessToken"].(string)
	}
	registerUser("alice", "alice@test.local", "AlicePass123!")

	xff := func(i int) map[string]string {
		return map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.%d.%d", i/256, i%256)}
	}
	wrong := map[string]any{"identifier": "alice@test.local", "password": "WrongPass99!"}
	right := map[string]any{"identifier": "alice@test.local", "password": "AlicePass123!"}

	for i := 0; i < 4; i++ {
		if status, _ := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/login", "", wrong, xff(i)); status != http.StatusUnauthorized {
			t.Fatalf("failure %d: expected 401, got %d", i, status)
		}
	}
	if status, _ := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/login", "", right, xff(4)); status != http.StatusOK {
		t.Fatalf("success after 4 failures should reset: expected 200, got %d", status)
	}
	for i := 5; i < 10; i++ {
		if status, _ := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/login", "", wrong, xff(i)); status != http.StatusUnauthorized {
			t.Fatalf("failure %d: expected 401, got %d", i, status)
		}
	}
	status, body := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/login", "", wrong, xff(99))
	if status != http.StatusTooManyRequests || body["code"] != "AUTH_LOGIN_LOCKED" {
		t.Fatalf("6th failure after reset: expected 429 AUTH_LOGIN_LOCKED, got %d %+v", status, body)
	}
}

func TestVerificationCodeBruteForceCap(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	status, body := client.request(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": "brute", "email": "brute@test.local", "password": "BrutePass123!"})
	if status != http.StatusAccepted {
		t.Fatalf("register request: %d %+v", status, body)
	}
	challengeID := body["challenge"].(map[string]any)["challengeId"].(string)
	correctCode := body["devVerificationCode"].(string)

	xff := func(i int) map[string]string {
		return map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.%d.%d", i/256, i%256)}
	}
	for i := 0; i < 10; i++ {
		status, resp := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/register/email/confirm", "", map[string]any{"challengeId": challengeID, "verificationCode": "000000"}, xff(i))
		if status != http.StatusBadRequest || resp["code"] != "AUTH_EMAIL_VERIFICATION_INVALID" {
			t.Fatalf("wrong attempt %d: expected 400 AUTH_EMAIL_VERIFICATION_INVALID, got %d %+v", i, status, resp)
		}
	}
	status, resp := client.requestWithHeaders(t, http.MethodPost, "/api/v1/auth/register/email/confirm", "", map[string]any{"challengeId": challengeID, "verificationCode": correctCode}, xff(99))
	if status != http.StatusBadRequest || resp["code"] != "AUTH_EMAIL_VERIFICATION_INVALID" {
		t.Fatalf("correct code after lockout: expected 400 (locked), got %d %+v", status, resp)
	}
}

func TestRateLimiterBounded(t *testing.T) {
	limiter := newRateLimiterWithCap(100, time.Minute, 4)
	for i := 0; i < 4; i++ {
		if !limiter.allow(fmt.Sprintf("ip-%d", i)) {
			t.Fatalf("allow ip-%d should succeed under limit", i)
		}
	}
	if size := limiter.size(); size != 4 {
		t.Fatalf("size after 4 keys = %d, want 4", size)
	}
	if !limiter.allow("ip-4") {
		t.Fatal("allow ip-4 should succeed (eviction frees capacity)")
	}
	if size := limiter.size(); size > 4 {
		t.Fatalf("size after 5 keys = %d, want <= 4 (eviction must bound map)", size)
	}
	if !limiter.allow("ip-5") {
		t.Fatal("allow ip-5 should succeed")
	}
	if size := limiter.size(); size > 4 {
		t.Fatalf("size after 6 keys = %d, want <= 4", size)
	}

	short := newRateLimiterWithCap(100, 40*time.Millisecond, 4)
	for i := 0; i < 4; i++ {
		short.allow(fmt.Sprintf("short-%d", i))
	}
	time.Sleep(60 * time.Millisecond)
	short.allow("short-new")
	if size := short.size(); size != 1 {
		t.Fatalf("size after sweep = %d, want 1 (expired entries removed, only new key remains)", size)
	}

	failLimiter := newRateLimiterWithCap(5, time.Minute, 4)
	for i := 0; i < 5; i++ {
		failLimiter.recordFailure("alice")
	}
	if !failLimiter.isLimited("alice") {
		t.Fatal("isLimited should be true after 5 failures")
	}
	if failLimiter.isLimited("bob") {
		t.Fatal("isLimited should be false for unknown key")
	}
	failLimiter.reset("alice")
	if failLimiter.isLimited("alice") {
		t.Fatal("isLimited should be false after reset")
	}
}

// registerTestUser registers a user via the email verification flow and returns
// the access token. Each test creates a fresh server, so the 20/min auth
// rate limit easily covers the 2 calls per registration from 127.0.0.1.
func registerTestUser(t *testing.T, client testClient, username, email, password string) string {
	t.Helper()
	status, body := client.request(t, http.MethodPost, "/api/v1/auth/register/email/request", "", map[string]any{"username": username, "email": email, "password": password})
	if status != http.StatusAccepted {
		t.Fatalf("register %s: %d %+v", username, status, body)
	}
	challenge := body["challenge"].(map[string]any)
	status, body = client.request(t, http.MethodPost, "/api/v1/auth/register/email/confirm", "", map[string]any{"challengeId": challenge["challengeId"], "verificationCode": body["devVerificationCode"]})
	if status != http.StatusCreated {
		t.Fatalf("confirm %s: %d %+v", username, status, body)
	}
	return body["session"].(map[string]any)["accessToken"].(string)
}

func TestLogoutRevokesOnlyCurrentAccessSession(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	login := func() (string, string) {
		status, body := client.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"identifier": "admin", "password": "AdminPass123!"})
		if status != http.StatusOK {
			t.Fatalf("login: %d %+v", status, body)
		}
		session := body["session"].(map[string]any)
		return session["accessToken"].(string), session["refreshToken"].(string)
	}
	firstAccess, _ := login()
	secondAccess, secondRefresh := login()
	status, body := client.request(t, http.MethodPost, "/api/v1/auth/logout", firstAccess, map[string]any{"refreshToken": secondRefresh})
	if status != http.StatusOK {
		t.Fatalf("logout: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodGet, "/api/v1/account/me", firstAccess, nil)
	if status != http.StatusUnauthorized || body["code"] != "AUTH_SESSION_REVOKED" {
		t.Fatalf("revoked access accepted: %d %+v", status, body)
	}
	status, body = client.request(t, http.MethodGet, "/api/v1/account/me", secondAccess, nil)
	if status != http.StatusOK {
		t.Fatalf("unrelated session revoked: %d %+v", status, body)
	}
}

func TestSensitiveLimitMiddleware(t *testing.T) {
	ipLim := newRateLimiterWithCap(2, time.Minute, 8)
	acctLim := newRateLimiterWithCap(3, time.Minute, 8)
	srv := &Server{}
	handler := srv.sensitiveLimit(ipLim, acctLim)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// IP dimension: 3rd request from same IP (unauthenticated) trips IP limiter.
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		w := serve(r)
		if w.Code != http.StatusOK {
			t.Fatalf("ip request %d: expected 200, got %d", i, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	w := serve(r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("ip limit: expected 429 + Retry-After, got %d %+v", w.Code, w.Header())
	}

	// Account dimension: 4th request from same account trips account limiter.
	// Uses a fresh IP each time so the IP limiter is not the bottleneck.
	withPrincipal := func(userID, remoteAddr string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = remoteAddr
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{User: model.User{ID: userID}})
		return r.WithContext(ctx)
	}
	for i := 0; i < 3; i++ {
		req := withPrincipal("user-acct", fmt.Sprintf("198.51.100.%d:1234", i))
		rr := serve(req)
		if rr.Code != http.StatusOK {
			t.Fatalf("account request %d: expected 200, got %d", i, rr.Code)
		}
	}
	// 4th request from same account but fresh IP -> account limiter blocks.
	req := withPrincipal("user-acct", "198.51.100.99:1234")
	rr := serve(req)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "60" {
		t.Fatalf("account limit: expected 429 + Retry-After, got %d %+v", rr.Code, rr.Header())
	}
}

func TestRedeemAccountRateLimit(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	access := registerTestUser(t, client, "alice", "alice@redeem.test", "AlicePass123!")

	// Rotating XFF so the IP limiter never trips; only the account dimension is exercised.
	xff := func(i int) map[string]string {
		return map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.%d.%d", i/256, i%256)}
	}
	for i := 0; i < redeemAccountLimit; i++ {
		status, _ := client.requestWithHeaders(t, http.MethodPost, "/api/v1/membership/redeem", access, map[string]any{"code": "INVALID"}, xff(i))
		if status == http.StatusTooManyRequests {
			t.Fatalf("redeem %d unexpectedly rate limited", i)
		}
	}
	status, body := client.requestWithHeaders(t, http.MethodPost, "/api/v1/membership/redeem", access, map[string]any{"code": "INVALID"}, xff(redeemAccountLimit))
	if status != http.StatusTooManyRequests || body["code"] != "ACCOUNT_RATE_LIMITED" {
		t.Fatalf("redeem after limit: expected 429 ACCOUNT_RATE_LIMITED, got %d %+v", status, body)
	}
}

func TestBriefingTestAccountRateLimit(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	access := registerTestUser(t, client, "bob", "bob@brief.test", "BobPass123!")

	xff := func(i int) map[string]string {
		return map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.%d.%d", i/256, i%256)}
	}
	for i := 0; i < briefingTestAccountLimit; i++ {
		status, _ := client.requestWithHeaders(t, http.MethodPost, "/api/v1/briefings/daily/test", access, map[string]any{"channel": "EMAIL"}, xff(i))
		if status == http.StatusTooManyRequests {
			t.Fatalf("briefing test %d unexpectedly rate limited", i)
		}
	}
	status, body := client.requestWithHeaders(t, http.MethodPost, "/api/v1/briefings/daily/test", access, map[string]any{"channel": "EMAIL"}, xff(briefingTestAccountLimit))
	if status != http.StatusTooManyRequests || body["code"] != "ACCOUNT_RATE_LIMITED" {
		t.Fatalf("briefing test after limit: expected 429 ACCOUNT_RATE_LIMITED, got %d %+v", status, body)
	}
}

func TestCloudWriteAccountRateLimit(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	access := registerTestUser(t, client, "carol", "carol@cloud.test", "CarolPass123!")

	xff := func(i int) map[string]string {
		return map[string]string{"X-Forwarded-For": fmt.Sprintf("192.0.%d.%d", i/256, i%256)}
	}
	doc := map[string]any{"format": "classing_cloud_sync_v2", "updatedAt": 0, "records": map[string]any{}, "changes": []any{}, "devices": []any{}}
	for i := 0; i < cloudWriteAccountLimit; i++ {
		status, _ := client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", access, doc, xff(i))
		if status == http.StatusTooManyRequests {
			t.Fatalf("cloud write %d unexpectedly rate limited", i)
		}
	}
	status, body := client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", access, doc, xff(cloudWriteAccountLimit))
	if status != http.StatusTooManyRequests || body["code"] != "ACCOUNT_RATE_LIMITED" {
		t.Fatalf("cloud write after limit: expected 429 ACCOUNT_RATE_LIMITED, got %d %+v", status, body)
	}
}

func TestAccountWriteRateLimit(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	access := registerTestUser(t, client, "dave", "dave@acct.test", "DavePass123!")

	xff := func(i int) map[string]string {
		return map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i)}
	}
	// Wrong current password -> handler returns 403, but each request counts.
	for i := 0; i < accountWriteAccountLimit; i++ {
		status, _ := client.requestWithHeaders(t, http.MethodPut, "/api/v1/account/password", access, map[string]any{"currentPassword": "WrongPass99!", "newPassword": "NewPass99!"}, xff(i))
		if status == http.StatusTooManyRequests {
			t.Fatalf("account write %d unexpectedly rate limited", i)
		}
	}
	status, body := client.requestWithHeaders(t, http.MethodPut, "/api/v1/account/password", access, map[string]any{"currentPassword": "WrongPass99!", "newPassword": "NewPass99!"}, xff(accountWriteAccountLimit))
	if status != http.StatusTooManyRequests || body["code"] != "ACCOUNT_RATE_LIMITED" {
		t.Fatalf("account write after limit: expected 429 ACCOUNT_RATE_LIMITED, got %d %+v", status, body)
	}
}

func TestSensitiveIPRateLimit(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, parseCIDRs(t, "127.0.0.0/8"))
	// Same IP (127.0.0.1, no XFF) across multiple accounts so the account
	// dimension is not the bottleneck. Each account stays under its own cap.
	accesses := make([]string, 0, sensitiveIPLimit/accountWriteAccountLimit+1)
	for i := 0; i < sensitiveIPLimit/accountWriteAccountLimit+1; i++ {
		accesses = append(accesses, registerTestUser(t, client, fmt.Sprintf("ip%d", i), fmt.Sprintf("ip%d@ip.test", i), "IpPass123!"))
	}
	// Fire sensitiveIPLimit requests from the same IP, round-robin across accounts.
	idx := 0
	for i := 0; i < sensitiveIPLimit; i++ {
		status, _ := client.request(t, http.MethodPut, "/api/v1/account/password", accesses[idx], map[string]any{"currentPassword": "WrongPass99!", "newPassword": "NewPass99!"})
		if status == http.StatusTooManyRequests {
			t.Fatalf("ip request %d unexpectedly rate limited", i)
		}
		idx = (idx + 1) % len(accesses)
	}
	// Next request from the same IP should trip the shared IP limiter.
	status, body := client.request(t, http.MethodPut, "/api/v1/account/password", accesses[0], map[string]any{"currentPassword": "WrongPass99!", "newPassword": "NewPass99!"})
	if status != http.StatusTooManyRequests || body["code"] != "IP_RATE_LIMITED" {
		t.Fatalf("ip limit: expected 429 IP_RATE_LIMITED, got %d %+v", status, body)
	}
}

func TestPublicDownloadRateLimit(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, nil)
	for i := 0; i < 3; i++ {
		status, _ := client.request(t, http.MethodGet, "/api/v1/client/releases/any-id/download", "", nil)
		if status == http.StatusTooManyRequests {
			t.Fatalf("download %d unexpectedly rate limited", i)
		}
	}
	status, body := client.request(t, http.MethodGet, "/api/v1/client/releases/any-id/download", "", nil)
	if status != http.StatusTooManyRequests || body["code"] != "CLIENT_RATE_LIMITED" {
		t.Fatalf("download after limit: expected 429 CLIENT_RATE_LIMITED, got %d %+v", status, body)
	}
}
