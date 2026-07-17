package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

type SessionToken struct {
	ID         string `db:"id"`
	UserID     string `db:"user_id"`
	TokenHash  string `db:"token_hash"`
	ExpiresAt  int64  `db:"expires_at"`
	RevokedAt  int64  `db:"revoked_at"`
	ReplacedBy string `db:"replaced_by"`
	SessionID  string `db:"session_id"`
}

// maxVerificationAttempts caps how many times a verification code or email
// change code may be submitted incorrectly before the challenge is locked.
// A 6-digit code has 1M possibilities; 10 attempts gives a ~0.001% brute-force
// probability while staying forgiving for legitimate typos.
const maxVerificationAttempts = 10

func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash, role string) (model.User, error) {
	now := nowMillis()
	user := model.User{
		ID: ids.New("usr"), Username: strings.TrimSpace(username), Email: strings.ToLower(strings.TrimSpace(email)),
		PasswordHash: passwordHash, Role: role, Status: model.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	query := s.rebind(`INSERT INTO users (id, username, email, password_hash, role, status, email_verified, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`)
	if _, err := s.db.ExecContext(ctx, query, user.ID, user.Username, user.Email, user.PasswordHash, user.Role, user.Status, now, now); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	_, _ = s.db.ExecContext(ctx, s.rebind(`INSERT INTO memberships (user_id, tier, expires_at, updated_at, source) VALUES (?, 'FREE', 0, ?, 'SYSTEM')`), user.ID, now)
	return user, nil
}

func (s *Store) CreateOrRefreshPendingUser(ctx context.Context, username, email, passwordHash string) (model.User, error) {
	username = strings.TrimSpace(username)
	email = strings.ToLower(strings.TrimSpace(email))
	for _, identifier := range []string{email, username} {
		existing, err := s.UserByIdentifier(ctx, identifier)
		if err == nil {
			if existing.Status != model.StatusPending || !strings.EqualFold(existing.Email, email) || !strings.EqualFold(existing.Username, username) {
				return model.User{}, ErrConflict
			}
			now := nowMillis()
			_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ? AND status = ?`), passwordHash, now, existing.ID, model.StatusPending)
			if err != nil {
				return model.User{}, normalizeDBError(err)
			}
			return s.UserByID(ctx, existing.ID)
		}
		if err != ErrNotFound {
			return model.User{}, err
		}
	}
	now := nowMillis()
	user := model.User{
		ID: ids.New("usr"), Username: username, Email: email, PasswordHash: passwordHash,
		Role: model.RoleUser, Status: model.StatusPending, CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO users (id, username, email, password_hash, role, status, email_verified, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`), user.ID, user.Username, user.Email, user.PasswordHash, user.Role, user.Status, now, now)
	if err != nil {
		return model.User{}, normalizeDBError(err)
	}
	_, _ = s.db.ExecContext(ctx, s.rebind(`INSERT INTO memberships (user_id, tier, expires_at, updated_at, source) VALUES (?, 'FREE', 0, ?, 'SYSTEM')`), user.ID, now)
	return user, nil
}

func (s *Store) CreateEmailVerificationChallenge(ctx context.Context, userID, codeHash string, expiresAt int64, ip, ua string) (string, error) {
	id := ids.New("evc")
	now := nowMillis()
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var lastCreated int64
	_ = tx.GetContext(ctx, &lastCreated, s.rebind(`SELECT COALESCE(MAX(created_at), 0) FROM email_verification_challenges WHERE user_id = ?`), userID)
	if lastCreated > 0 && now-lastCreated < 60_000 {
		return "", ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE email_verification_challenges SET used_at = ? WHERE user_id = ? AND used_at = 0`), now, userID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO email_verification_challenges (id, user_id, code_hash, expires_at, request_ip, request_ua, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`), id, userID, codeHash, expiresAt, ip, ua, now); err != nil {
		return "", normalizeDBError(err)
	}
	return id, tx.Commit()
}

