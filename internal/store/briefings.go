package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

func (s *Store) Briefing(ctx context.Context, userID string) (model.BriefingSubscription, error) {
	var item model.BriefingSubscription
	err := s.db.GetContext(ctx, &item, s.rebind(`SELECT * FROM briefing_subscriptions WHERE user_id = ?`), userID)
	return item, normalizeDBError(err)
}

type DueSubscription struct {
	model.BriefingSubscription
	Email    string `db:"email"`
	Username string `db:"username"`
}

func (s *Store) ActiveBriefingSubscriptions(ctx context.Context) ([]DueSubscription, error) {
	items := []DueSubscription{}
	err := s.db.SelectContext(ctx, &items, `SELECT b.*, u.email, u.username FROM briefing_subscriptions b JOIN users u ON u.id = b.user_id WHERE b.enabled = 1 AND b.channel IN ('EMAIL', 'BOTH') AND u.status = 'ACTIVE'`)
	return items, err
}

func (s *Store) MarkBriefingScheduled(ctx context.Context, userID string, at int64) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE briefing_subscriptions SET last_scheduled_at = ?, updated_at = ? WHERE user_id = ?`), at, nowMillis(), userID)
	return err
}

type ClaimedJob struct {
	model.BriefingJob
	Email    string `db:"email"`
	Username string `db:"username"`
}

func (s *Store) ClaimBriefingJob(ctx context.Context) (ClaimedJob, error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return ClaimedJob{}, err
	}
	defer tx.Rollback()
	var item ClaimedJob
	claimQuery := `SELECT j.*, u.email, u.username FROM briefing_jobs j JOIN users u ON u.id = j.user_id WHERE j.status IN ('PENDING', 'RETRY') AND j.scheduled_at <= ? ORDER BY j.scheduled_at ASC LIMIT 1`
	if s.dialect == "pgx" {
		claimQuery += ` FOR UPDATE OF j SKIP LOCKED`
	}
	err = tx.GetContext(ctx, &item, s.rebind(claimQuery), nowMillis())
	if err != nil {
		return ClaimedJob{}, normalizeDBError(err)
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE briefing_jobs SET status = 'PROCESSING', updated_at = ? WHERE id = ? AND status IN ('PENDING', 'RETRY')`), nowMillis(), item.ID)
	if err != nil {
		return ClaimedJob{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ClaimedJob{}, ErrConflict
	}
	item.Status = "PROCESSING"
	return item, tx.Commit()
}

func (s *Store) AcquireMailbox(ctx context.Context, usageDate string) (model.Mailbox, error) {
	return s.AcquireMailboxExcluding(ctx, usageDate, nil)
}

func (s *Store) AcquireMailboxExcluding(ctx context.Context, usageDate string, excluded map[string]bool) (model.Mailbox, error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return model.Mailbox{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE mailboxes SET used_today = 0, usage_date = ?, updated_at = ? WHERE usage_date <> ?`), usageDate, nowMillis(), usageDate); err != nil {
		return model.Mailbox{}, err
	}
	items := []model.Mailbox{}
	mailboxQuery := `SELECT * FROM mailboxes WHERE enabled = 1 AND used_today < daily_quota ORDER BY used_today ASC, created_at ASC`
	if s.dialect == "pgx" {
		mailboxQuery += ` FOR UPDATE SKIP LOCKED`
	}
	if err := tx.SelectContext(ctx, &items, s.rebind(mailboxQuery)); err != nil {
		return model.Mailbox{}, err
	}
	var item model.Mailbox
	for _, candidate := range items {
		if !excluded[candidate.ID] {
			item = candidate
			break
		}
	}
	if item.ID == "" {
		return model.Mailbox{}, ErrNotFound
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE mailboxes SET used_today = used_today + 1, updated_at = ? WHERE id = ? AND used_today < daily_quota`), nowMillis(), item.ID)
	if err != nil {
		return model.Mailbox{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.Mailbox{}, ErrConflict
	}
	item.UsedToday++
	return item, tx.Commit()
}

