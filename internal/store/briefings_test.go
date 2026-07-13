package store

import (
	"context"
	"strings"
	"testing"

	"github.com/xtawa/classing-backend/internal/model"
)

func TestBriefingJobLogsPersistAndListByJob(t *testing.T) {
	data := newSQLiteTestStore(t)
	ctx := context.Background()
	user, err := data.CreateUser(ctx, "loguser", "loguser@example.test", "hash", model.RoleUser)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	job, err := data.QueueBriefingJob(ctx, user.ID, "2026-07-13", "EMAIL_TEST", 0)
	if err != nil {
		t.Fatalf("queue job: %v", err)
	}
	if err := data.AddBriefingJobLog(ctx, job.ID, "info", "smtp.connect_start", "Connecting", map[string]any{"host": "smtp.larksuite.com", "port": 465}); err != nil {
		t.Fatalf("add first log: %v", err)
	}
	if err := data.AddBriefingJobLog(ctx, job.ID, "error", "smtp.auth_failed", "Auth failed", map[string]any{"error": "535 auth failed"}); err != nil {
		t.Fatalf("add second log: %v", err)
	}
	logs, err := data.ListBriefingJobLogs(ctx, []string{job.ID}, 20)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	items := logs[job.ID]
	if len(items) != 2 {
		t.Fatalf("log count = %d, want 2", len(items))
	}
	if items[0].Event != "smtp.connect_start" || items[1].Level != "ERROR" {
		t.Fatalf("logs not returned in chronological order: %+v", items)
	}
	if !strings.Contains(items[0].Details, "smtp.larksuite.com") || !strings.Contains(items[1].Details, "535 auth failed") {
		t.Fatalf("details not preserved: %+v", items)
	}
}
