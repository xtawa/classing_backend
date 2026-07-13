package worker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/model"
	"github.com/xtawa/classing-backend/internal/store"
)

type Worker struct {
	store *store.Store
	log   *slog.Logger
}

func New(data *store.Store, logger *slog.Logger) *Worker { return &Worker{store: data, log: logger} }

func (w *Worker) Run(ctx context.Context) {
	scheduleTicker := time.NewTicker(time.Minute)
	deliveryTicker := time.NewTicker(10 * time.Second)
	defer scheduleTicker.Stop()
	defer deliveryTicker.Stop()
	w.schedule(ctx)
	if err := w.store.CleanupExpiredSecurityData(ctx); err != nil {
		w.log.Warn("cleanup expired security data", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-scheduleTicker.C:
			w.schedule(ctx)
			if err := w.store.CleanupExpiredSecurityData(ctx); err != nil {
				w.log.Warn("cleanup expired security data", "error", err)
			}
		case <-deliveryTicker.C:
			w.deliverOne(ctx)
		}
	}
}

func (w *Worker) schedule(ctx context.Context) {
	items, err := w.store.ActiveBriefingSubscriptions(ctx)
	if err != nil {
		w.log.Error("list briefing subscriptions", "error", err)
		return
	}
	now := time.Now()
	for _, item := range items {
		location, err := time.LoadLocation(item.Timezone)
		if err != nil {
			w.log.Warn("invalid briefing timezone", "user_id", item.UserID, "timezone", item.Timezone)
			continue
		}
		local := now.In(location)
		parts := strings.Split(item.Time, ":")
		if len(parts) != 2 {
			continue
		}
		hour, _ := strconv.Atoi(parts[0])
		minute, _ := strconv.Atoi(parts[1])
		deliveryMinute := hour*60 + minute
		if local.Hour()*60+local.Minute() < deliveryMinute {
			continue
		}
		if item.LastScheduledAt > 0 && time.UnixMilli(item.LastScheduledAt).In(location).Format("2006-01-02") == local.Format("2006-01-02") {
			continue
		}
		_, err = w.store.ScheduleBriefingJob(ctx, item.UserID, local.Format("2006-01-02"), "EMAIL", now.UnixMilli())
		if err != nil && err != store.ErrConflict {
			w.log.Error("queue briefing", "user_id", item.UserID, "error", err)
			continue
		}
	}
}

func (w *Worker) deliverOne(ctx context.Context) {
	job, err := w.store.ClaimBriefingJob(ctx)
	if err == store.ErrNotFound || err == store.ErrConflict {
		return
	}
	if err != nil {
		w.log.Error("claim briefing job", "error", err)
		return
	}
	w.jobLog(ctx, job.ID, "INFO", "job.claimed", "Delivery worker claimed the task", map[string]any{
		"channel":     job.Channel,
		"targetDate":  job.TargetDate,
		"retryCount":  job.RetryCount,
		"scheduledAt": job.ScheduledAt,
	})
	usageDate := time.Now().Format("2006-01-02")
	excluded := map[string]bool{}
	var deliveredMailbox model.Mailbox
	for {
		mailbox, acquireErr := w.store.AcquireMailboxExcluding(ctx, usageDate, excluded)
		if acquireErr != nil {
			w.jobLog(ctx, job.ID, "ERROR", "mailbox.pool_exhausted", "No enabled mailbox with quota is available", map[string]any{"error": acquireErr.Error(), "usageDate": usageDate})
			_ = w.store.FailBriefingJob(ctx, job.ID, "mailbox pool exhausted", job.RetryCount)
			return
		}
		w.jobLog(ctx, job.ID, "INFO", "mailbox.selected", "SMTP mailbox selected", map[string]any{
			"mailboxId": mailbox.ID,
			"name":      mailbox.Name,
			"host":      mailbox.SMTPHost,
			"port":      mailbox.SMTPPort,
			"from":      mailbox.FromAddress,
			"username":  mailbox.Username,
			"quota":     fmt.Sprintf("%d/%d", mailbox.UsedToday, mailbox.DailyQuota),
		})
		deliveryErr := send(mailbox, job, func(level, event, message string, details map[string]any) {
			w.jobLog(ctx, job.ID, level, event, message, details)
		})
		if deliveryErr == nil {
			deliveredMailbox = mailbox
			break
		}
		classified := classifyMailError(deliveryErr)
		w.log.Warn("send briefing", "job_id", job.ID, "mailbox_id", mailbox.ID, "error_class", classified, "error", deliveryErr)
		w.jobLog(ctx, job.ID, "ERROR", "mailbox.delivery_failed", "SMTP delivery attempt failed", map[string]any{
			"mailboxId":  mailbox.ID,
			"errorClass": classified,
			"error":      deliveryErr.Error(),
		})
		_ = w.store.ReleaseMailboxReservation(ctx, mailbox.ID, usageDate)
		excluded[mailbox.ID] = true
		if classified != "smtp connection failed" && classified != "smtp authentication failed" {
			_ = w.store.FailBriefingJob(ctx, job.ID, classified, job.RetryCount)
			return
		}
	}
	if err := w.store.CompleteBriefingJob(ctx, job.ID, deliveredMailbox.ID); err != nil {
		w.log.Error("complete briefing job", "job_id", job.ID, "error", err)
		w.jobLog(ctx, job.ID, "ERROR", "job.complete_failed", "Task was delivered but could not be marked as sent", map[string]any{"error": err.Error(), "mailboxId": deliveredMailbox.ID})
		return
	}
	w.jobLog(ctx, job.ID, "INFO", "job.sent", "Task delivered and marked as sent", map[string]any{"mailboxId": deliveredMailbox.ID})
}

