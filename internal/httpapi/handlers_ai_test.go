package httpapi

import (
	"encoding/json"
	"testing"
)

func TestNormalizeAITimetable(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "standard", raw: `{"lessons":[{"title":"Math"}]}`, want: 1},
		{name: "legacy", raw: `{"timetable":{"baseLessons":[{"title":"Math"}],"exceptions":[]}}`, want: 1},
		{name: "cloud v2", raw: `{"records":{"timetable.lessons":[{"payload":"{\"title\":\"Math\"}","deletedAt":null},{"payload":"{\"title\":\"Deleted\"}","deletedAt":1}]}}`, want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizeAITimetable([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			var root struct {
				Lessons []json.RawMessage `json:"lessons"`
			}
			if err := json.Unmarshal(normalized, &root); err != nil {
				t.Fatal(err)
			}
			if len(root.Lessons) != test.want {
				t.Fatalf("lessons=%d want=%d", len(root.Lessons), test.want)
			}
		})
	}
}

func TestNormalizeAITimetableRejectsEmpty(t *testing.T) {
	if _, err := normalizeAITimetable([]byte(`{"lessons":[]}`)); err == nil {
		t.Fatal("expected empty timetable to be rejected")
	}
}
