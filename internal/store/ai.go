package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

const (
	AIQuotaInherit   = "INHERIT"
	AIQuotaLimited   = "LIMITED"
	AIQuotaUnlimited = "UNLIMITED"
	AIQuotaBlocked   = "BLOCKED"
)

type AIStartInput struct {
	ConversationID  string
	ClientRequestID string
	Message         string
	Timetable       string
	SourceProjectID string
}

type AIStartResult struct {
	Conversation model.AIConversation
	RequestID    string
	Config       model.AIConfig
	Usage        model.AIUsage
	Replay       *model.AIMessage
}

type AIAdminUsage struct {
	UserID       string `db:"user_id" json:"userId"`
	Username     string `db:"username" json:"username"`
	Email        string `db:"email" json:"email"`
	Period       string `db:"period" json:"period"`
	Used         int    `db:"used" json:"used"`
	Reserved     int    `db:"reserved" json:"reserved"`
	Mode         string `db:"mode" json:"mode"`
	MonthlyLimit int    `db:"monthly_limit" json:"monthlyLimit"`
}

func (s *Store) AIConfig(ctx context.Context) (model.AIConfig, error) {
	var item model.AIConfig
	err := s.db.GetContext(ctx, &item, `SELECT enabled, provider_kind, base_url, model, secret_ref, system_prompt, timetable_prompt, temperature, max_output_tokens, timeout_seconds, max_history_messages, default_monthly_limit, quota_timezone, version, updated_by, updated_at FROM ai_config WHERE id = 1`)
	return item, normalizeDBError(err)
}

func (s *Store) UpdateAIConfig(ctx context.Context, actorID string, item model.AIConfig) (model.AIConfig, error) {
	now := nowMillis()
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE ai_config SET enabled=?, provider_kind=?, base_url=?, model=?, secret_ref=?, system_prompt=?, timetable_prompt=?, temperature=?, max_output_tokens=?, timeout_seconds=?, max_history_messages=?, default_monthly_limit=?, quota_timezone=?, version=version+1, updated_by=?, updated_at=? WHERE id=1`), item.Enabled, item.ProviderKind, item.BaseURL, item.Model, item.SecretRef, item.SystemPrompt, item.TimetablePrompt, item.Temperature, item.MaxOutputTokens, item.TimeoutSeconds, item.MaxHistoryMessages, item.DefaultMonthlyLimit, item.QuotaTimezone, actorID, now)
	if err != nil {
		return model.AIConfig{}, err
	}
	return s.AIConfig(ctx)
}

func aiPeriod(now time.Time, timezone string) (string, int64) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	local := now.In(location)
	next := time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, location)
	return local.Format("2006-01"), next.UnixMilli()
}

func quotaLimit(mode string, override, fallback int) int {
	switch mode {
	case AIQuotaUnlimited:
		return -1
	case AIQuotaBlocked:
		return 0
	case AIQuotaLimited:
		return override
	default:
		return fallback
	}
}

func (s *Store) aiUsageTx(ctx context.Context, tx *sqlx.Tx, userID string, config model.AIConfig) (model.AIUsage, error) {
	period, resetAt := aiPeriod(time.Now(), config.QuotaTimezone)
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_usage_monthly (user_id, period, used, reserved, updated_at) VALUES (?, ?, 0, 0, ?) ON CONFLICT(user_id, period) DO NOTHING`), userID, period, nowMillis()); err != nil {
		return model.AIUsage{}, err
	}
	var record struct {
		Used     int `db:"used"`
		Reserved int `db:"reserved"`
	}
	if err := tx.GetContext(ctx, &record, s.rebind(`SELECT used, reserved FROM ai_usage_monthly WHERE user_id=? AND period=?`), userID, period); err != nil {
		return model.AIUsage{}, err
	}
	var override struct {
		Mode  string `db:"mode"`
		Limit int    `db:"monthly_limit"`
	}
	err := tx.GetContext(ctx, &override, s.rebind(`SELECT mode, monthly_limit FROM ai_user_quotas WHERE user_id=?`), userID)
	if err == sql.ErrNoRows {
		override.Mode = AIQuotaInherit
		err = nil
	}
	if err != nil {
		return model.AIUsage{}, err
	}
	return model.AIUsage{Period: period, Limit: quotaLimit(override.Mode, override.Limit, config.DefaultMonthlyLimit), Used: record.Used, Reserved: record.Reserved, Mode: override.Mode, ResetAt: resetAt}, nil
}