func (s *Store) ReleaseMailboxReservation(ctx context.Context, mailboxID, usageDate string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE mailboxes SET used_today = CASE WHEN used_today > 0 THEN used_today - 1 ELSE 0 END, updated_at = ? WHERE id = ? AND usage_date = ?`), nowMillis(), mailboxID, usageDate)
	return err
}

func (s *Store) CompleteBriefingJob(ctx context.Context, jobID, mailboxID string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE briefing_jobs SET status = 'SENT', provider_mailbox_id = ?, payload = '', updated_at = ? WHERE id = ?`), mailboxID, now, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailBriefingJob(ctx context.Context, jobID, message string, retryCount int) error {
	status := "RETRY"
	delay := time.Duration(retryCount+1) * 5 * time.Minute
	if retryCount >= 4 {
		status = "FAILED"
		delay = 0
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE briefing_jobs SET status = ?, retry_count = retry_count + 1, last_error = ?, scheduled_at = ?, updated_at = ? WHERE id = ?`), status, prefixError(message, 500), time.Now().Add(delay).UnixMilli(), nowMillis(), jobID)
	return err
}

func prefixError(message string, max int) string {
	message = strings.TrimSpace(message)
	if len(message) > max {
		return message[:max]
	}
	return message
}

func (s *Store) UpsertBriefing(ctx context.Context, userID string, enabled bool, channel, deliveryTime, timezone string) (model.BriefingSubscription, error) {
	channel = strings.ToUpper(strings.TrimSpace(channel))
	if channel != "APP_NOTIFICATION" && channel != "EMAIL" && channel != "BOTH" {
		return model.BriefingSubscription{}, ErrInvalid
	}
	if len(deliveryTime) != 5 || deliveryTime[2] != ':' {
		return model.BriefingSubscription{}, ErrInvalid
	}
	if _, err := time.Parse("15:04", deliveryTime); err != nil {
		return model.BriefingSubscription{}, ErrInvalid
	}
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return model.BriefingSubscription{}, ErrInvalid
	}
	value := 0
	if enabled {
		value = 1
	}
	now := nowMillis()
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO briefing_subscriptions (user_id, enabled, channel, delivery_time, timezone, last_scheduled_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, ?) ON CONFLICT(user_id) DO UPDATE SET enabled = excluded.enabled, channel = excluded.channel, delivery_time = excluded.delivery_time, timezone = excluded.timezone, updated_at = excluded.updated_at`), userID, value, channel, deliveryTime, timezone, now)
	if err != nil {
		return model.BriefingSubscription{}, err
	}
	if !enabled {
		if _, err := s.db.ExecContext(ctx, s.rebind(`UPDATE briefing_jobs SET status = 'CANCELLED', payload = '', last_error = '', updated_at = ? WHERE user_id = ? AND status IN ('PENDING', 'RETRY') AND channel IN ('EMAIL', 'EMAIL_TEST')`), now, userID); err != nil {
			return model.BriefingSubscription{}, err
		}
	}
	return s.Briefing(ctx, userID)
}

func (s *Store) DeleteBriefing(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM briefing_subscriptions WHERE user_id = ?`), userID)
	return err
}

func (s *Store) QueueBriefingJob(ctx context.Context, userID, targetDate, channel string, scheduledAt int64) (model.BriefingJob, error) {
	now := nowMillis()
	item := model.BriefingJob{ID: ids.New("job"), UserID: userID, TargetDate: targetDate, Channel: channel, Status: "PENDING", ScheduledAt: scheduledAt, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO briefing_jobs (id, user_id, target_date, channel, status, scheduled_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'PENDING', ?, ?, ?)`), item.ID, userID, targetDate, channel, scheduledAt, now, now)
	return item, normalizeDBError(err)
}

func (s *Store) ScheduleBriefingJob(ctx context.Context, userID, targetDate, channel string, scheduledAt int64) (model.BriefingJob, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.BriefingJob{}, err
	}
	defer tx.Rollback()
	now := nowMillis()
	item := model.BriefingJob{ID: ids.New("job"), UserID: userID, TargetDate: targetDate, Channel: channel, Status: "PENDING", ScheduledAt: scheduledAt, CreatedAt: now, UpdatedAt: now}
	if _, err = tx.ExecContext(ctx, s.rebind(`INSERT INTO briefing_jobs (id, user_id, target_date, channel, status, scheduled_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'PENDING', ?, ?, ?)`), item.ID, userID, targetDate, channel, scheduledAt, now, now); err != nil {
		return model.BriefingJob{}, normalizeDBError(err)
	}
	if _, err = tx.ExecContext(ctx, s.rebind(`UPDATE briefing_subscriptions SET last_scheduled_at = ?, updated_at = ? WHERE user_id = ? AND enabled = 1`), scheduledAt, now, userID); err != nil {
		return model.BriefingJob{}, err
	}
	return item, tx.Commit()
}

