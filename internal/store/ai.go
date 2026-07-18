package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/xtawa/classing-backend/internal/aicost"
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
	ConversationID       string
	ClientRequestID      string
	Message              string
	Timetable            string
	SourceProjectID      string
	Model                string
	EstimatedInputTokens int
}

type AIStartResult struct {
	Conversation model.AIConversation
	RequestID    string
	Config       model.AIConfig
	Usage        model.AIUsage
	Replay       *model.AIMessage
}

type AIAdminUsage struct {
	UserID         string `db:"user_id" json:"userId"`
	Username       string `db:"username" json:"username"`
	Email          string `db:"email" json:"email"`
	Period         string `db:"period" json:"period"`
	Used           int    `db:"used" json:"used"`
	Reserved       int    `db:"reserved" json:"reserved"`
	Mode           string `db:"mode" json:"mode"`
	MonthlyLimit   int    `db:"monthly_limit" json:"monthlyLimit"`
	EffectiveLimit int    `db:"effective_limit" json:"effectiveLimit"`
	CreditBalance  int    `db:"credit_balance" json:"creditBalance"`
	CreditFrozen   bool   `db:"credit_frozen" json:"creditFrozen"`
	IsMember       bool   `db:"is_member" json:"isMember"`
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
		Used          int `db:"used"`
		Reserved      int `db:"reserved"`
		CreditBalance int `db:"credit_balance"`
		IsMember      int `db:"is_member"`
	}
	if err := tx.GetContext(ctx, &record, s.rebind(`SELECT used, reserved, COALESCE((SELECT balance FROM ai_credit_wallets WHERE user_id=?), 0) AS credit_balance, CASE WHEN EXISTS(SELECT 1 FROM memberships WHERE user_id=? AND expires_at>?) THEN 1 ELSE 0 END AS is_member FROM ai_usage_monthly WHERE user_id=? AND period=?`), userID, userID, nowMillis(), userID, period); err != nil {
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
	isMember := record.IsMember != 0
	fallbackLimit := aicost.FreeMonthlyLimit
	creditAvailable := 0
	if isMember {
		fallbackLimit = config.DefaultMonthlyLimit
		creditAvailable = record.CreditBalance
	}
	return model.AIUsage{Period: period, Limit: quotaLimit(override.Mode, override.Limit, fallbackLimit), Used: record.Used, Reserved: record.Reserved, CreditBalance: record.CreditBalance, CreditAvailable: creditAvailable, CreditFrozen: !isMember && record.CreditBalance > 0, IsMember: isMember, Mode: override.Mode, ResetAt: resetAt}, nil
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

func (s *Store) GrantAICredits(ctx context.Context, actorID, userID string, points int, note string) (int, error) {
	userID = strings.TrimSpace(userID)
	note = strings.TrimSpace(note)
	if userID == "" || points < 1 || points > 100000000 || len([]rune(note)) > 240 {
		return 0, ErrInvalid
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.GetContext(ctx, &exists, s.rebind(`SELECT COUNT(*) FROM users WHERE id=?`), userID); err != nil {
		return 0, err
	}
	if exists != 1 {
		return 0, ErrNotFound
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_credit_wallets (user_id, balance, updated_at) VALUES (?, ?, ?) ON CONFLICT(user_id) DO UPDATE SET balance=ai_credit_wallets.balance+excluded.balance, updated_at=excluded.updated_at`), userID, points, now); err != nil {
		return 0, normalizeDBError(err)
	}
	var balance int
	if err := tx.GetContext(ctx, &balance, s.rebind(`SELECT balance FROM ai_credit_wallets WHERE user_id=?`), userID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_credit_transactions (id,user_id,points,balance_after,source,reference_id,note,actor_id,created_at) VALUES (?,?,?,?,?,?,?,?,?)`), ids.New("aict"), userID, points, balance, "ADMIN_GRANT", "", note, actorID, now); err != nil {
		return 0, normalizeDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return balance, nil
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
		RequestID      string `db:"id"`
		ConversationID string `db:"conversation_id"`
		Status         string `db:"status"`
		QuotaCounted   int    `db:"quota_counted"`
	}
	err = tx.GetContext(ctx, &replayRequest, s.rebind(`SELECT id, conversation_id, status, quota_counted FROM ai_requests WHERE user_id=? AND client_request_id=?`), userID, input.ClientRequestID)
	if err == nil {
		var message model.AIMessage
		if replayRequest.Status == "COMPLETE" {
			_ = tx.GetContext(ctx, &message, s.rebind(`SELECT * FROM ai_messages WHERE conversation_id=? AND role='ASSISTANT' AND client_request_id=? LIMIT 1`), replayRequest.ConversationID, replayRequest.RequestID)
		}
		var conversation model.AIConversation
		_ = tx.GetContext(ctx, &conversation, s.rebind(`SELECT * FROM ai_conversations WHERE id=?`), replayRequest.ConversationID)
		_ = tx.Commit()
		return AIStartResult{Conversation: conversation, Config: config, Usage: usage, Replay: &message}, nil
	}
	if err != sql.ErrNoRows {
		return AIStartResult{}, err
	}
	selectedModel, validModel := aicost.Resolve(input.Model)
	if config.Enabled == 0 || config.DefaultMonthlyLimit < 0 {
		return AIStartResult{}, ErrForbidden
	}
	if !validModel {
		return AIStartResult{}, ErrInvalid
	}
	config.Model = selectedModel.ID
	reservation := aicost.ReservationPoints(config.Model, input.EstimatedInputTokens, config.MaxOutputTokens)
	if usage.Mode == AIQuotaBlocked || (usage.Limit >= 0 && usage.Used+usage.Reserved+reservation > usage.Limit+usage.CreditAvailable) {
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
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_messages (id,conversation_id,role,content,status,client_request_id,created_at,completed_at) VALUES (?,?,'USER',?,'PENDING',?,?,0)`), ids.New("aim"), conversation.ID, input.Message, input.ClientRequestID, now); err != nil {
		return AIStartResult{}, normalizeDBError(err)
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_requests (id,user_id,conversation_id,client_request_id,provider_kind,model,status,reserved_points,created_at) VALUES (?,?,?,?,?,?, 'PENDING', ?, ?)`), requestID, userID, conversation.ID, input.ClientRequestID, config.ProviderKind, config.Model, reservation, now); err != nil {
		return AIStartResult{}, normalizeDBError(err)
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_usage_monthly SET reserved=reserved+?, updated_at=? WHERE user_id=? AND period=?`), reservation, now, userID, usage.Period); err != nil {
		return AIStartResult{}, err
	}
	usage.Reserved += reservation
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

func (s *Store) SettleAIQuota(ctx context.Context, requestID string, tokens aicost.TokenUsage) (model.AIUsage, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.AIUsage{}, err
	}
	defer tx.Rollback()
	var req struct {
		UserID         string `db:"user_id"`
		Model          string `db:"model"`
		Counted        int    `db:"quota_counted"`
		ReservedPoints int    `db:"reserved_points"`
	}
	if err := tx.GetContext(ctx, &req, s.rebind(`SELECT user_id,model,quota_counted,reserved_points FROM ai_requests WHERE id=?`), requestID); err != nil {
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
		points := aicost.Points(req.Model, tokens)
		monthlyPoints := points
		creditPoints := 0
		if usage.Limit >= 0 {
			monthlyPoints = min(points, max(0, usage.Limit-usage.Used))
			creditPoints = min(points-monthlyPoints, usage.CreditAvailable)
			monthlyPoints += points - monthlyPoints - creditPoints
		}
		now := nowMillis()
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_usage_monthly SET reserved=CASE WHEN reserved>=? THEN reserved-? ELSE 0 END, used=used+?, updated_at=? WHERE user_id=? AND period=?`), req.ReservedPoints, req.ReservedPoints, monthlyPoints, now, req.UserID, usage.Period); err != nil {
			return model.AIUsage{}, err
		}
		if creditPoints > 0 {
			result, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_credit_wallets SET balance=balance-?, updated_at=? WHERE user_id=? AND balance>=?`), creditPoints, now, req.UserID, creditPoints)
			if err != nil {
				return model.AIUsage{}, err
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return model.AIUsage{}, ErrUnavailable
			}
			balance := usage.CreditBalance - creditPoints
			if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_credit_transactions (id,user_id,points,balance_after,source,reference_id,note,actor_id,created_at) VALUES (?,?,?,?,?,?,?,?,?)`), ids.New("aict"), req.UserID, -creditPoints, balance, "AI_USAGE", requestID, "", "", now); err != nil {
				return model.AIUsage{}, err
			}
			usage.CreditBalance = balance
			usage.CreditAvailable = balance
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_requests SET quota_counted=1,input_tokens=?,cached_input_tokens=?,output_tokens=?,cost_points=? WHERE id=?`), tokens.InputTokens, tokens.CachedInputTokens, tokens.OutputTokens, points, requestID); err != nil {
			return model.AIUsage{}, err
		}
		usage.Reserved = max(0, usage.Reserved-req.ReservedPoints)
		usage.Used += monthlyPoints
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
		ReservedPoints int    `db:"reserved_points"`
	}
	if err := tx.GetContext(ctx, &req, s.rebind(`SELECT user_id,conversation_id,quota_counted,reserved_points FROM ai_requests WHERE id=?`), requestID); err != nil {
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
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE ai_usage_monthly SET reserved=CASE WHEN reserved>=? THEN reserved-? ELSE 0 END, updated_at=? WHERE user_id=? AND period=?`), req.ReservedPoints, req.ReservedPoints, nowMillis(), req.UserID, usage.Period); err != nil {
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
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ai_messages (id,conversation_id,role,content,status,client_request_id,created_at,completed_at) VALUES (?,?,'ASSISTANT',?,'COMPLETE',?,?,?)`), ids.New("aim"), req.ConversationID, response, requestID, now, now); err != nil {
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
	err = s.db.SelectContext(ctx, &items, s.rebind(`SELECT u.id AS user_id, u.username, u.email, COALESCE(usage.period, ?) AS period, COALESCE(usage.used, 0) AS used, COALESCE(usage.reserved, 0) AS reserved, COALESCE(quota.mode, 'INHERIT') AS mode, COALESCE(quota.monthly_limit, 0) AS monthly_limit, COALESCE(wallet.balance, 0) AS credit_balance, CASE WHEN COALESCE(membership.expires_at, 0)>? THEN 1 ELSE 0 END AS is_member FROM users u LEFT JOIN ai_usage_monthly usage ON usage.user_id=u.id AND usage.period=? LEFT JOIN ai_user_quotas quota ON quota.user_id=u.id LEFT JOIN ai_credit_wallets wallet ON wallet.user_id=u.id LEFT JOIN memberships membership ON membership.user_id=u.id ORDER BY usage.used DESC, u.created_at DESC LIMIT ? OFFSET ?`), period, nowMillis(), period, limit, offset)
	for index := range items {
		fallbackLimit := aicost.FreeMonthlyLimit
		if items[index].IsMember {
			fallbackLimit = config.DefaultMonthlyLimit
		}
		items[index].EffectiveLimit = quotaLimit(items[index].Mode, items[index].MonthlyLimit, fallbackLimit)
		items[index].CreditFrozen = !items[index].IsMember && items[index].CreditBalance > 0
	}
	return items, total, err
}
