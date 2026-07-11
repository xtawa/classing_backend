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
	for {
		select {
		case <-ctx.Done():
			return
		case <-scheduleTicker.C:
			w.schedule(ctx)
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
		if local.Hour() != hour || local.Minute() != minute {
			continue
		}
		if item.LastScheduledAt > 0 && time.UnixMilli(item.LastScheduledAt).In(location).Format("2006-01-02") == local.Format("2006-01-02") {
			continue
		}
		_, err = w.store.QueueBriefingJob(ctx, item.UserID, local.Format("2006-01-02"), "EMAIL", now.UnixMilli())
		if err != nil && err != store.ErrConflict {
			w.log.Error("queue briefing", "user_id", item.UserID, "error", err)
			continue
		}
		_ = w.store.MarkBriefingScheduled(ctx, item.UserID, now.UnixMilli())
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
	mailbox, err := w.store.AcquireMailbox(ctx, time.Now().Format("2006-01-02"))
	if err != nil {
		_ = w.store.FailBriefingJob(ctx, job.ID, "mailbox pool exhausted", job.RetryCount)
		return
	}
	if err := send(mailbox, job); err != nil {
		w.log.Warn("send briefing", "job_id", job.ID, "mailbox_id", mailbox.ID, "error", err)
		_ = w.store.FailBriefingJob(ctx, job.ID, err.Error(), job.RetryCount)
		return
	}
	if err := w.store.CompleteBriefingJob(ctx, job.ID, mailbox.ID); err != nil {
		w.log.Error("complete briefing job", "job_id", job.ID, "error", err)
	}
}

func send(mailbox model.Mailbox, job store.ClaimedJob) error {
	secretName := strings.TrimPrefix(mailbox.PasswordSecretRef, "env:")
	if secretName == mailbox.PasswordSecretRef || secretName == "" {
		return fmt.Errorf("unsupported mailbox secret reference")
	}
	password := os.Getenv(secretName)
	if password == "" {
		return fmt.Errorf("mailbox secret %s is empty", secretName)
	}
	address := net.JoinHostPort(mailbox.SMTPHost, strconv.Itoa(mailbox.SMTPPort))
	var client *smtp.Client
	var err error
	if mailbox.SMTPPort == 465 {
		connection, dialErr := tls.Dial("tcp", address, &tls.Config{ServerName: mailbox.SMTPHost, MinVersion: tls.VersionTLS12})
		if dialErr != nil {
			return dialErr
		}
		client, err = smtp.NewClient(connection, mailbox.SMTPHost)
	} else {
		client, err = smtp.Dial(address)
		if err == nil {
			if ok, _ := client.Extension("STARTTLS"); ok {
				err = client.StartTLS(&tls.Config{ServerName: mailbox.SMTPHost, MinVersion: tls.VersionTLS12})
			}
		}
	}
	if err != nil {
		return err
	}
	defer client.Close()
	if mailbox.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", mailbox.Username, password, mailbox.SMTPHost)); err != nil {
			return err
		}
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
	if err := client.Mail(mailbox.FromAddress); err != nil {
		return err
	}
	if err := client.Rcpt(recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	subjectText, body, err := mailContent(job)
	if err != nil {
		return err
	}
	subject := mime.BEncoding.Encode("UTF-8", subjectText)
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", mailbox.FromAddress, recipient, subject, body)
	if _, err := writer.Write([]byte(message)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
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
