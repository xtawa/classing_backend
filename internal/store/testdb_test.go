package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// testDialect represents a database dialect available for testing.
type testDialect struct {
	name string
	open func(t *testing.T) *Store
	// openNoMigrate opens a clean database without running migrations.
	// Used by tests that need to set up a dirty state before calling Migrate.
	openNoMigrate func(t *testing.T) *Store
}

// testDialects returns SQLite always, plus PostgreSQL when TEST_POSTGRES_DSN
// is set. Tests using this helper must NOT call t.Parallel() because the
// PostgreSQL harness recreates the public schema.
func testDialects(t *testing.T) []testDialect {
	t.Helper()
	dialects := []testDialect{
		{name: "sqlite", open: newSQLiteTestStore, openNoMigrate: newSQLiteTestStoreNoMigrate},
	}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		dialects = append(dialects, testDialect{
			name:          "pgx",
			open:          func(t *testing.T) *Store { return newPostgresTestStore(t, dsn, true) },
			openNoMigrate: func(t *testing.T) *Store { return newPostgresTestStore(t, dsn, false) },
		})
	}
	return dialects
}

func newSQLiteTestStore(t *testing.T) *Store {
	t.Helper()
	return newSQLiteTestStoreNoMigrate(t).migrateForTest(t)
}

func newSQLiteTestStoreNoMigrate(t *testing.T) *Store {
	t.Helper()
	databaseURL := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", filepath.ToSlash(filepath.Join(t.TempDir(), "test.db")))
	db, err := sqlx.Open("sqlite", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return &Store{db: db, dialect: "sqlite"}
}

func newPostgresTestStore(t *testing.T, dsn string, migrate bool) *Store {
	t.Helper()
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS public CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA public`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := &Store{db: db, dialect: "pgx"}
	if migrate {
		store.migrateForTest(t)
	}
	return store
}

func (s *Store) migrateForTest(t *testing.T) *Store {
	t.Helper()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}
