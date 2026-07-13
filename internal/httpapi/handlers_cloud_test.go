package httpapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMergeCloudChangesIsStableAndKeepsNewestVersion(t *testing.T) {
	older := map[string]any{
		"id":         "change-1",
		"domain":     "mobile.settings",
		"recordId":   "showWeekend",
		"occurredAt": float64(100),
		"version":    map[string]any{"counter": float64(1), "changedAt": float64(100)},
	}
	newer := map[string]any{
		"id":         "change-1",
		"domain":     "mobile.settings",
		"recordId":   "showWeekend",
		"occurredAt": float64(200),
		"version":    map[string]any{"counter": float64(2), "changedAt": float64(200)},
	}
	anonymous := map[string]any{
		"domain":     "wear.settings",
		"recordId":   "theme",
		"action":     "updated",
		"occurredAt": float64(300),
	}
	merged := mergeCloudChanges(
		[]any{older, older, anonymous},
		[]any{newer, anonymous},
	)
	if len(merged) != 2 {
		t.Fatalf("merged changes length = %d, want 2: %+v", len(merged), merged)
	}
	if !reflect.DeepEqual(merged[0], newer) {
		t.Fatalf("newest change was not retained: %+v", merged[0])
	}
	repeated := mergeCloudChanges(merged, []any{newer, anonymous})
	if !reflect.DeepEqual(repeated, merged) {
		t.Fatalf("repeated merge was not stable: first=%+v repeated=%+v", merged, repeated)
	}
}

func TestValidateCloudDocumentContract(t *testing.T) {
	valid := []byte(emptyCloudDocumentV2())
	if err := validateCloudDocument(valid); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	cases := []string{
		`[]`,
		`{"format":"legacy","updatedAt":0,"records":{},"changes":[],"devices":[]}`,
		`{"format":"classing_cloud_sync_v2","updatedAt":-1,"records":{},"changes":[],"devices":[]}`,
		`{"format":"classing_cloud_sync_v2","updatedAt":0,"records":[],"changes":[],"devices":[]}`,
		`{"format":"classing_cloud_sync_v2","updatedAt":0,"records":{},"changes":[],"devices":[],"secret":true}`,
	}
	for _, payload := range cases {
		if err := validateCloudDocument([]byte(payload)); err == nil {
			t.Errorf("invalid document accepted: %s", payload)
		}
	}

	deep := any("leaf")
	for i := 0; i < 33; i++ {
		deep = map[string]any{"next": deep}
	}
	payload, err := json.Marshal(map[string]any{"format": "classing_cloud_sync_v2", "updatedAt": 0, "records": map[string]any{"mobile.settings": deep}, "changes": []any{}, "devices": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCloudDocument(payload); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("deep document error = %v, want nesting error", err)
	}
}

func TestCloudETagParsers(t *testing.T) {
	if version, err := parseIfMatch(`"12"`); err != nil || version != 12 {
		t.Fatalf("parseIfMatch = %d, %v", version, err)
	}
	for _, invalid := range []string{"", "12", `W/"12"`, `"-1"`, `"x"`} {
		if _, err := parseIfMatch(invalid); err == nil {
			t.Errorf("invalid If-Match accepted: %q", invalid)
		}
	}
	if !etagMatches(`W/"3", "4"`, 3) || !etagMatches(`W/"3", "4"`, 4) || etagMatches(`"5"`, 4) {
		t.Fatal("ETag matching did not honor strong/weak candidate lists")
	}
}
