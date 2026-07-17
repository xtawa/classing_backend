package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

var (
	ErrDeviceAuthorizationPending  = errors.New("device authorization pending")
	ErrDeviceAuthorizationExpired  = errors.New("device authorization expired")
	ErrDeviceAuthorizationConsumed = errors.New("device authorization consumed")
)

type deviceAuthorizationRow struct {
	ID             string         `db:"id"`
	PollSecretHash string         `db:"poll_secret_hash"`
	UserID         sql.NullString `db:"user_id"`
	ExpiresAt      int64          `db:"expires_at"`
	ApprovedAt     int64          `db:"approved_at"`
	ConsumedAt     int64          `db:"consumed_at"`
}

func (s *Store) CreateDeviceAuthorization(
	ctx context.Context,
	id string,
	pollSecretHash string,
	deviceName string,
	expiresAt int64,
	ip string,
	ua string,
) error {
	now := nowMillis()
	_, _ = s.db.ExecContext(ctx, s.rebind(`DELETE FROM device_authorizations WHERE expires_at < ?`), now-24*60*60*1000)
	_, err := s.db.ExecContext(
		ctx,
		s.rebind(`INSERT INTO device_authorizations (id, poll_secret_hash, device_name, expires_at, request_ip, request_ua, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		id,
		pollSecretHash,
		deviceName,
		expiresAt,
		ip,
		ua,
		now,
	)
	return normalizeDBError(err)
}

func (s *Store) ApproveDeviceAuthorization(ctx context.Context, id string, userID string) (int64, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var item deviceAuthorizationRow
	if err := tx.GetContext(ctx, &item, s.rebind(`SELECT id, poll_secret_hash, user_id, expires_at, approved_at, consumed_at FROM device_authorizations WHERE id = ?`), id); err != nil {
		return 0, normalizeDBError(err)
	}
	now := nowMillis()
	if item.ExpiresAt <= now {
		return 0, ErrDeviceAuthorizationExpired
	}
	if item.ConsumedAt != 0 {
		return 0, ErrDeviceAuthorizationConsumed
	}
	if item.ApprovedAt != 0 {
		if item.UserID.Valid && item.UserID.String == userID {
			return item.ExpiresAt, nil
		}
		return 0, ErrConflict
	}
	result, err := tx.ExecContext(
		ctx,
		s.rebind(`UPDATE device_authorizations SET user_id = ?, approved_at = ? WHERE id = ? AND approved_at = 0 AND consumed_at = 0`),
		userID,
		now,
		id,
	)
	if err != nil {
		return 0, normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return 0, ErrConflict
	}
	return item.ExpiresAt, tx.Commit()
}

func (s *Store) ConsumeDeviceAuthorizationAndCreateSession(
	ctx context.Context,
	id string,
	pollSecretHash string,
	refreshTokenHash string,
	refreshExpiresAt int64,
	ip string,
	ua string,
) (model.User, string, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.User{}, "", err
	}
	defer tx.Rollback()

	var item deviceAuthorizationRow
	if err := tx.GetContext(
		ctx,
		&item,
		s.rebind(`SELECT id, poll_secret_hash, user_id, expires_at, approved_at, consumed_at FROM device_authorizations WHERE id = ? AND poll_secret_hash = ?`),
		id,
		pollSecretHash,
	); err != nil {
		return model.User{}, "", normalizeDBError(err)
	}
	now := nowMillis()
	if item.ExpiresAt <= now {
		return model.User{}, "", ErrDeviceAuthorizationExpired
	}
	if item.ConsumedAt != 0 {
		return model.User{}, "", ErrDeviceAuthorizationConsumed
	}
	if item.ApprovedAt == 0 || !item.UserID.Valid || item.UserID.String == "" {
		return model.User{}, "", ErrDeviceAuthorizationPending
	}

	var user model.User
	if err := tx.GetContext(ctx, &user, s.rebind(`SELECT * FROM users WHERE id = ?`), item.UserID.String); err != nil {
		return model.User{}, "", normalizeDBError(err)
	}
	if user.Status != model.StatusActive {
		return model.User{}, "", ErrForbidden
	}

	result, err := tx.ExecContext(
		ctx,
		s.rebind(`UPDATE device_authorizations SET consumed_at = ? WHERE id = ? AND consumed_at = 0`),
		now,
		id,
	)
	if err != nil {
		return model.User{}, "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.User{}, "", ErrDeviceAuthorizationConsumed
	}

	sessionID := ids.New("ses")
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO auth_sessions (id, user_id, expires_at, created_at, last_seen_at, ip_address, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)`), sessionID, user.ID, refreshExpiresAt, now, now, ip, ua); err != nil {
		return model.User{}, "", normalizeDBError(err)
	}
	refreshID := ids.New("rft")
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at, ip_address, user_agent, session_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), refreshID, user.ID, refreshTokenHash, refreshExpiresAt, now, ip, ua, sessionID); err != nil {
		return model.User{}, "", normalizeDBError(err)
	}

	return user, sessionID, tx.Commit()
}