func (s *Store) CancelEmailVerificationChallenge(ctx context.Context, challengeID string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE email_verification_challenges SET used_at = ? WHERE id = ? AND used_at = 0`), nowMillis(), challengeID)
	return err
}

func (s *Store) CleanupExpiredSecurityData(ctx context.Context) error {
	now := nowMillis()
	cutoff := now - int64((24 * time.Hour).Milliseconds())
	queries := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM email_verification_challenges WHERE expires_at < ? OR (used_at > 0 AND used_at < ?)`, []any{now, cutoff}},
		{`DELETE FROM email_change_requests WHERE expires_at < ? OR (used_at > 0 AND used_at < ?)`, []any{now, cutoff}},
		{`DELETE FROM password_reset_tokens WHERE expires_at < ? OR (used_at > 0 AND used_at < ?)`, []any{now, cutoff}},
		{`DELETE FROM idempotency_keys WHERE expires_at < ?`, []any{now}},
		{`DELETE FROM auth_sessions WHERE (expires_at < ? OR revoked_at > 0) AND created_at < ?`, []any{now, cutoff}},
		{`DELETE FROM runtime_events WHERE created_at < ?`, []any{now - int64((7 * 24 * time.Hour).Milliseconds())}},
	}
	for _, item := range queries {
		if _, err := s.db.ExecContext(ctx, s.rebind(item.query), item.args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ConsumeEmailVerificationChallenge(ctx context.Context, challengeID, codeHash string) (model.User, error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback()
	var row struct {
		ID             string `db:"id"`
		UserID         string `db:"user_id"`
		CodeHash       string `db:"code_hash"`
		ExpiresAt      int64  `db:"expires_at"`
		UsedAt         int64  `db:"used_at"`
		FailedAttempts int    `db:"failed_attempts"`
	}
	if err := tx.GetContext(ctx, &row, s.rebind(`SELECT id, user_id, code_hash, expires_at, used_at, failed_attempts FROM email_verification_challenges WHERE id = ?`), challengeID); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	now := nowMillis()
	if row.UsedAt != 0 || row.ExpiresAt <= now || row.FailedAttempts >= maxVerificationAttempts {
		return model.User{}, ErrForbidden
	}
	if row.CodeHash != codeHash {
		if err := recordFailedAttempt(ctx, tx, `email_verification_challenges`, row.ID, row.FailedAttempts, now); err != nil {
			return model.User{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.User{}, err
		}
		return model.User{}, ErrForbidden
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE email_verification_challenges SET used_at = ? WHERE id = ? AND used_at = 0`), now, row.ID)
	if err != nil {
		return model.User{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.User{}, ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE users SET status = ?, email_verified = 1, updated_at = ? WHERE id = ? AND status = ?`), model.StatusActive, now, row.UserID, model.StatusPending); err != nil {
		return model.User{}, err
	}
	var user model.User
	if err := tx.GetContext(ctx, &user, s.rebind(`SELECT * FROM users WHERE id = ?`), row.UserID); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	return user, tx.Commit()
}

// recordFailedAttempt increments the failed_attempts counter for a challenge
// row and locks it (used_at = now) once the cap is reached, so no further
// attempts are accepted.
func recordFailedAttempt(ctx context.Context, tx *sqlx.Tx, table, rowID string, currentFailures int, now int64) error {
	newAttempts := currentFailures + 1
	if newAttempts >= maxVerificationAttempts {
		_, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET failed_attempts = ?, used_at = ? WHERE id = ?`), newAttempts, now, rowID)
		return err
	}
	_, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET failed_attempts = ? WHERE id = ?`), newAttempts, rowID)
	return err
}

func (s *Store) BootstrapAdmin(ctx context.Context, username, email, password string) (bool, error) {
	if strings.TrimSpace(email) == "" || password == "" {
		return false, nil
	}
	if _, err := s.UserByIdentifier(ctx, email); err == nil {
		return false, nil
	} else if err != ErrNotFound {
		return false, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}
	_, err = s.CreateUser(ctx, username, email, hash, model.RoleAdmin)
	return err == nil, err
}

func (s *Store) UserByIdentifier(ctx context.Context, identifier string) (model.User, error) {
	var user model.User
	value := strings.ToLower(strings.TrimSpace(identifier))
	err := s.db.GetContext(ctx, &user, s.rebind(`SELECT * FROM users WHERE lower(email) = ? OR lower(username) = ? LIMIT 1`), value, value)
	return user, normalizeDBError(err)
}

func (s *Store) UserByID(ctx context.Context, id string) (model.User, error) {
	var user model.User
	err := s.db.GetContext(ctx, &user, s.rebind(`SELECT * FROM users WHERE id = ?`), id)
	return user, normalizeDBError(err)
}

func (s *Store) UpdateUsername(ctx context.Context, id, username string) (model.User, error) {
	now := nowMillis()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE users SET username = ?, updated_at = ? WHERE id = ?`), strings.TrimSpace(username), now, id)
	if err != nil {
		return model.User{}, normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.User{}, ErrNotFound
	}
	return s.UserByID(ctx, id)
}

func (s *Store) CreateEmailChangeRequest(ctx context.Context, userID, newEmail, codeHash string, expiresAt int64, ip, ua string) (string, error) {
	id := ids.New("ecr")
	now := nowMillis()
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var lastCreated int64
	_ = tx.GetContext(ctx, &lastCreated, s.rebind(`SELECT COALESCE(MAX(created_at), 0) FROM email_change_requests WHERE user_id = ?`), userID)
	if lastCreated > 0 && now-lastCreated < 60_000 {
		return "", ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE email_change_requests SET used_at = ? WHERE user_id = ? AND used_at = 0`), now, userID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO email_change_requests (id, user_id, new_email, code_hash, expires_at, request_ip, request_ua, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), id, userID, strings.ToLower(strings.TrimSpace(newEmail)), codeHash, expiresAt, ip, ua, now); err != nil {
		return "", normalizeDBError(err)
	}
	return id, tx.Commit()
}

