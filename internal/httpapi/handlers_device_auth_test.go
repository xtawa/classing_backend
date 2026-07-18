package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestWearQRCodeDeviceAuthorizationFlow(t *testing.T) {
	_, _, client := newTestServerWithTrustedProxies(t, nil)

	status, started := client.request(t, http.MethodPost, "/api/v1/auth/device/qr/start", "", map[string]any{
		"deviceName": "Pixel Watch",
	})
	if status != http.StatusCreated {
		t.Fatalf("start: expected 201, got %d %+v", status, started)
	}
	authorizationID, _ := started["authorizationId"].(string)
	pollSecret, _ := started["pollSecret"].(string)
	qrPayload, _ := started["qrPayload"].(string)
	qrImage, _ := started["qrImage"].(string)
	if authorizationID == "" || pollSecret == "" || !strings.Contains(qrPayload, authorizationID) || !strings.HasPrefix(qrImage, "data:image/png;base64,") {
		t.Fatalf("start response missing secure device credentials: %+v", started)
	}
	if strings.Contains(qrPayload, pollSecret) {
		t.Fatal("poll secret must never be encoded in the QR payload")
	}

	status, pending := client.request(t, http.MethodPost, "/api/v1/auth/device/qr/poll", "", map[string]any{
		"authorizationId": authorizationID,
		"pollSecret":      pollSecret,
	})
	if status != http.StatusAccepted || pending["status"] != "PENDING" {
		t.Fatalf("pending poll: expected 202 PENDING, got %d %+v", status, pending)
	}

	status, invalid := client.request(t, http.MethodPost, "/api/v1/auth/device/qr/poll", "", map[string]any{
		"authorizationId": authorizationID,
		"pollSecret":      "not-the-secret",
	})
	if status != http.StatusUnauthorized || invalid["code"] != "DEVICE_AUTH_INVALID" {
		t.Fatalf("invalid secret: expected 401 DEVICE_AUTH_INVALID, got %d %+v", status, invalid)
	}

	status, unauthorized := client.request(t, http.MethodPost, "/api/v1/auth/device/qr/approve", "", map[string]any{
		"authorizationId": authorizationID,
	})
	if status != http.StatusUnauthorized || unauthorized["code"] != "AUTH_REQUIRED" {
		t.Fatalf("unauthorized approve: expected 401 AUTH_REQUIRED, got %d %+v", status, unauthorized)
	}

	access := registerTestUser(t, client, "wearowner", "wearowner@test.local", "WearOwner123!")
	status, approved := client.request(t, http.MethodPost, "/api/v1/auth/device/qr/approve", access, map[string]any{
		"authorizationId": authorizationID,
	})
	if status != http.StatusOK || approved["status"] != "APPROVED" {
		t.Fatalf("approve: expected 200 APPROVED, got %d %+v", status, approved)
	}

	status, exchanged := client.request(t, http.MethodPost, "/api/v1/auth/device/qr/poll", "", map[string]any{
		"authorizationId": authorizationID,
		"pollSecret":      pollSecret,
	})
	if status != http.StatusOK || exchanged["status"] != "APPROVED" {
		t.Fatalf("exchange: expected 200 APPROVED, got %d %+v", status, exchanged)
	}
	session, ok := exchanged["session"].(map[string]any)
	if !ok || session["accessToken"] == "" || session["refreshToken"] == "" {
		t.Fatalf("exchange response missing session: %+v", exchanged)
	}
	wearAccess, _ := session["accessToken"].(string)
	status, account := client.request(t, http.MethodGet, "/api/v1/account/me", wearAccess, nil)
	if status != http.StatusOK {
		t.Fatalf("Wear access token cannot access account: %d %+v", status, account)
	}

	status, replay := client.request(t, http.MethodPost, "/api/v1/auth/device/qr/poll", "", map[string]any{
		"authorizationId": authorizationID,
		"pollSecret":      pollSecret,
	})
	if status != http.StatusGone || replay["code"] != "DEVICE_AUTH_CONSUMED" {
		t.Fatalf("replay: expected 410 DEVICE_AUTH_CONSUMED, got %d %+v", status, replay)
	}
}
