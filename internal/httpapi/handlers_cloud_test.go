package httpapi

import (
	"reflect"
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