func (s *Store) ConsumeEmailChangeRequest(ctx context.Context, requestID, codeHash string) (model.User, error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback()
	var row struct {
		ID             string `db:"id"`
		UserID         string `db:"user_id"`
		NewEmail       string `db:"new_email"`
		CodeHash       string `db:"code_hash"`
		ExpiresAt      int64  `db:"expires_at"`
		UsedAt         int64  `db:"used_at"`
		FailedAttempts int    `db:"failed_attempts"`
	}
	if err := tx.GetContext(ctx, &row, s.rebind(`SELECT id, user_id, new_email, code_hash, expires_at, used_at, failed_attempts FROM email_change_requests WHERE id = ?`), requestID); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	now := nowMillis()
	if row.UsedAt != 0 || row.ExpiresAt <= now || row.FailedAttempts >= maxVerificationAttempts {
		return model.User{}, ErrForbidden
	}
	if row.CodeHash != codeHash {
		if err := recordFailedAttempt(ctx, tx, `email_change_requests`, row.ID, row.FailedAttempts, now); err != nil {
			return model.User{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.User{}, err
		}
		return model.User{}, ErrForbidden
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE email_change_requests SET used_at = ? WHERE id = ? AND used_at = 0`), now, row.ID)
	if err != nil {
		return model.User{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.User{}, ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE users SET email = ?, email_verified = 1, auth_epoch = ?, updated_at = ? WHERE id = ?`), row.NewEmail, now, now, row.UserID); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, row.UserID); err != nil {
		return model.User{}, err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, row.UserID); err != nil {
		return model.User{}, err
	}
	var user model.User
	if err := tx.GetContext(ctx, &user, s.rebind(`SELECT * FROM users WHERE id = ?`), row.UserID); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	return user, tx.Commit()
}

func (s *Store) PendingEmailChange(ctx context.Context, userID string) (string, int64, error) {
	var row struct {
		NewEmail  string `db:"new_email"`
		ExpiresAt int64  `db:"expires_at"`
	}
	err := s.db.GetContext(ctx, &row, s.rebind(`SELECT new_email, expires_at FROM email_change_requests WHERE user_id = ? AND used_at = 0 AND expires_at > ? ORDER BY created_at DESC LIMIT 1`), userID, nowMillis())
	if err != nil {
		return "", 0, normalizeDBError(err)
	}
	return row.NewEmail, row.ExpiresAt, nil
}