func (s *Store) AIUsage(ctx context.Context, userID string) (model.AIUsage, error) {
	config, err := s.AIConfig(ctx)
	if err != nil {
		return model.AIUsage{}, err
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.AIUsage{}, err
	}
	defer tx.Rollback()
	usage, err := s.aiUsageTx(ctx, tx, userID, config)
	if err != nil {
		return model.AIUsage{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.AIUsage{}, err
	}
	return usage, nil
}

func (s *Store) SetAIQuota(ctx context.Context, actorID string, userIDs []string, mode string, limit int) error {
	if len(userIDs) == 0 || len(userIDs) > 500 || !map[string]bool{AIQuotaInherit: true, AIQuotaLimited: true, AIQuotaUnlimited: true, AIQuotaBlocked: true}[mode] || limit < 0 {
		return ErrInvalid
	}
	if mode == AIQuotaLimited && limit < 1 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, userID := range userIDs {
		if strings.TrimSpace(userID) == "" {
			return ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_user_quotas (user_id, mode, monthly_limit, updated_by, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(user_id) DO UPDATE SET mode=excluded.mode, monthly_limit=excluded.monthly_limit, updated_by=excluded.updated_by, updated_at=excluded.updated_at`), userID, mode, limit, actorID, nowMillis()); err != nil {
			return normalizeDBError(err)
		}
	}
	return tx.Commit()
}

func (s *Store) StartAIRequest(ctx context.Context, userID string, input AIStartInput) (AIStartResult, error) {
	if strings.TrimSpace(input.ClientRequestID) == "" || strings.TrimSpace(input.Message) == "" {
		return AIStartResult{}, ErrInvalid
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return AIStartResult{}, err
	}
	defer tx.Rollback()
	var config model.AIConfig
	if err := tx.GetContext(ctx, &config, `SELECT enabled, provider_kind, base_url, model, secret_ref, system_prompt, timetable_prompt, temperature, max_output_tokens, timeout_seconds, max_history_messages, default_monthly_limit, quota_timezone, version, updated_by, updated_at FROM ai_config WHERE id=1`); err != nil {
		return AIStartResult{}, err
	}
	usage, err := s.aiUsageTx(ctx, tx, userID, config)
	if err != nil {
		return AIStartResult{}, err
	}
	var replayRequest struct {
		ConversationID string `db:"conversation_id"`
		Status         string `db:"status"`
		QuotaCounted   int    `db:"quota_counted"`
	}
	err = tx.GetContext(ctx, &replayRequest, s.rebind(`SELECT conversation_id, status, quota_counted FROM ai_requests WHERE user_id=? AND client_request_id=?`), userID, input.ClientRequestID)
	if err == nil {
		var message model.AIMessage
		if replayRequest.Status == "COMPLETE" {
			_ = tx.GetContext(ctx, &message, s.rebind(`SELECT * FROM ai_messages WHERE conversation_id=? AND role='ASSISTANT' ORDER BY created_at DESC LIMIT 1`), replayRequest.ConversationID)
		}
		var conversation model.AIConversation
		_ = tx.GetContext(ctx, &conversation, s.rebind(`SELECT * FROM ai_conversations WHERE id=?`), replayRequest.ConversationID)
		_ = tx.Commit()
		return AIStartResult{Conversation: conversation, Config: config, Usage: usage, Replay: &message}, nil
	}
	if err != sql.ErrNoRows {
		return AIStartResult{}, err
	}
	if config.Enabled == 0 || config.DefaultMonthlyLimit < 0 {
		return AIStartResult{}, ErrForbidden
	}
	if usage.Limit == 0 || (usage.Limit > 0 && usage.Used+usage.Reserved >= usage.Limit) {
		return AIStartResult{}, ErrUnavailable
	}
	if input.ConversationID == "" {
		if strings.TrimSpace(input.Timetable) == "" {
			return AIStartResult{}, ErrInvalid
		}
		hash := sha256.Sum256([]byte(input.Timetable))
		conversation := model.AIConversation{ID: ids.New("aic"), UserID: userID, Title: truncateTitle(input.Message), Timetable: input.Timetable, TimetableHash: hex.EncodeToString(hash[:]), SourceProjectID: input.SourceProjectID, CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_conversations (id,user_id,title,timetable_snapshot,timetable_hash,source_project_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`), conversation.ID, conversation.UserID, conversation.Title, conversation.Timetable, conversation.TimetableHash, conversation.SourceProjectID, conversation.CreatedAt, conversation.UpdatedAt); err != nil {
			return AIStartResult{}, err
		}
		input.ConversationID = conversation.ID
	} else if strings.TrimSpace(input.Timetable) != "" {
		return AIStartResult{}, ErrInvalid
	}
	var conversation model.AIConversation
	if err := tx.GetContext(ctx, &conversation, s.rebind(`SELECT * FROM ai_conversations WHERE id=? AND user_id=?`), input.ConversationID, userID); err != nil {
		return AIStartResult{}, normalizeDBError(err)
	}
	requestID := ids.New("air")
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_messages (id,conversation_id,role,content,status,client_request_id,created_at,completed_at) VALUES (?,?,'USER',?,'PENDING',?,?,?,0)`), ids.New("aim"), conversation.ID, input.Message, input.ClientRequestID, now); err != nil {
		return AIStartResult{}, normalizeDBError(err)
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_requests (id,user_id,conversation_id,client_request_id,provider_kind,model,status,created_at) VALUES (?,?,?,?,?,?, 'PENDING', ?)`), requestID, userID, conversation.ID, input.ClientRequestID, config.ProviderKind, config.Model, now); err != nil {
		return AIStartResult{}, normalizeDBError(err)
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_usage_monthly SET reserved=reserved+1, updated_at=? WHERE user_id=? AND period=?`), now, userID, usage.Period); err != nil {
		return AIStartResult{}, err
	}
	usage.Reserved++
	if err := tx.Commit(); err != nil {
		return AIStartResult{}, err
	}
	return AIStartResult{Conversation: conversation, RequestID: requestID, Config: config, Usage: usage}, nil
}

func truncateTitle(value string) string {
	value = strings.TrimSpace(value)
	r := []rune(value)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return value
}

func (s *Store) AIHistory(ctx context.Context, userID, conversationID string, max int) ([]model.AIMessage, model.AIConversation, error) {
	var conversation model.AIConversation
	if err := s.db.GetContext(ctx, &conversation, s.rebind(`SELECT * FROM ai_conversations WHERE id=? AND user_id=?`), conversationID, userID); err != nil {
		return nil, conversation, normalizeDBError(err)
	}
	items := []model.AIMessage{}
	if err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM ai_messages WHERE conversation_id=? AND status='COMPLETE' ORDER BY created_at DESC LIMIT ?`), conversationID, max); err != nil {
		return nil, conversation, err
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, conversation, nil
}

