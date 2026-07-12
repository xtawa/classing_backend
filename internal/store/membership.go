package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

type RedeemCode struct {
	Code               string `db:"code" json:"code"`
	CodeType           string `db:"code_type" json:"codeType"`
	GrantDays          int    `db:"grant_days" json:"grantDays"`
	MaxRedemptions     int    `db:"max_redemptions" json:"maxRedemptions"`
	CurrentRedemptions int    `db:"current_redemptions" json:"currentRedemptions"`
	ExpiresAt          int64  `db:"expires_at" json:"expiresAt"`
	RevokedAt          int64  `db:"revoked_at" json:"revokedAt"`
	CreatedBy          string `db:"created_by" json:"createdBy"`
	CreatedAt          int64  `db:"created_at" json:"createdAt"`
}

func (s *Store) Membership(ctx context.Context, userID string) (model.Membership, error) {
	var membership model.Membership
	err := s.db.GetContext(ctx, &membership, s.rebind(`SELECT * FROM memberships WHERE user_id = ?`), userID)
	if err == sql.ErrNoRows {
		now := nowMillis()
		_, err = s.db.ExecContext(ctx, s.rebind(`INSERT INTO memberships (user_id, tier, expires_at, updated_at, source) VALUES (?, 'FREE', 0, ?, 'SYSTEM')`), userID, now)
		if err != nil {
			return model.Membership{}, normalizeDBError(err)
		}
		return model.Membership{UserID: userID, Tier: "FREE", UpdatedAt: now, Source: "SYSTEM"}, nil
	}
	return membership, normalizeDBError(err)
}

