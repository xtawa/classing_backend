package httpapi

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOfficialCloudEventsVersionIsolationReconnectAndKeepAlive(t *testing.T) {
	previousPoll := cloudEventPollInterval
	previousKeepAlive := cloudEventKeepAliveInterval
	cloudEventPollInterval = 5 * time.Millisecond
	cloudEventKeepAliveInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		cloudEventPollInterval = previousPoll
		cloudEventKeepAliveInterval = previousKeepAlive
	})

	server, _, client := newTestServerWithTrustedProxies(t, nil)
	alice := registerTestUser(t, client, "sse-alice", "sse-alice@test.local", "AlicePass123!")
	bob := registerTestUser(t, client, "sse-bob", "sse-bob@test.local", "BobPass123!")
	document := func(updatedAt int64) map[string]any {
		return map[string]any{
			"format": "classing_cloud_sync_v2", "updatedAt": updatedAt,
			"records": map[string]any{}, "changes": []any{}, "devices": []any{},
		}
	}

	status, body := client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", alice, document(1), map[string]string{
		"If-Match": `"0"`, "Idempotency-Key": "alice-first-write",
	})
	if status != http.StatusOK || body["version"] != float64(1) {
		t.Fatalf("alice first write: %d %+v", status, body)
	}
	status, body = client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", alice, document(1), map[string]string{
		"If-Match": `"0"`, "Idempotency-Key": "alice-first-write",
	})
	if status != http.StatusOK || body["version"] != float64(1) {
		t.Fatalf("idempotent replay changed version: %d %+v", status, body)
	}
	status, body = client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", alice, document(2), map[string]string{
		"If-Match": `"1"`, "Idempotency-Key": "alice-second-write",
	})
	if status != http.StatusOK || body["version"] != float64(2) {
		t.Fatalf("alice second write: %d %+v", status, body)
	}
	status, body = client.requestWithHeaders(t, http.MethodPut, "/api/v1/cloud/official/document", bob, document(1), map[string]string{
		"If-Match": `"0"`, "Idempotency-Key": "bob-first-write",
	})
	if status != http.StatusOK || body["version"] != float64(1) {
		t.Fatalf("bob first write: %d %+v", status, body)
	}

	if status, block := readOfficialCloudSSEBlock(t, server.URL, alice, "0"); status != http.StatusOK ||
		!strings.Contains(block, "id: 2") || !strings.Contains(block, "event: cloud-document") ||
		!strings.Contains(block, `"version":2`) || strings.Contains(block, "records") {
		t.Fatalf("alice latest-only event: status=%d block=%q", status, block)
	}
	if status, block := readOfficialCloudSSEBlock(t, server.URL, alice, "1"); status != http.StatusOK || !strings.Contains(block, `"version":2`) {
		t.Fatalf("alice reconnect event: status=%d block=%q", status, block)
	}
	if status, block := readOfficialCloudSSEBlock(t, server.URL, bob, ""); status != http.StatusOK || !strings.Contains(block, `"version":1`) {
		t.Fatalf("bob isolated event: status=%d block=%q", status, block)
	}
	if status, block := readOfficialCloudSSEBlock(t, server.URL, alice, "2"); status != http.StatusOK || block != ": keep-alive" {
		t.Fatalf("keep-alive event: status=%d block=%q", status, block)
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/cloud/official/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("unauthorized SSE status=%d body=%s", response.StatusCode, payload)
	}
}

func readOfficialCloudSSEBlock(t *testing.T, baseURL, token, cursor string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/cloud/official/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/event-stream")
	if cursor != "" {
		request.Header.Set("Last-Event-ID", cursor)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, ""
	}
	scanner := bufio.NewScanner(response.Body)
	lines := make([]string, 0, 4)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, strings.Join(lines, "\n")
}