func (s *Store) CommitAIQuota(ctx context.Context, requestID string) (model.AIUsage, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.AIUsage{}, err
	}
	defer tx.Rollback()
	var req struct {
		UserID  string `db:"user_id"`
		Status  string `db:"status"`
		Counted int    `db:"quota_counted"`
	}
	if err := tx.GetContext(ctx, &req, s.rebind(`SELECT user_id,status,quota_counted FROM ai_requests WHERE id=?`), requestID); err != nil {
		return model.AIUsage{}, normalizeDBError(err)
	}
	config := model.AIConfig{}
	if err := tx.GetContext(ctx, &config, `SELECT enabled, provider_kind, base_url, model, secret_ref, system_prompt, timetable_prompt, temperature, max_output_tokens, timeout_seconds, max_history_messages, default_monthly_limit, quota_timezone, version, updated_by, updated_at FROM ai_config WHERE id=1`); err != nil {
		return model.AIUsage{}, err
	}
	usage, err := s.aiUsageTx(ctx, tx, req.UserID, config)
	if err != nil {
		return model.AIUsage{}, err
	}
	if req.Counted == 0 {
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_usage_monthly SET reserved=CASE WHEN reserved>0 THEN reserved-1 ELSE 0 END, used=used+1, updated_at=? WHERE user_id=? AND period=?`), nowMillis(), req.UserID, usage.Period); err != nil {
			return model.AIUsage{}, err
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_requests SET quota_counted=1 WHERE id=?`), requestID); err != nil {
			return model.AIUsage{}, err
		}
		usage.Reserved = max(0, usage.Reserved-1)
		usage.Used++
	}
	if err := tx.Commit(); err != nil {
		return model.AIUsage{}, err
	}
	return usage, nil
}

