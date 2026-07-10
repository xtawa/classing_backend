package store

import (
	"context"
	"fmt"
)

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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
