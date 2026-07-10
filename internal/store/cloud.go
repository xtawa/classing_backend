package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type CloudDocument struct {
	UserID    string `db:"user_id"`
	Payload   string `db:"payload"`
	Version   int64  `db:"version"`
	UpdatedAt int64  `db:"updated_at"`
}

type IdempotencyRecord struct {
	KeyValue     string `db:"key_value"`
	UserID       string `db:"user_id"`
	RequestHash  string `db:"request_hash"`
	ResponseCode int    `db:"response_code"`
	ResponseBody string `db:"response_body"`
	ExpiresAt    int64  `db:"expires_at"`
}

func (s *Store) CloudDocument(ctx context.Context, userID string) (CloudDocument, error) {
	var item CloudDocument
	err := s.db.GetContext(ctx, &item, s.rebind(`SELECT * FROM cloud_documents WHERE user_id = ?`), userID)
	return item, normalizeDBError(err)
}

func (s *Store) PutCloudDocument(ctx context.Context, userID string, payload json.RawMessage, expectedVersion int64) (CloudDocument, error) {
	if !json.Valid(payload) {
		return CloudDocument{}, ErrInvalid
	}
	current, err := s.CloudDocument(ctx, userID)
	if err == ErrNotFound {
		if expectedVersion > 0 {
			return CloudDocument{}, ErrConflict
		}
		item := CloudDocument{UserID: userID, Payload: string(payload), Version: 1, UpdatedAt: nowMillis()}
		_, err = s.db.ExecContext(ctx, s.rebind(`INSERT INTO cloud_documents (user_id, payload, version, updated_at) VALUES (?, ?, 1, ?)`), userID, item.Payload, item.UpdatedAt)
		return item, normalizeDBError(err)
	}
	if err != nil {
		return CloudDocument{}, err
	}
	if expectedVersion > 0 && current.Version != expectedVersion {
		return CloudDocument{}, ErrConflict
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE cloud_documents SET payload = ?, version = version + 1, updated_at = ? WHERE user_id = ? AND version = ?`), string(payload), nowMillis(), userID, current.Version)
	if err != nil {
		return CloudDocument{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return CloudDocument{}, ErrConflict
	}
	return s.CloudDocument(ctx, userID)
}

func HashRequest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *Store) Idempotency(ctx context.Context, userID, key string) (IdempotencyRecord, error) {
	var item IdempotencyRecord
	err := s.db.GetContext(ctx, &item, s.rebind(`SELECT * FROM idempotency_keys WHERE user_id = ? AND key_value = ? AND expires_at > ?`), userID, key, nowMillis())
	return item, normalizeDBError(err)
}

func (s *Store) SaveIdempotency(ctx context.Context, item IdempotencyRecord) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO idempotency_keys (key_value, user_id, request_hash, response_code, response_body, expires_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(key_value, user_id) DO NOTHING`), item.KeyValue, item.UserID, item.RequestHash, item.ResponseCode, item.ResponseBody, item.ExpiresAt)
	return err
}