func (s *Store) UpdatePassword(ctx context.Context, id, hash string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowMillis()
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE users SET password_hash = ?, auth_epoch = ?, updated_at = ? WHERE id = ?`), hash, now, now, id)
	if err != nil {
		return normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteAccount(ctx context.Context, userID string, audit AuditContext) error {
	return s.deleteAccount(ctx, userID, "", false, audit)
}

func (s *Store) AdminDeleteUser(ctx context.Context, actorID, userID string, audit AuditContext) error {
	if strings.TrimSpace(actorID) == "" || actorID == userID {
		return ErrForbidden
	}
	return s.deleteAccount(ctx, userID, actorID, true, audit)
}

func (s *Store) deleteAccount(ctx context.Context, userID, actorID string, allowManagedStatuses bool, audit AuditContext) error {
	if strings.TrimSpace(userID) == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lockSuffix := ""
	if s.dialect == "pgx" {
		lockSuffix = " FOR UPDATE"
	}
	var current model.User
	if err := tx.GetContext(ctx, &current, s.rebind(`SELECT * FROM users WHERE id = ?`)+lockSuffix, userID); err != nil {
		return normalizeDBError(err)
	}
	managedStatus := current.Status == model.StatusDisabled || current.Status == model.StatusPending
	if current.Status != model.StatusActive && !(allowManagedStatuses && managedStatus) {
		return ErrForbidden
	}
	if actorID != "" && current.ID == actorID {
		return ErrForbidden
	}
	if current.Role == model.RoleAdmin {
		remaining := 0
		if s.dialect == "pgx" {
			var activeAdminIDs []string
			if err := tx.SelectContext(ctx, &activeAdminIDs, s.rebind(`SELECT id FROM users WHERE role = ? AND status = ? ORDER BY id FOR UPDATE`), model.RoleAdmin, model.StatusActive); err != nil {
				return err
			}
			for _, adminID := range activeAdminIDs {
				if adminID != userID {
					remaining++
				}
			}
		} else if err := tx.GetContext(ctx, &remaining, s.rebind(`SELECT COUNT(*) FROM users WHERE role = ? AND status = ? AND id <> ?`), model.RoleAdmin, model.StatusActive, userID); err != nil {
			return err
		}
		if remaining == 0 {
			return ErrConflict
		}
	}
	now := nowMillis()
	suffix := current.ID
	if len(suffix) > 32 {
		suffix = suffix[len(suffix)-32:]
	}
	deletedUsername := "deleted_" + suffix
	deletedEmail := "deleted+" + current.ID + "@deleted.classing.local"
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE users SET username = ?, email = ?, password_hash = '', status = ?, email_verified = 0, auth_epoch = ?, updated_at = ? WHERE id = ? AND status = ?`), deletedUsername, deletedEmail, model.StatusDeleted, now, now, userID, current.Status)
	if err != nil {
		return normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE memberships SET tier = 'FREE', expires_at = 0, updated_at = ?, source = 'ACCOUNT_DELETE' WHERE user_id = ?`), now, userID); err != nil {
		return err
	}
	audit.TargetID = userID
	audit.Metadata = map[string]any{"deletedAt": now}
	if err := s.auditInTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt int64, ip, ua string) (string, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := nowMillis()
	sessionID := ids.New("ses")
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO auth_sessions (id, user_id, expires_at, created_at, last_seen_at, ip_address, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)`), sessionID, userID, expiresAt, now, now, ip, ua); err != nil {
		return "", normalizeDBError(err)
	}
	refreshID := ids.New("rft")
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at, ip_address, user_agent, session_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), refreshID, userID, tokenHash, expiresAt, now, ip, ua, sessionID); err != nil {
		return "", normalizeDBError(err)
	}
	return sessionID, tx.Commit()
}

func (s *Store) RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt int64, ip, ua string) (model.User, error) {
	user, _, err := s.RotateRefreshTokenSession(ctx, oldHash, newHash, newExpiresAt, ip, ua)
	return user, err
}

