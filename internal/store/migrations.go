package store

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Migrations are append-only once a release has been deployed. Existing
// installations identify migrations by their one-based position in this slice.
//
// Idempotency rules for new entries:
//   - Prefer CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS.
//   - SQLite does not support ALTER TABLE ... ADD COLUMN IF NOT EXISTS, so
//     any new ALTER TABLE ADD COLUMN must be wrapped in an ensure*Column
//     helper (see ensureFailedAttemptsColumns) rather than added directly to
//     this slice. This keeps the migration idempotent across both dialects.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'USER',
		status TEXT NOT NULL DEFAULT 'ACTIVE',
		email_verified INTEGER NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS refresh_tokens (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash TEXT NOT NULL UNIQUE,
		expires_at BIGINT NOT NULL,
		revoked_at BIGINT NOT NULL DEFAULT 0,
		replaced_by TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		ip_address TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_refresh_user ON refresh_tokens(user_id)`,
	`ALTER TABLE users ADD COLUMN auth_epoch BIGINT NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS password_reset_tokens (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash TEXT NOT NULL UNIQUE,
		expires_at BIGINT NOT NULL,
		used_at BIGINT NOT NULL DEFAULT 0,
		request_ip TEXT NOT NULL DEFAULT '',
		request_ua TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS memberships (
		user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		tier TEXT NOT NULL DEFAULT 'FREE',
		expires_at BIGINT NOT NULL DEFAULT 0,
		updated_at BIGINT NOT NULL,
		source TEXT NOT NULL DEFAULT 'SYSTEM'
	)`,
	`CREATE TABLE IF NOT EXISTS membership_events (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		action TEXT NOT NULL,
		tier TEXT NOT NULL,
		old_expires_at BIGINT NOT NULL DEFAULT 0,
		new_expires_at BIGINT NOT NULL DEFAULT 0,
		source TEXT NOT NULL,
		actor_id TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS redeem_codes (
		code TEXT PRIMARY KEY,
		code_type TEXT NOT NULL,
		grant_days INTEGER NOT NULL,
		max_redemptions INTEGER NOT NULL,
		current_redemptions INTEGER NOT NULL DEFAULT 0,
		expires_at BIGINT NOT NULL DEFAULT 0,
		revoked_at BIGINT NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL REFERENCES users(id),
		created_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS redeem_redemptions (
		id TEXT PRIMARY KEY,
		code TEXT NOT NULL REFERENCES redeem_codes(code),
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		grant_days INTEGER NOT NULL,
		redeemed_at BIGINT NOT NULL,
		UNIQUE(code, user_id)
	)`,
	`CREATE TABLE IF NOT EXISTS timetable_projects (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
		semester_start TEXT NOT NULL DEFAULT '',
		week_count INTEGER NOT NULL DEFAULT 20,
		document TEXT NOT NULL DEFAULT '{}',
		version BIGINT NOT NULL DEFAULT 1,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_timetables_owner ON timetable_projects(owner_id, updated_at)`,
	`CREATE TABLE IF NOT EXISTS cloud_documents (
		user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		payload TEXT NOT NULL,
		version BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS idempotency_keys (
		key_value TEXT NOT NULL,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		request_hash TEXT NOT NULL,
		response_code INTEGER NOT NULL,
		response_body TEXT NOT NULL,
		expires_at BIGINT NOT NULL,
		PRIMARY KEY(key_value, user_id)
	)`,
	`CREATE TABLE IF NOT EXISTS briefing_subscriptions (
		user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		enabled INTEGER NOT NULL DEFAULT 0,
		channel TEXT NOT NULL DEFAULT 'APP_NOTIFICATION',
		delivery_time TEXT NOT NULL DEFAULT '20:00',
		timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
		last_scheduled_at BIGINT NOT NULL DEFAULT 0,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS mailboxes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		smtp_host TEXT NOT NULL,
		smtp_port INTEGER NOT NULL,
		username TEXT NOT NULL,
		password_secret_ref TEXT NOT NULL,
		from_address TEXT NOT NULL,
		daily_quota INTEGER NOT NULL,
		used_today INTEGER NOT NULL DEFAULT 0,
		usage_date TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS briefing_jobs (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		target_date TEXT NOT NULL,
		channel TEXT NOT NULL,
		status TEXT NOT NULL,
		provider_mailbox_id TEXT NOT NULL DEFAULT '',
		retry_count INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		scheduled_at BIGINT NOT NULL,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL,
		UNIQUE(user_id, target_date, channel)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_briefing_jobs_status ON briefing_jobs(status, scheduled_at)`,
	`ALTER TABLE briefing_jobs ADD COLUMN payload TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS announcements (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		content TEXT NOT NULL,
		platform TEXT NOT NULL DEFAULT '',
		priority INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1,
		publish_at BIGINT NOT NULL,
		expires_at BIGINT NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL REFERENCES users(id),
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_announcements_public ON announcements(active, platform, publish_at, expires_at)`,
	`CREATE TABLE IF NOT EXISTS app_releases (
		id TEXT PRIMARY KEY,
		platform TEXT NOT NULL,
		channel TEXT NOT NULL,
		version_code BIGINT NOT NULL,
		version_name TEXT NOT NULL,
		min_supported_version_code BIGINT NOT NULL DEFAULT 0,
		title TEXT NOT NULL,
		changelog TEXT NOT NULL DEFAULT '',
		mandatory INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'DRAFT',
		artifact_file_name TEXT NOT NULL,
		artifact_storage_name TEXT NOT NULL,
		artifact_size BIGINT NOT NULL,
		artifact_sha256 TEXT NOT NULL,
		artifact_mime_type TEXT NOT NULL,
		created_by TEXT NOT NULL REFERENCES users(id),
		published_at BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL,
		UNIQUE(platform, channel, version_code)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_app_releases_latest ON app_releases(platform, channel, status, version_code)`,
	`CREATE TABLE IF NOT EXISTS audit_logs (
		id TEXT PRIMARY KEY,
		actor_id TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		ip_address TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}',
		created_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at)`,
	`CREATE TABLE IF NOT EXISTS system_settings (
		setting_key TEXT PRIMARY KEY,
		setting_value TEXT NOT NULL,
		updated_by TEXT NOT NULL DEFAULT '',
		updated_at BIGINT NOT NULL
	)`,
	// Repair migrations for installations upgraded from fdac502. The first
	// announcements migrations were inserted before the old audit/settings
	// entries, whose versions were already recorded, so they were skipped.
	`CREATE TABLE IF NOT EXISTS announcements (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		content TEXT NOT NULL,
		platform TEXT NOT NULL DEFAULT '',
		priority INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1,
		publish_at BIGINT NOT NULL,
		expires_at BIGINT NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL REFERENCES users(id),
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_announcements_public ON announcements(active, platform, publish_at, expires_at)`,
	`CREATE TABLE IF NOT EXISTS email_verification_challenges (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		code_hash TEXT NOT NULL,
		expires_at BIGINT NOT NULL,
		used_at BIGINT NOT NULL DEFAULT 0,
		request_ip TEXT NOT NULL DEFAULT '',
		request_ua TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_email_verification_user ON email_verification_challenges(user_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS email_change_requests (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		new_email TEXT NOT NULL,
		code_hash TEXT NOT NULL,
		expires_at BIGINT NOT NULL,
		used_at BIGINT NOT NULL DEFAULT 0,
		request_ip TEXT NOT NULL DEFAULT '',
		request_ua TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_email_change_user ON email_change_requests(user_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS auth_sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		revoked_at BIGINT NOT NULL DEFAULT 0,
		expires_at BIGINT NOT NULL,
		created_at BIGINT NOT NULL,
		last_seen_at BIGINT NOT NULL,
		ip_address TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id, revoked_at, expires_at)`,
	`CREATE TABLE IF NOT EXISTS runtime_events (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL DEFAULT '',
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		created_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_runtime_events_user_id ON runtime_events(user_id, id)`,
	`CREATE TABLE IF NOT EXISTS briefing_job_logs (
		id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL REFERENCES briefing_jobs(id) ON DELETE CASCADE,
		level TEXT NOT NULL,
		event TEXT NOT NULL,
		message TEXT NOT NULL,
		details TEXT NOT NULL DEFAULT '{}',
		created_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_briefing_job_logs_job ON briefing_job_logs(job_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS ai_config (
		id INTEGER PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0,
		provider_kind TEXT NOT NULL DEFAULT 'OPENAI_COMPATIBLE',
		base_url TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		secret_ref TEXT NOT NULL DEFAULT '',
		system_prompt TEXT NOT NULL DEFAULT '',
		timetable_prompt TEXT NOT NULL DEFAULT '',
		temperature REAL NOT NULL DEFAULT 0.2,
		max_output_tokens INTEGER NOT NULL DEFAULT 1024,
		timeout_seconds INTEGER NOT NULL DEFAULT 60,
		max_history_messages INTEGER NOT NULL DEFAULT 40,
		default_monthly_limit INTEGER NOT NULL DEFAULT 0,
		quota_timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
		version BIGINT NOT NULL DEFAULT 1,
		updated_by TEXT NOT NULL DEFAULT '',
		updated_at BIGINT NOT NULL DEFAULT 0,
		CHECK (id = 1)
	)`,
	`INSERT INTO ai_config (id, updated_at) SELECT 1, 0 WHERE NOT EXISTS (SELECT 1 FROM ai_config WHERE id = 1)`,
	`CREATE TABLE IF NOT EXISTS ai_user_quotas (
		user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		mode TEXT NOT NULL DEFAULT 'INHERIT',
		monthly_limit INTEGER NOT NULL DEFAULT 0,
		updated_by TEXT NOT NULL DEFAULT '',
		updated_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS ai_usage_monthly (
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		period TEXT NOT NULL,
		used INTEGER NOT NULL DEFAULT 0,
		reserved INTEGER NOT NULL DEFAULT 0,
		updated_at BIGINT NOT NULL,
		PRIMARY KEY (user_id, period)
	)`,
	`CREATE TABLE IF NOT EXISTS ai_conversations (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		title TEXT NOT NULL,
		timetable_snapshot TEXT NOT NULL,
		timetable_hash TEXT NOT NULL,
		source_project_id TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_conversations_user ON ai_conversations(user_id, updated_at DESC)`,
	`CREATE TABLE IF NOT EXISTS ai_messages (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		content TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'COMPLETE',
		client_request_id TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		completed_at BIGINT NOT NULL DEFAULT 0,
		UNIQUE (conversation_id, client_request_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_messages_conversation ON ai_messages(conversation_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS ai_requests (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		conversation_id TEXT NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
		client_request_id TEXT NOT NULL,
		provider_kind TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		quota_counted INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		latency_ms INTEGER NOT NULL DEFAULT 0,
		error_code TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		completed_at BIGINT NOT NULL DEFAULT 0,
		UNIQUE (user_id, client_request_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_requests_user_period ON ai_requests(user_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS device_authorizations (
		id TEXT PRIMARY KEY,
		poll_secret_hash TEXT NOT NULL UNIQUE,
		user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
		device_name TEXT NOT NULL DEFAULT '',
		expires_at BIGINT NOT NULL,
		approved_at BIGINT NOT NULL DEFAULT 0,
		consumed_at BIGINT NOT NULL DEFAULT 0,
		request_ip TEXT NOT NULL DEFAULT '',
		request_ua TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_device_authorizations_expiry ON device_authorizations(expires_at, consumed_at)`,
}

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at BIGINT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration registry: %w", err)
	}
	for index, migration := range migrations {
		version := index + 1
		var applied int
		if err := tx.GetContext(ctx, &applied, s.rebind(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`), version); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`), version, nowMillis()); err != nil {
			return fmt.Errorf("record migration %d: %w", version, err)
		}
	}
	if err := s.ensureBriefingJobsPayload(ctx, tx); err != nil {
		return err
	}
	if err := s.ensureFailedAttemptsColumns(ctx, tx); err != nil {
		return err
	}
	if err := s.ensureRefreshTokenSessionColumn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func (s *Store) ensureRefreshTokenSessionColumn(ctx context.Context, tx *sqlx.Tx) error {
	present, err := s.tableExists(ctx, tx, "refresh_tokens")
	if err != nil || !present {
		return err
	}
	hasColumn, err := s.columnExists(ctx, tx, "refresh_tokens", "session_id")
	if err != nil {
		return fmt.Errorf("check refresh_tokens.session_id: %w", err)
	}
	if hasColumn {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE refresh_tokens ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add refresh_tokens.session_id: %w", err)
	}
	return nil
}

// MigrationStatus reports how many migrations have been applied versus how
// many are available. It does NOT run pending migrations.
type MigrationStatus struct {
	Applied   int
	Available int
	Pending   int
}

func (s *Store) MigrationStatus(ctx context.Context) (MigrationStatus, error) {
	available := len(migrations)
	exists, err := s.schemaMigrationsExists(ctx)
	if err != nil {
		return MigrationStatus{}, err
	}
	if !exists {
		return MigrationStatus{Applied: 0, Available: available, Pending: available}, nil
	}
	var applied int
	if err := s.db.GetContext(ctx, &applied, s.rebind(`SELECT COUNT(*) FROM schema_migrations`)); err != nil {
		return MigrationStatus{}, fmt.Errorf("query schema_migrations: %w", err)
	}
	return MigrationStatus{Applied: applied, Available: available, Pending: available - applied}, nil
}

func (s *Store) schemaMigrationsExists(ctx context.Context) (bool, error) {
	switch s.dialect {
	case "pgx":
		var count int
		if err := s.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'schema_migrations'`); err != nil {
			return false, err
		}
		return count > 0, nil
	case "sqlite":
		var count int
		if err := s.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`); err != nil {
			return false, err
		}
		return count > 0, nil
	default:
		return false, fmt.Errorf("unsupported database dialect %q", s.dialect)
	}
}

func (s *Store) ensureBriefingJobsPayload(ctx context.Context, tx *sqlx.Tx) error {
	var exists bool
	switch s.dialect {
	case "pgx":
		var count int
		if err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'briefing_jobs' AND column_name = 'payload'`); err != nil {
			return fmt.Errorf("check briefing job payload column: %w", err)
		}
		exists = count > 0
	case "sqlite":
		rows, err := tx.QueryxContext(ctx, `PRAGMA table_info(briefing_jobs)`)
		if err != nil {
			return fmt.Errorf("inspect briefing job columns: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				return fmt.Errorf("scan briefing job column: %w", err)
			}
			if name == "payload" {
				exists = true
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("inspect briefing job columns: %w", err)
		}
	default:
		return fmt.Errorf("unsupported database dialect %q", s.dialect)
	}
	if exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE briefing_jobs ADD COLUMN payload TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add briefing job payload column: %w", err)
	}
	return nil
}

// ensureFailedAttemptsColumns idempotently adds the failed_attempts column to
// the verification-code and reset-token tables. It skips tables that do not
// exist yet (legacy databases where the CREATE TABLE migration was recorded
// but never actually ran), so it is safe to call after every migration pass.
func (s *Store) ensureFailedAttemptsColumns(ctx context.Context, tx *sqlx.Tx) error {
	for _, table := range []string{"email_verification_challenges", "email_change_requests", "password_reset_tokens"} {
		present, err := s.tableExists(ctx, tx, table)
		if err != nil {
			return fmt.Errorf("check table %s: %w", table, err)
		}
		if !present {
			continue
		}
		hasColumn, err := s.columnExists(ctx, tx, table, "failed_attempts")
		if err != nil {
			return fmt.Errorf("check %s.failed_attempts: %w", table, err)
		}
		if hasColumn {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0`, table)); err != nil {
			return fmt.Errorf("add %s.failed_attempts: %w", table, err)
		}
	}
	return nil
}

func (s *Store) tableExists(ctx context.Context, tx *sqlx.Tx, table string) (bool, error) {
	switch s.dialect {
	case "pgx":
		var count int
		if err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1`, table); err != nil {
			return false, err
		}
		return count > 0, nil
	case "sqlite":
		var count int
		if err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); err != nil {
			return false, err
		}
		return count > 0, nil
	default:
		return false, fmt.Errorf("unsupported database dialect %q", s.dialect)
	}
}

func (s *Store) columnExists(ctx context.Context, tx *sqlx.Tx, table, column string) (bool, error) {
	switch s.dialect {
	case "pgx":
		var count int
		if err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column); err != nil {
			return false, err
		}
		return count > 0, nil
	case "sqlite":
		rows, err := tx.QueryxContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				return false, err
			}
			if name == column {
				return true, nil
			}
		}
		return false, rows.Err()
	default:
		return false, fmt.Errorf("unsupported database dialect %q", s.dialect)
	}
}