func (s *Store) QueuePasswordResetJob(ctx context.Context, userID, token string, expiresAt int64) (model.BriefingJob, error) {
	payload, err := json.Marshal(map[string]any{"token": token, "expiresAt": expiresAt})
	if err != nil {
		return model.BriefingJob{}, err
	}
	now := nowMillis()
	item := model.BriefingJob{
		ID:          ids.New("job"),
		UserID:      userID,
		Channel:     "PASSWORD_RESET",
		Status:      "PENDING",
		ScheduledAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
		Payload:     string(payload),
	}
	item.TargetDate = item.ID
	_, err = s.db.ExecContext(
		ctx,
		s.rebind(`INSERT INTO briefing_jobs (id, user_id, target_date, channel, status, scheduled_at, created_at, updated_at, payload) VALUES (?, ?, ?, ?, 'PENDING', ?, ?, ?, ?)`),
		item.ID,
		item.UserID,
		item.TargetDate,
		item.Channel,
		item.ScheduledAt,
		item.CreatedAt,
		item.UpdatedAt,
		item.Payload,
	)
	return item, normalizeDBError(err)
}

func (s *Store) QueueEmailVerificationJob(ctx context.Context, userID, code string, expiresAt int64) (model.BriefingJob, error) {
	payload, err := json.Marshal(map[string]any{"code": code, "expiresAt": expiresAt})
	if err != nil {
		return model.BriefingJob{}, err
	}
	now := nowMillis()
	item := model.BriefingJob{
		ID: ids.New("job"), UserID: userID, Channel: "EMAIL_VERIFICATION", Status: "PENDING",
		ScheduledAt: now, CreatedAt: now, UpdatedAt: now, Payload: string(payload),
	}
	item.TargetDate = item.ID
	_, err = s.db.ExecContext(ctx, s.rebind(`INSERT INTO briefing_jobs (id, user_id, target_date, channel, status, scheduled_at, created_at, updated_at, payload) VALUES (?, ?, ?, ?, 'PENDING', ?, ?, ?, ?)`), item.ID, item.UserID, item.TargetDate, item.Channel, item.ScheduledAt, item.CreatedAt, item.UpdatedAt, item.Payload)
	return item, normalizeDBError(err)
}

func (s *Store) QueueEmailChangeVerificationJob(ctx context.Context, userID, code string, expiresAt int64, newEmail string) (model.BriefingJob, error) {
	payload, err := json.Marshal(map[string]any{"code": code, "expiresAt": expiresAt, "toEmail": newEmail})
	if err != nil {
		return model.BriefingJob{}, err
	}
	now := nowMillis()
	item := model.BriefingJob{
		ID: ids.New("job"), UserID: userID, Channel: "EMAIL_CHANGE_VERIFY", Status: "PENDING",
		ScheduledAt: now, CreatedAt: now, UpdatedAt: now, Payload: string(payload),
	}
	item.TargetDate = item.ID
	_, err = s.db.ExecContext(ctx, s.rebind(`INSERT INTO briefing_jobs (id, user_id, target_date, channel, status, scheduled_at, created_at, updated_at, payload) VALUES (?, ?, ?, ?, 'PENDING', ?, ?, ?, ?)`), item.ID, item.UserID, item.TargetDate, item.Channel, item.ScheduledAt, item.CreatedAt, item.UpdatedAt, item.Payload)
	return item, normalizeDBError(err)
}

func (s *Store) QueueEmailChangeNotifyJob(ctx context.Context, userID, newEmail, oldEmail string) (model.BriefingJob, error) {
	payload, err := json.Marshal(map[string]any{"newEmail": newEmail, "toEmail": oldEmail})
	if err != nil {
		return model.BriefingJob{}, err
	}
	now := nowMillis()
	item := model.BriefingJob{
		ID: ids.New("job"), UserID: userID, Channel: "EMAIL_CHANGE_NOTIFY", Status: "PENDING",
		ScheduledAt: now, CreatedAt: now, UpdatedAt: now, Payload: string(payload),
	}
	item.TargetDate = item.ID
	_, err = s.db.ExecContext(ctx, s.rebind(`INSERT INTO briefing_jobs (id, user_id, target_date, channel, status, scheduled_at, created_at, updated_at, payload) VALUES (?, ?, ?, ?, 'PENDING', ?, ?, ?, ?)`), item.ID, item.UserID, item.TargetDate, item.Channel, item.ScheduledAt, item.CreatedAt, item.UpdatedAt, item.Payload)
	return item, normalizeDBError(err)
}

