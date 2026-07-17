package store

import (
	"context"
	"testing"
	"time"

	"github.com/xtawa/classing-backend/internal/model"
)

func TestCleanupAuditLogsRespectsRetention(t *testing.T) {
	ctx := context.Background()
	for _, dialect := range testDialects(t) {
		t.Run(dialect.name, func(t *testing.T) {
			data := dialect.open(t)
			old := time.Now().Add(-48 * time.Hour).UnixMilli()
			recent := time.Now().Add(-12 * time.Hour).UnixMilli()
			if err := data.Audit(ctx, model.AuditLog{ID: "aud-old", Action: "OLD", TargetType: "TEST", CreatedAt: old}); err != nil {
				t.Fatal(err)
			}
			if err := data.Audit(ctx, model.AuditLog{ID: "aud-recent", Action: "RECENT", TargetType: "TEST", CreatedAt: recent}); err != nil {
				t.Fatal(err)
			}
			deleted, err := data.CleanupAuditLogs(ctx, 1)
			if err != nil || deleted != 1 {
				t.Fatalf("cleanup: deleted=%d err=%v", deleted, err)
			}
			items, total, err := data.ListAudit(ctx, 10, 0)
			if err != nil || total != 1 || len(items) != 1 || items[0].ID != "aud-recent" {
				t.Fatalf("remaining audit logs: total=%d items=%+v err=%v", total, items, err)
			}
		})
	}
}
