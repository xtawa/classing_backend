package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func TestMigrateRepairsLegacyVersionRegistry(t *testing.T) {
	databaseURL := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", filepath.ToSlash(filepath.Join(t.TempDir(), "legacy.db")))
	db, err := sqlx.Open("sqlite", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	statements := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY)`,
		`CREATE TABLE briefing_jobs (id TEXT PRIMARY KEY)`,
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at BIGINT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for version := 1; version <= 20; version++ {
		if _, err := db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, version); err != nil {
			t.Fatal(err)
		}
	}

	data := &Store{db: db, dialect: "sqlite"}
	if err := data.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}

	for _, table := range []string{"announcements", "app_releases"} {
		var count int
		if err := db.Get(&count, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("expected table %s to exist", table)
		}
	}
	rows, err := db.Queryx(`PRAGMA table_info(briefing_jobs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	foundPayload := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		foundPayload = foundPayload || name == "payload"
	}
	if !foundPayload {
		t.Fatal("expected briefing_jobs.payload to exist")
	}
}