func (s *Store) ListBriefingJobs(ctx context.Context, limit, offset int) ([]model.BriefingJob, int, error) {
	var total int
	if err := s.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM briefing_jobs`); err != nil {
		return nil, 0, err
	}
	items := []model.BriefingJob{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM briefing_jobs ORDER BY created_at DESC LIMIT ? OFFSET ?`), limit, offset)
	return items, total, err
}

func (s *Store) AddBriefingJobLog(ctx context.Context, jobID, level, event, message string, details map[string]any) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return ErrInvalid
	}
	level = strings.ToUpper(strings.TrimSpace(level))
	if level == "" {
		level = "INFO"
	}
	event = strings.TrimSpace(event)
	if event == "" {
		event = "event"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = event
	}
	payload := "{}"
	if len(details) > 0 {
		encoded, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("encode job log details: %w", err)
		}
		payload = prefixError(string(encoded), 4000)
	}
	_, err := s.db.ExecContext(
		ctx,
		s.rebind(`INSERT INTO briefing_job_logs (id, job_id, level, event, message, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		ids.New("jlg"),
		jobID,
		prefixError(level, 16),
		prefixError(event, 80),
		prefixError(message, 500),
		payload,
		nowMillis(),
	)
	return normalizeDBError(err)
}

func (s *Store) ListBriefingJobLogs(ctx context.Context, jobIDs []string, limitPerJob int) (map[string][]model.BriefingJobLog, error) {
	result := map[string][]model.BriefingJobLog{}
	if limitPerJob <= 0 {
		limitPerJob = 20
	}
	seen := map[string]bool{}
	for _, jobID := range jobIDs {
		jobID = strings.TrimSpace(jobID)
		if jobID == "" || seen[jobID] {
			continue
		}
		seen[jobID] = true
		items := []model.BriefingJobLog{}
		if err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM briefing_job_logs WHERE job_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`), jobID, limitPerJob); err != nil {
			return nil, err
		}
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
		result[jobID] = items
	}
	return result, nil
}

func (s *Store) RetryBriefingJob(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE briefing_jobs SET status = 'PENDING', retry_count = retry_count + 1, last_error = '', updated_at = ? WHERE id = ?`), nowMillis(), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListMailboxes(ctx context.Context) ([]model.Mailbox, error) {
	items := []model.Mailbox{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM mailboxes ORDER BY created_at DESC`)
	return items, err
}

func (s *Store) MailReady(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM mailboxes WHERE enabled = 1 AND daily_quota > 0`); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) CreateMailbox(ctx context.Context, item model.Mailbox) (model.Mailbox, error) {
	if item.SMTPHost == "" || item.SMTPPort < 1 || item.DailyQuota < 1 || !strings.HasPrefix(item.PasswordSecretRef, "env:") {
		return model.Mailbox{}, ErrInvalid
	}
	item.ID = ids.New("mbx")
	item.CreatedAt = nowMillis()
	item.UpdatedAt = item.CreatedAt
	if item.Enabled != 0 {
		item.Enabled = 1
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO mailboxes (id, name, smtp_host, smtp_port, username, password_secret_ref, from_address, daily_quota, used_today, usage_date, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?)`), item.ID, item.Name, item.SMTPHost, item.SMTPPort, item.Username, item.PasswordSecretRef, item.FromAddress, item.DailyQuota, item.Enabled, item.CreatedAt, item.UpdatedAt)
	return item, normalizeDBError(err)
}

func (s *Store) UpdateMailbox(ctx context.Context, item model.Mailbox) (model.Mailbox, error) {
	if item.ID == "" || item.SMTPHost == "" || item.SMTPPort < 1 || item.DailyQuota < 1 || !strings.HasPrefix(item.PasswordSecretRef, "env:") {
		return model.Mailbox{}, ErrInvalid
	}
	if item.Enabled != 0 {
		item.Enabled = 1
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE mailboxes SET name = ?, smtp_host = ?, smtp_port = ?, username = ?, password_secret_ref = ?, from_address = ?, daily_quota = ?, enabled = ?, updated_at = ? WHERE id = ?`), item.Name, item.SMTPHost, item.SMTPPort, item.Username, item.PasswordSecretRef, item.FromAddress, item.DailyQuota, item.Enabled, nowMillis(), item.ID)
	if err != nil {
		return model.Mailbox{}, normalizeDBError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.Mailbox{}, ErrNotFound
	}
	var updated model.Mailbox
	err = s.db.GetContext(ctx, &updated, s.rebind(`SELECT * FROM mailboxes WHERE id = ?`), item.ID)
	return updated, normalizeDBError(err)
}

func (s *Store) DeleteMailbox(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM mailboxes WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}