func (s *Store) FinishAIRequest(ctx context.Context, requestID, response, status, errorCode string, latency int64) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var req struct {
		UserID         string `db:"user_id"`
		ConversationID string `db:"conversation_id"`
		Counted        int    `db:"quota_counted"`
	}
	if err := tx.GetContext(ctx, &req, s.rebind(`SELECT user_id,conversation_id,quota_counted FROM ai_requests WHERE id=?`), requestID); err != nil {
		return normalizeDBError(err)
	}
	if req.Counted == 0 {
		config := model.AIConfig{}
		if err := tx.GetContext(ctx, &config, `SELECT enabled, provider_kind, base_url, model, secret_ref, system_prompt, timetable_prompt, temperature, max_output_tokens, timeout_seconds, max_history_messages, default_monthly_limit, quota_timezone, version, updated_by, updated_at FROM ai_config WHERE id=1`); err != nil {
			return err
		}
		usage, err := s.aiUsageTx(ctx, tx, req.UserID, config)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_usage_monthly SET reserved=CASE WHEN reserved>0 THEN reserved-1 ELSE 0 END, updated_at=? WHERE user_id=? AND period=?`), nowMillis(), req.UserID, usage.Period); err != nil {
			return err
		}
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_requests SET status=?, error_code=?, latency_ms=?, completed_at=? WHERE id=?`), status, errorCode, latency, now, requestID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_messages SET status=? WHERE conversation_id=? AND role='USER' AND status='PENDING'`), status, req.ConversationID); err != nil {
		return err
	}
	if response != "" {
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_messages (id,conversation_id,role,content,status,created_at,completed_at) VALUES (?,?,'ASSISTANT',?,'COMPLETE',?,?,?)`), ids.New("aim"), req.ConversationID, response, now, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.rebind(`UPDATE ai_conversations SET updated_at=? WHERE id=?`), now, req.ConversationID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListAIConversations(ctx context.Context, userID string, limit, offset int) ([]model.AIConversation, int, error) {
	var total int
	if err := s.db.GetContext(ctx, &total, s.rebind(`SELECT COUNT(*) FROM ai_conversations WHERE user_id=?`), userID); err != nil {
		return nil, 0, err
	}
	items := []model.AIConversation{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM ai_conversations WHERE user_id=? ORDER BY updated_at DESC LIMIT ? OFFSET ?`), userID, limit, offset)
	return items, total, err
}
func (s *Store) DeleteAIConversation(ctx context.Context, userID, id string) error {
	r, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM ai_conversations WHERE id=? AND user_id=?`), id, userID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListAIUsageAdmin(ctx context.Context, limit, offset int) ([]AIAdminUsage, int, error) {
	config, err := s.AIConfig(ctx)
	if err != nil {
		return nil, 0, err
	}
	period, _ := aiPeriod(time.Now(), config.QuotaTimezone)
	var total int
	if err := s.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM users`); err != nil {
		return nil, 0, err
	}
	items := []AIAdminUsage{}
	err = s.db.SelectContext(ctx, &items, s.rebind(`SELECT u.id AS user_id, u.username, u.email, COALESCE(usage.period, ?) AS period, COALESCE(usage.used, 0) AS used, COALESCE(usage.reserved, 0) AS reserved, COALESCE(quota.mode, 'INHERIT') AS mode, COALESCE(quota.monthly_limit, 0) AS monthly_limit FROM users u LEFT JOIN ai_usage_monthly usage ON usage.user_id=u.id AND usage.period=? LEFT JOIN ai_user_quotas quota ON quota.user_id=u.id ORDER BY usage.used DESC, u.created_at DESC LIMIT ? OFFSET ?`), period, period, limit, offset)
	return items, total, err
}