func (s *Store) RotateRefreshTokenSession(ctx context.Context, oldHash, newHash string, newExpiresAt int64, ip, ua string) (model.User, string, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.User{}, "", err
	}
	defer tx.Rollback()
	var token SessionToken
	if err := tx.GetContext(ctx, &token, s.rebind(`SELECT id, user_id, token_hash, expires_at, revoked_at, replaced_by, session_id FROM refresh_tokens WHERE token_hash = ?`), oldHash); err != nil {
		return model.User{}, "", normalizeDBError(err)
	}
	now := nowMillis()
	if token.RevokedAt != 0 || token.ExpiresAt <= now {
		if token.SessionID != "" {
			_, _ = tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE id = ? AND revoked_at = 0`), now, token.SessionID)
			_ = tx.Commit()
		}
		return model.User{}, "", ErrForbidden
	}
	if token.SessionID == "" {
		return model.User{}, "", ErrForbidden
	}
	var active int
	if err := tx.GetContext(ctx, &active, s.rebind(`SELECT COUNT(*) FROM auth_sessions WHERE id = ? AND user_id = ? AND revoked_at = 0 AND expires_at > ?`), token.SessionID, token.UserID, now); err != nil || active != 1 {
		return model.User{}, "", ErrForbidden
	}
	newID := ids.New("rft")
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ?, replaced_by = ? WHERE id = ? AND revoked_at = 0`), now, newID, token.ID)
	if err != nil {
		return model.User{}, "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.User{}, "", ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at, ip_address, user_agent, session_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), newID, token.UserID, newHash, newExpiresAt, now, ip, ua, token.SessionID); err != nil {
		return model.User{}, "", err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET expires_at = ?, last_seen_at = ?, ip_address = ?, user_agent = ? WHERE id = ?`), newExpiresAt, now, ip, ua, token.SessionID); err != nil {
		return model.User{}, "", err
	}
	var user model.User
	if err := tx.GetContext(ctx, &user, s.rebind(`SELECT * FROM users WHERE id = ?`), token.UserID); err != nil {
		return model.User{}, "", normalizeDBError(err)
	}
	if user.Status != model.StatusActive {
		return model.User{}, "", ErrForbidden
	}
	return user, token.SessionID, tx.Commit()
}

func (s *Store) RevokeRefreshToken(ctx context.Context, userID, tokenHash string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.GetContext(ctx, &sessionID, s.rebind(`SELECT session_id FROM refresh_tokens WHERE user_id = ? AND token_hash = ?`), userID, tokenHash); err != nil {
		return normalizeDBError(err)
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE session_id = ? AND revoked_at = 0`), now, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE id = ? AND revoked_at = 0`), now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeSession(ctx context.Context, userID, sessionID string) error {
	if userID == "" || sessionID == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowMillis()
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at = 0`), now, sessionID, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE session_id = ? AND user_id = ? AND revoked_at = 0`), now, sessionID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SessionActive(ctx context.Context, userID, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	var count int
	err := s.db.GetContext(ctx, &count, s.rebind(`SELECT COUNT(*) FROM auth_sessions WHERE id = ? AND user_id = ? AND revoked_at = 0 AND expires_at > ?`), sessionID, userID, nowMillis())
	return count == 1, err
}

func (s *Store) CreateResetToken(ctx context.Context, userID, hash string, expiresAt int64, ip, ua string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at, request_ip, request_ua, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`), ids.New("rst"), userID, hash, expiresAt, ip, ua, nowMillis())
	return normalizeDBError(err)
}