func (s *Store) CreateRedeemCodes(ctx context.Context, actorID, codeType string, count, grantDays, maxRedemptions int, expiresAt int64) ([]RedeemCode, error) {
	codeType = strings.ToUpper(strings.TrimSpace(codeType))
	if codeType != "UNIQUE" && codeType != "CAMPAIGN" {
		return nil, ErrInvalid
	}
	if count < 1 || count > 500 || grantDays < 1 || grantDays > 3650 {
		return nil, ErrInvalid
	}
	if codeType == "UNIQUE" {
		maxRedemptions = 1
	}
	if maxRedemptions < 1 || maxRedemptions > 100000 {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := make([]RedeemCode, 0, count)
	now := nowMillis()
	for len(result) < count {
		code := formatRedeemCode()
		item := RedeemCode{Code: code, CodeType: codeType, GrantDays: grantDays, MaxRedemptions: maxRedemptions, ExpiresAt: expiresAt, CreatedBy: actorID, CreatedAt: now}
		_, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO redeem_codes (code, code_type, grant_days, max_redemptions, current_redemptions, expires_at, revoked_at, created_by, created_at) VALUES (?, ?, ?, ?, 0, ?, 0, ?, ?)`), item.Code, item.CodeType, item.GrantDays, item.MaxRedemptions, item.ExpiresAt, actorID, now)
		if err != nil {
			if normalizeDBError(err) == ErrConflict {
				continue
			}
			return nil, err
		}
		result = append(result, item)
	}
	return result, tx.Commit()
}

func formatRedeemCode() string {
	raw := strings.ToUpper(strings.ReplaceAll(ids.Token(9), "-", "X"))
	if len(raw) < 12 {
		raw += "CLASSINGCODE"
	}
	return fmt.Sprintf("CLS-%s-%s-%s", raw[0:4], raw[4:8], raw[8:12])
}

func (s *Store) ListRedeemCodes(ctx context.Context, limit, offset int) ([]RedeemCode, int, error) {
	var total int
	if err := s.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM redeem_codes`); err != nil {
		return nil, 0, err
	}
	items := []RedeemCode{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM redeem_codes ORDER BY created_at DESC LIMIT ? OFFSET ?`), limit, offset)
	return items, total, err
}

func (s *Store) RevokeRedeemCode(ctx context.Context, code string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE redeem_codes SET revoked_at = ? WHERE code = ? AND revoked_at = 0`), nowMillis(), strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Redeem(ctx context.Context, userID, code string) (model.Membership, error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return model.Membership{}, err
	}
	defer tx.Rollback()
	var item RedeemCode
	if err := tx.GetContext(ctx, &item, s.rebind(`SELECT * FROM redeem_codes WHERE code = ?`), strings.ToUpper(strings.TrimSpace(code))); err != nil {
		return model.Membership{}, normalizeDBError(err)
	}
	now := nowMillis()
	if item.RevokedAt != 0 || (item.ExpiresAt != 0 && item.ExpiresAt <= now) {
		return model.Membership{}, ErrForbidden
	}
	if item.CurrentRedemptions >= item.MaxRedemptions {
		return model.Membership{}, ErrUnavailable
	}
	var existing int
	if err := tx.GetContext(ctx, &existing, s.rebind(`SELECT COUNT(*) FROM redeem_redemptions WHERE code = ? AND user_id = ?`), item.Code, userID); err != nil {
		return model.Membership{}, err
	}
	if existing > 0 {
		return model.Membership{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE redeem_codes SET current_redemptions = current_redemptions + 1 WHERE code = ? AND current_redemptions < max_redemptions`), item.Code)
	if err != nil {
		return model.Membership{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.Membership{}, ErrUnavailable
	}
	var old model.Membership
	if err := tx.GetContext(ctx, &old, s.rebind(`SELECT * FROM memberships WHERE user_id = ?`), userID); err != nil && err != sql.ErrNoRows {
		return model.Membership{}, err
	}
	base := now
	if old.ExpiresAt > base {
		base = old.ExpiresAt
	}
	newExpires := time.UnixMilli(base).Add(time.Duration(item.GrantDays) * 24 * time.Hour).UnixMilli()
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO memberships (user_id, tier, expires_at, updated_at, source) VALUES (?, 'REDEEMED', ?, ?, 'REDEEM_CODE') ON CONFLICT(user_id) DO UPDATE SET tier = 'REDEEMED', expires_at = excluded.expires_at, updated_at = excluded.updated_at, source = excluded.source`), userID, newExpires, now); err != nil {
		return model.Membership{}, err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO redeem_redemptions (id, code, user_id, grant_days, redeemed_at) VALUES (?, ?, ?, ?, ?)`), ids.New("red"), item.Code, userID, item.GrantDays, now); err != nil {
		return model.Membership{}, normalizeDBError(err)
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO membership_events (id, user_id, action, tier, old_expires_at, new_expires_at, source, actor_id, created_at) VALUES (?, ?, 'GRANT', 'REDEEMED', ?, ?, 'REDEEM_CODE', ?, ?)`), ids.New("mev"), userID, old.ExpiresAt, newExpires, userID, now); err != nil {
		return model.Membership{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Membership{}, err
	}
	return s.Membership(ctx, userID)
}

func (s *Store) SetMembership(ctx context.Context, actorID, userID, tier string, expiresAt int64, action string) (model.Membership, error) {
	tier = strings.ToUpper(strings.TrimSpace(tier))
	if tier == "" {
		tier = "FREE"
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.Membership{}, err
	}
	defer tx.Rollback()
	var old model.Membership
	if err := tx.GetContext(ctx, &old, s.rebind(`SELECT * FROM memberships WHERE user_id = ?`), userID); err != nil && err != sql.ErrNoRows {
		return model.Membership{}, err
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO memberships (user_id, tier, expires_at, updated_at, source) VALUES (?, ?, ?, ?, 'ADMIN') ON CONFLICT(user_id) DO UPDATE SET tier = excluded.tier, expires_at = excluded.expires_at, updated_at = excluded.updated_at, source = excluded.source`), userID, tier, expiresAt, now); err != nil {
		return model.Membership{}, err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO membership_events (id, user_id, action, tier, old_expires_at, new_expires_at, source, actor_id, created_at) VALUES (?, ?, ?, ?, ?, ?, 'ADMIN', ?, ?)`), ids.New("mev"), userID, action, tier, old.ExpiresAt, expiresAt, actorID, now); err != nil {
		return model.Membership{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Membership{}, err
	}
	return s.Membership(ctx, userID)
}