func (w *Worker) jobLog(ctx context.Context, jobID, level, event, message string, details map[string]any) {
	if err := w.store.AddBriefingJobLog(ctx, jobID, level, event, message, details); err != nil {
		w.log.Warn("write briefing job log", "job_id", jobID, "event", event, "error", err)
	}
}

func classifyMailError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "auth") || strings.Contains(message, "password") || strings.Contains(message, "secret"):
		return "smtp authentication failed"
	case strings.Contains(message, "timeout") || strings.Contains(message, "connection") || strings.Contains(message, "dial"):
		return "smtp connection failed"
	case strings.Contains(message, "recipient") || strings.Contains(message, "rcpt"):
		return "smtp recipient rejected"
	default:
		return "smtp delivery failed"
	}
}

type mailLogFunc func(level, event, message string, details map[string]any)

func send(mailbox model.Mailbox, job store.ClaimedJob, logStep mailLogFunc) error {
	secretName := strings.TrimPrefix(mailbox.PasswordSecretRef, "env:")
	if secretName == mailbox.PasswordSecretRef || secretName == "" {
		emitMailLog(logStep, "ERROR", "smtp.secret_invalid", "Mailbox password secret reference is invalid", map[string]any{"secretRef": mailbox.PasswordSecretRef})
		return fmt.Errorf("unsupported mailbox secret reference")
	}
	password := os.Getenv(secretName)
	if password == "" {
		emitMailLog(logStep, "ERROR", "smtp.secret_empty", "SMTP password environment variable is empty", map[string]any{"secretName": secretName})
		return fmt.Errorf("mailbox secret %s is empty", secretName)
	}
	address := net.JoinHostPort(mailbox.SMTPHost, strconv.Itoa(mailbox.SMTPPort))
	mode := "starttls_if_available"
	if mailbox.SMTPPort == 465 {
		mode = "implicit_tls"
	}
	emitMailLog(logStep, "INFO", "smtp.connect_start", "Connecting to SMTP server", map[string]any{"host": mailbox.SMTPHost, "port": mailbox.SMTPPort, "mode": mode, "secretName": secretName})
	var client *smtp.Client
	var err error
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if mailbox.SMTPPort == 465 {
		connection, dialErr := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: mailbox.SMTPHost, MinVersion: tls.VersionTLS12})
		if dialErr != nil {
			emitMailLog(logStep, "ERROR", "smtp.connect_failed", "Implicit TLS SMTP connection failed", map[string]any{"error": dialErr.Error(), "host": mailbox.SMTPHost, "port": mailbox.SMTPPort})
			return fmt.Errorf("smtp connect implicit tls: %w", dialErr)
		}
		client, err = smtp.NewClient(connection, mailbox.SMTPHost)
	} else {
		connection, dialErr := dialer.Dial("tcp", address)
		if dialErr != nil {
			emitMailLog(logStep, "ERROR", "smtp.connect_failed", "SMTP TCP connection failed", map[string]any{"error": dialErr.Error(), "host": mailbox.SMTPHost, "port": mailbox.SMTPPort})
			return fmt.Errorf("smtp connect tcp: %w", dialErr)
		}
		client, err = smtp.NewClient(connection, mailbox.SMTPHost)
		if err == nil {
			if ok, _ := client.Extension("STARTTLS"); ok {
				emitMailLog(logStep, "INFO", "smtp.starttls_start", "SMTP server supports STARTTLS; upgrading connection", map[string]any{"host": mailbox.SMTPHost, "port": mailbox.SMTPPort})
				err = client.StartTLS(&tls.Config{ServerName: mailbox.SMTPHost, MinVersion: tls.VersionTLS12})
				if err == nil {
					emitMailLog(logStep, "INFO", "smtp.starttls_ok", "STARTTLS negotiation completed", map[string]any{"host": mailbox.SMTPHost, "port": mailbox.SMTPPort})
				}
			} else {
				emitMailLog(logStep, "WARN", "smtp.starttls_unavailable", "SMTP server did not advertise STARTTLS", map[string]any{"host": mailbox.SMTPHost, "port": mailbox.SMTPPort})
			}
		}
	}
	if err != nil {
		emitMailLog(logStep, "ERROR", "smtp.connect_failed", "SMTP client setup failed", map[string]any{"error": err.Error(), "host": mailbox.SMTPHost, "port": mailbox.SMTPPort})
		return fmt.Errorf("smtp connect setup: %w", err)
	}
	defer client.Close()
	emitMailLog(logStep, "INFO", "smtp.connect_ok", "SMTP connection established", map[string]any{"host": mailbox.SMTPHost, "port": mailbox.SMTPPort, "mode": mode})
	if mailbox.Username != "" {
		emitMailLog(logStep, "INFO", "smtp.auth_start", "Authenticating SMTP user", map[string]any{"username": mailbox.Username})
		if err := client.Auth(smtp.PlainAuth("", mailbox.Username, password, mailbox.SMTPHost)); err != nil {
			emitMailLog(logStep, "ERROR", "smtp.auth_failed", "SMTP authentication failed", map[string]any{"error": err.Error(), "username": mailbox.Username})
			return fmt.Errorf("smtp auth: %w", err)
		}
		emitMailLog(logStep, "INFO", "smtp.auth_ok", "SMTP authentication succeeded", map[string]any{"username": mailbox.Username})
	}
	recipient := job.Email
	if job.Payload != "" {
		var override struct {
			ToEmail string `json:"toEmail"`
		}
		if json.Unmarshal([]byte(job.Payload), &override) == nil && override.ToEmail != "" {
			recipient = override.ToEmail
		}
	}
	emitMailLog(logStep, "INFO", "smtp.mail_from", "Sending SMTP MAIL FROM", map[string]any{"from": mailbox.FromAddress})
	if err := client.Mail(mailbox.FromAddress); err != nil {
		emitMailLog(logStep, "ERROR", "smtp.mail_from_failed", "SMTP MAIL FROM was rejected", map[string]any{"error": err.Error(), "from": mailbox.FromAddress})
		return fmt.Errorf("smtp mail from: %w", err)
	}
	emitMailLog(logStep, "INFO", "smtp.rcpt_to", "Sending SMTP RCPT TO", map[string]any{"recipient": maskEmail(recipient)})
	if err := client.Rcpt(recipient); err != nil {
		emitMailLog(logStep, "ERROR", "smtp.rcpt_failed", "SMTP recipient was rejected", map[string]any{"error": err.Error(), "recipient": maskEmail(recipient)})
		return fmt.Errorf("smtp rcpt recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		emitMailLog(logStep, "ERROR", "smtp.data_failed", "SMTP DATA command failed", map[string]any{"error": err.Error()})
		return fmt.Errorf("smtp data: %w", err)
	}
	subjectText, body, err := mailContent(job)
	if err != nil {
		emitMailLog(logStep, "ERROR", "mail.content_failed", "Mail content rendering failed", map[string]any{"error": err.Error(), "channel": job.Channel})
		return fmt.Errorf("mail content: %w", err)
	}
	subject := mime.BEncoding.Encode("UTF-8", subjectText)
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", mailbox.FromAddress, recipient, subject, body)
	if _, err := writer.Write([]byte(message)); err != nil {
		emitMailLog(logStep, "ERROR", "smtp.write_failed", "Writing message body failed", map[string]any{"error": err.Error(), "subject": subjectText})
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := writer.Close(); err != nil {
		emitMailLog(logStep, "ERROR", "smtp.data_close_failed", "SMTP DATA close failed", map[string]any{"error": err.Error(), "subject": subjectText})
		return fmt.Errorf("smtp data close: %w", err)
	}
	emitMailLog(logStep, "INFO", "smtp.accepted", "SMTP server accepted the message", map[string]any{"subject": subjectText, "recipient": maskEmail(recipient)})
	if err := client.Quit(); err != nil {
		emitMailLog(logStep, "WARN", "smtp.quit_failed", "SMTP QUIT failed after message acceptance", map[string]any{"error": err.Error()})
		return fmt.Errorf("smtp quit: %w", err)
	}
	emitMailLog(logStep, "INFO", "smtp.quit_ok", "SMTP session closed cleanly", nil)
	return nil
}

func emitMailLog(logStep mailLogFunc, level, event, message string, details map[string]any) {
	if logStep != nil {
		logStep(level, event, message, details)
	}
}

func maskEmail(value string) string {
	parts := strings.Split(value, "@")
	if len(parts) != 2 {
		return value
	}
	local := parts[0]
	if local == "" {
		return "***@" + parts[1]
	}
	if len(local) <= 2 {
		return local[:1] + "***@" + parts[1]
	}
	return local[:1] + "***" + local[len(local)-1:] + "@" + parts[1]
}

func mailContent(job store.ClaimedJob) (string, string, error) {
	switch job.Channel {
	case "EMAIL", "EMAIL_TEST":
		return "Classing 每日课程简报", fmt.Sprintf(
			"你好 %s，\r\n\r\n这是 %s 的 Classing 课程简报。请打开 Classing 查看最新课表、调课与同步状态。\r\n",
			job.Username,
			job.TargetDate,
		), nil
	case "PASSWORD_RESET":
		var payload struct {
			Token     string `json:"token"`
			ExpiresAt int64  `json:"expiresAt"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil || payload.Token == "" {
			return "", "", fmt.Errorf("invalid password reset payload")
		}
		expires := time.UnixMilli(payload.ExpiresAt).Format("2006-01-02 15:04:05 MST")
		return "Classing 密码重置", fmt.Sprintf(
			"你好 %s，\r\n\r\n你正在重置 Classing 密码。请在 Classing 的“找回密码”页面输入以下一次性验证码：\r\n\r\n%s\r\n\r\n验证码将在 %s 过期。若非本人操作，请忽略本邮件。\r\n",
			job.Username,
			payload.Token,
			expires,
		), nil
	case "EMAIL_VERIFICATION":
		var payload struct {
			Code      string `json:"code"`
			ExpiresAt int64  `json:"expiresAt"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil || payload.Code == "" {
			return "", "", fmt.Errorf("invalid email verification payload")
		}
		expires := time.UnixMilli(payload.ExpiresAt).Format("2006-01-02 15:04:05 MST")
		return "Classing email verification", fmt.Sprintf(
			"Hello %s,\r\n\r\nYour Classing verification code is:\r\n\r\n%s\r\n\r\nThe code expires at %s. If you did not request this account, ignore this email.\r\n",
			job.Username, payload.Code, expires,
		), nil
	case "EMAIL_CHANGE_VERIFY":
		var payload struct {
			Code      string `json:"code"`
			ExpiresAt int64  `json:"expiresAt"`
			ToEmail   string `json:"toEmail"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil || payload.Code == "" {
			return "", "", fmt.Errorf("invalid email change verification payload")
		}
		expires := time.UnixMilli(payload.ExpiresAt).Format("2006-01-02 15:04:05 MST")
		return "Classing email change verification", fmt.Sprintf(
			"Hello %s,\r\n\r\nYou requested to change your Classing account email to %s. Use the following verification code to confirm the change:\r\n\r\n%s\r\n\r\nThe code expires at %s. If you did not request this change, please secure your account immediately.\r\n",
			job.Username, payload.ToEmail, payload.Code, expires,
		), nil
	case "EMAIL_CHANGE_NOTIFY":
		var payload struct {
			NewEmail string `json:"newEmail"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil || payload.NewEmail == "" {
			return "", "", fmt.Errorf("invalid email change notify payload")
		}
		return "Classing email address change notice", fmt.Sprintf(
			"Hello %s,\r\n\r\nA request was made to change the email address on your Classing account to %s. If you did not make this request, please change your password immediately.\r\n",
			job.Username, payload.NewEmail,
		), nil
	default:
		return "", "", fmt.Errorf("unsupported mail job channel %s", job.Channel)
	}
}