func (s *Store) CancelResetToken(ctx context.Context, userID, hash string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM password_reset_tokens WHERE user_id = ? AND token_hash = ? AND used_at = 0`), userID, hash)
	return err
}

func (s *Store) ConsumeResetToken(ctx context.Context, tokenHash, newPasswordHash string) (string, error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var row struct {
		ID             string `db:"id"`
		UserID         string `db:"user_id"`
		ExpiresAt      int64  `db:"expires_at"`
		UsedAt         int64  `db:"used_at"`
		FailedAttempts int    `db:"failed_attempts"`
	}
	if err := tx.GetContext(ctx, &row, s.rebind(`SELECT id, user_id, expires_at, used_at, failed_attempts FROM password_reset_tokens WHERE token_hash = ?`), tokenHash); err != nil {
		return "", normalizeDBError(err)
	}
	now := nowMillis()
	if row.UsedAt != 0 || row.ExpiresAt <= now || row.FailedAttempts >= maxVerificationAttempts {
		return "", ErrForbidden
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE password_reset_tokens SET used_at = ? WHERE id = ? AND used_at = 0`), now, row.ID)
	if err != nil {
		return "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return "", ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE users SET password_hash = ?, auth_epoch = ?, updated_at = ? WHERE id = ?`), newPasswordHash, now, now, row.UserID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, row.UserID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, row.UserID); err != nil {
		return "", err
	}
	return row.UserID, tx.Commit()
}

func (s *Store) ListUsers(ctx context.Context, limit, offset int, query string) ([]model.User, int, error) {
	where := ` WHERE status <> ?`
	args := []any{model.StatusDeleted}
	if strings.TrimSpace(query) != "" {
		where = ` WHERE lower(username) LIKE ? OR lower(email) LIKE ?`
		args = args[:0]
		term := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
		args = append(args, term, term)
	}
	var total int
	if err := s.db.GetContext(ctx, &total, s.rebind(`SELECT COUNT(*) FROM users`+where), args...); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	users := []model.User{}
	if err := s.db.SelectContext(ctx, &users, s.rebind(`SELECT * FROM users`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`), args...); err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

func (s *Store) AdminUpdateUser(ctx context.Context, actorID, id, role, status string, audit AuditContext) (model.User, error) {
	if role != model.RoleAdmin && role != model.RoleUser {
		return model.User{}, ErrInvalid
	}
	if status != model.StatusActive && status != model.StatusDisabled {
		return model.User{}, ErrInvalid
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback()
	lockSuffix := ""
	if s.dialect == "pgx" {
		lockSuffix = " FOR UPDATE"
	}
	var current model.User
	if err := tx.GetContext(ctx, &current, s.rebind(`SELECT * FROM users WHERE id = ?`)+lockSuffix, id); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	removingAdmin := current.Role == model.RoleAdmin && current.Status == model.StatusActive && (role != model.RoleAdmin || status != model.StatusActive)
	if id == actorID && removingAdmin {
		return model.User{}, ErrForbidden
	}
	if removingAdmin {
		remaining := 0
		if s.dialect == "pgx" {
			var activeAdminIDs []string
			if err := tx.SelectContext(ctx, &activeAdminIDs, s.rebind(`SELECT id FROM users WHERE role = ? AND status = ? ORDER BY id FOR UPDATE`), model.RoleAdmin, model.StatusActive); err != nil {
				return model.User{}, err
			}
			for _, adminID := range activeAdminIDs {
				if adminID != id {
					remaining++
				}
			}
		} else if err := tx.GetContext(ctx, &remaining, s.rebind(`SELECT COUNT(*) FROM users WHERE role = ? AND status = ? AND id <> ?`), model.RoleAdmin, model.StatusActive, id); err != nil {
			return model.User{}, err
		}
		if remaining == 0 {
			return model.User{}, ErrConflict
		}
	}
	now := nowMillis()
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE users SET role = ?, status = ?, auth_epoch = ?, updated_at = ? WHERE id = ?`), role, status, now, now, id)
	if err != nil {
		return model.User{}, normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.User{}, ErrNotFound
	}
	if role != current.Role || status != current.Status {
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, id); err != nil {
			return model.User{}, err
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE auth_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), now, id); err != nil {
			return model.User{}, err
		}
	}
	audit.TargetID = id
	if err := s.auditInTx(ctx, tx, audit); err != nil {
		return model.User{}, err
	}
	var updated model.User
	if err := tx.GetContext(ctx, &updated, s.rebind(`SELECT * FROM users WHERE id = ?`), id); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	return updated, tx.Commit()
}

func txGet[T any](ctx context.Context, tx *sqlx.Tx, query string, args ...any) (T, error) {
	var value T
	err := tx.GetContext(ctx, &value, query, args...)
	return value, err
}
