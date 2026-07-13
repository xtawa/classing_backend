package store

import (
	"context"
	"testing"
)

func TestMigrateEmptyDB(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.open(t)
			ctx := context.Background()

			tables := []string{
				"users", "auth_sessions", "refresh_tokens", "password_reset_tokens",
				"memberships", "membership_events", "redeem_codes", "redeem_redemptions",
				"timetable_projects", "cloud_documents", "idempotency_keys",
				"briefing_subscriptions", "mailboxes", "briefing_jobs",
				"announcements", "app_releases", "audit_logs", "system_settings",
				"runtime_events",
				"email_verification_challenges", "email_change_requests",
			}
			for _, table := range tables {
				var count int
				if err := store.db.GetContext(ctx, &count, store.rebind(`SELECT COUNT(*) FROM `+table)); err != nil {
					t.Errorf("expected table %s to be accessible: %v", table, err)
				}
			}

			var applied int
			if err := store.db.GetContext(ctx, &applied, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
				t.Fatal(err)
			}
			if applied != len(migrations) {
				t.Errorf("schema_migrations count = %d, want %d", applied, len(migrations))
			}

			status, err := store.MigrationStatus(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if status.Pending != 0 {
				t.Errorf("pending = %d, want 0", status.Pending)
			}
		})
	}
}

func TestMigrateIdempotent(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.open(t)
			if err := store.Migrate(context.Background()); err != nil {
				t.Fatalf("second migrate: %v", err)
			}
			var applied int
			if err := store.db.GetContext(context.Background(), &applied, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
				t.Fatal(err)
			}
			if applied != len(migrations) {
				t.Errorf("after second migrate, count = %d, want %d", applied, len(migrations))
			}
		})
	}
}

func TestMigrateRepairsLegacyVersionRegistry(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.openNoMigrate(t)
			ctx := context.Background()
			db := store.db

			for _, stmt := range []string{
				`CREATE TABLE users (id TEXT PRIMARY KEY)`,
				`CREATE TABLE briefing_jobs (id TEXT PRIMARY KEY)`,
				`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at BIGINT NOT NULL)`,
			} {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			for version := 1; version <= 20; version++ {
				if _, err := db.Exec(store.rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`), version); err != nil {
					t.Fatal(err)
				}
			}

			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate legacy database: %v", err)
			}

			for _, table := range []string{"announcements", "app_releases"} {
				var count int
				if err := db.GetContext(ctx, &count, store.rebind(`SELECT COUNT(*) FROM `+table)); err != nil {
					t.Errorf("expected table %s to exist: %v", table, err)
				}
			}
			var payloadCount int
			if err := db.GetContext(ctx, &payloadCount, `SELECT COUNT(payload) FROM briefing_jobs`); err != nil {
				t.Errorf("expected briefing_jobs.payload column: %v", err)
			}
		})
	}
}
