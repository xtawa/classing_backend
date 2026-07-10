package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
}

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

func (s *Store) UpdateProfile(ctx context.Context, id, username, email string) (model.User, error) {
	now := nowMillis()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE users SET username = ?, email = ?, updated_at = ? WHERE id = ?`), strings.TrimSpace(username), strings.ToLower(strings.TrimSpace(email)), now, id)
	if err != nil {
		return model.User{}, normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.User{}, ErrNotFound
	}
	return s.UserByID(ctx, id)
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
	return tx.Commit()
}

func (s *Store) CreateRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt int64, ip, ua string) (string, error) {
	id := ids.New("rft")
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at, ip_address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`), id, userID, tokenHash, expiresAt, nowMillis(), ip, ua)
	return id, normalizeDBError(err)
}

func (s *Store) RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt int64, ip, ua string) (model.User, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback()
	var token SessionToken
	if err := tx.GetContext(ctx, &token, s.rebind(`SELECT id, user_id, token_hash, expires_at, revoked_at, replaced_by FROM refresh_tokens WHERE token_hash = ?`), oldHash); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	now := nowMillis()
	if token.RevokedAt != 0 || token.ExpiresAt <= now {
		return model.User{}, ErrForbidden
	}
	newID := ids.New("rft")
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ?, replaced_by = ? WHERE id = ? AND revoked_at = 0`), now, newID, token.ID)
	if err != nil {
		return model.User{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.User{}, ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at, ip_address, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)`), newID, token.UserID, newHash, newExpiresAt, now, ip, ua); err != nil {
		return model.User{}, err
	}
	var user model.User
	if err := tx.GetContext(ctx, &user, s.rebind(`SELECT * FROM users WHERE id = ?`), token.UserID); err != nil {
		return model.User{}, normalizeDBError(err)
	}
	if user.Status != model.StatusActive {
		return model.User{}, ErrForbidden
	}
	return user, tx.Commit()
}

func (s *Store) RevokeRefreshToken(ctx context.Context, userID, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND token_hash = ? AND revoked_at = 0`), nowMillis(), userID, tokenHash)
	return err
}

func (s *Store) CreateResetToken(ctx context.Context, userID, hash string, expiresAt int64, ip, ua string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at, request_ip, request_ua, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`), ids.New("rst"), userID, hash, expiresAt, ip, ua, nowMillis())
	return normalizeDBError(err)
}

func (s *Store) ConsumeResetToken(ctx context.Context, tokenHash, newPasswordHash string) (string, error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var row struct {
		ID        string `db:"id"`
		UserID    string `db:"user_id"`
		ExpiresAt int64  `db:"expires_at"`
		UsedAt    int64  `db:"used_at"`
	}
	if err := tx.GetContext(ctx, &row, s.rebind(`SELECT id, user_id, expires_at, used_at FROM password_reset_tokens WHERE token_hash = ?`), tokenHash); err != nil {
		return "", normalizeDBError(err)
	}
	now := nowMillis()
	if row.UsedAt != 0 || row.ExpiresAt <= now {
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
	return row.UserID, tx.Commit()
}

func (s *Store) ListUsers(ctx context.Context, limit, offset int, query string) ([]model.User, int, error) {
	where := ""
	args := []any{}
	if strings.TrimSpace(query) != "" {
		where = ` WHERE lower(username) LIKE ? OR lower(email) LIKE ?`
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

func (s *Store) AdminUpdateUser(ctx context.Context, id, role, status string) (model.User, error) {
	if role != model.RoleAdmin && role != model.RoleUser {
		return model.User{}, ErrInvalid
	}
	if status != model.StatusActive && status != model.StatusDisabled {
		return model.User{}, ErrInvalid
	}
	now := nowMillis()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE users SET role = ?, status = ?, auth_epoch = ?, updated_at = ? WHERE id = ?`), role, status, now, now, id)
	if err != nil {
		return model.User{}, normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.User{}, ErrNotFound
	}
	if status == model.StatusDisabled {
		_, _ = s.db.ExecContext(ctx, s.rebind(`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0`), nowMillis(), id)
	}
	return s.UserByID(ctx, id)
}

func txGet[T any](ctx context.Context, tx *sqlx.Tx, query string, args ...any) (T, error) {
	var value T
	err := tx.GetContext(ctx, &value, query, args...)
	return value, err
}

func validateProfile(username, email string) error {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if len(username) < 3 || len(username) > 40 || !strings.Contains(email, "@") || len(email) > 254 {
		return fmt.Errorf("%w: invalid username or email", ErrInvalid)
	}
	return nil
}
