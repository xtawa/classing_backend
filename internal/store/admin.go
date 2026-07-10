package store

import (
	"context"
	"database/sql"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

type DashboardStats struct {
	Users             int `json:"users"`
	ActiveMembers     int `json:"activeMembers"`
	TimetableProjects int `json:"timetableProjects"`
	PendingJobs       int `json:"pendingJobs"`
	CloudDocuments    int `json:"cloudDocuments"`
}

func (s *Store) Setting(ctx context.Context, key, fallback string) (string, error) {
	var value string
	err := s.db.GetContext(ctx, &value, s.rebind(`SELECT setting_value FROM system_settings WHERE setting_key = ?`), key)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	return value, err
}

func (s *Store) Dashboard(ctx context.Context) (DashboardStats, error) {
	now := nowMillis()
	var stats DashboardStats
	queries := []struct {
		target *int
		query  string
		args   []any
	}{
		{&stats.Users, `SELECT COUNT(*) FROM users`, nil},
		{&stats.ActiveMembers, s.rebind(`SELECT COUNT(*) FROM memberships WHERE expires_at > ?`), []any{now}},
		{&stats.TimetableProjects, `SELECT COUNT(*) FROM timetable_projects`, nil},
		{&stats.PendingJobs, `SELECT COUNT(*) FROM briefing_jobs WHERE status IN ('PENDING', 'RETRY')`, nil},
		{&stats.CloudDocuments, `SELECT COUNT(*) FROM cloud_documents`, nil},
	}
	for _, item := range queries {
		if err := s.db.GetContext(ctx, item.target, item.query, item.args...); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (s *Store) Audit(ctx context.Context, item model.AuditLog) error {
	if item.ID == "" {
		item.ID = ids.New("aud")
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = nowMillis()
	}
	if item.Metadata == "" {
		item.Metadata = "{}"
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO audit_logs (id, actor_id, action, target_type, target_id, request_id, ip_address, user_agent, metadata, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), item.ID, item.ActorID, item.Action, item.TargetType, item.TargetID, item.RequestID, item.IPAddress, item.UserAgent, item.Metadata, item.CreatedAt)
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]model.AuditLog, int, error) {
	var total int
	if err := s.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM audit_logs`); err != nil {
		return nil, 0, err
	}
	items := []model.AuditLog{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM audit_logs ORDER BY created_at DESC LIMIT ? OFFSET ?`), limit, offset)
	return items, total, err
}

func (s *Store) ListSettings(ctx context.Context) (map[string]string, error) {
	rows := []struct {
		Key   string `db:"setting_key"`
		Value string `db:"setting_value"`
	}{}
	if err := s.db.SelectContext(ctx, &rows, `SELECT setting_key, setting_value FROM system_settings ORDER BY setting_key`); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.Key] = row.Value
	}
	return result, nil
}

func (s *Store) SetSetting(ctx context.Context, actorID, key, value string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO system_settings (setting_key, setting_value, updated_by, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(setting_key) DO UPDATE SET setting_value = excluded.setting_value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`), key, value, actorID, nowMillis())
	return err
}
