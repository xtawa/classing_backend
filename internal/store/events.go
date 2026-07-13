package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/xtawa/classing-backend/internal/ids"
)

type RuntimeEvent struct {
	ID        string `db:"id"`
	UserID    string `db:"user_id"`
	EventType string `db:"event_type"`
	Payload   string `db:"payload"`
	CreatedAt int64  `db:"created_at"`
}

func (s *Store) runtimeEventInTx(ctx context.Context, tx *sqlx.Tx, userID, eventType string, payload any) error {
	now := nowMillis()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("evt_%013d_%s", now, ids.Token(6))
	_, err = tx.ExecContext(ctx, s.rebind(`INSERT INTO runtime_events (id, user_id, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?)`), id, userID, eventType, string(encoded), now)
	return err
}

func (s *Store) RuntimeEvents(ctx context.Context, userID, afterID string, limit int) ([]RuntimeEvent, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	items := []RuntimeEvent{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM runtime_events WHERE id > ? AND (user_id = '' OR user_id = ?) ORDER BY id ASC LIMIT ?`), afterID, userID, limit)
	return items, err
}
