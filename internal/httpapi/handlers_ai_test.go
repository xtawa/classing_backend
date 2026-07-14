package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xtawa/classing-backend/internal/model"
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

func TestBuildAIProviderMessagesIncludesDateAndConfiguredWeek(t *testing.T) {
	config := model.AIConfig{QuotaTimezone: "Asia/Shanghai", SystemPrompt: "Stay concise.", TimetablePrompt: "Use lessons."}
	timetable := `{"currentDate":"2026-07-14","currentWeek":7,"weekNumberMode":"SEMESTER","lessons":[{"title":"Math"}]}`
	messages := buildAIProviderMessages(config, timetable, nil, "What is next?", time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC))
	if len(messages) != 3 {
		t.Fatalf("messages=%d want=3", len(messages))
	}
	for _, expected := range []string{"current date=2026-07-14", "day of week=Tuesday", "configured current week=7", "week number mode=SEMESTER"} {
		if !strings.Contains(messages[0].Content, expected) {
			t.Fatalf("system prompt missing %q: %s", expected, messages[0].Content)
		}
	}
	if !strings.HasSuffix(messages[0].Content, "Apply lesson start/end week and parity constraints using the configured current week.") {
		t.Fatalf("dynamic schedule context must be the final system instruction: %s", messages[0].Content)
	}
	if !strings.Contains(messages[1].Content, `"currentDate":"2026-07-14"`) || !strings.Contains(messages[1].Content, `"currentWeek":7`) {
		t.Fatalf("timetable prompt missing date or week: %s", messages[1].Content)
	}
}

func TestNormalizeAITimetableRejectsEmpty(t *testing.T) {
	if _, err := normalizeAITimetable([]byte(`{"lessons":[]}`)); err == nil {
		t.Fatal("expected empty timetable to be rejected")
	}
}
