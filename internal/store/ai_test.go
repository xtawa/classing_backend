package store

import (
	"context"
	"testing"
	"time"

	"github.com/xtawa/classing-backend/internal/aicost"
	"github.com/xtawa/classing-backend/internal/model"
)

func TestAIRequestPersistsUserAndAssistantMessages(t *testing.T) {
	for _, dialect := range testDialects(t) {
		t.Run(dialect.name, func(t *testing.T) {
			data := dialect.open(t)
			ctx := context.Background()
			user, err := data.CreateUser(ctx, "aiuser", "aiuser@example.test", "hash", model.RoleUser)
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			if _, err := data.db.ExecContext(ctx, `UPDATE ai_config SET enabled=1, default_monthly_limit=10000, provider_kind='OPENAI_COMPATIBLE', model='deepseek-v4-flash' WHERE id=1`); err != nil {
				t.Fatalf("enable AI: %v", err)
			}

			started, err := data.StartAIRequest(ctx, user.ID, AIStartInput{
				ClientRequestID:      "request-1",
				Message:              "What class is next?",
				Timetable:            `{"lessons":[{"title":"Math"}]}`,
				Model:                "deepseek-v4-flash",
				EstimatedInputTokens: 100,
			})
			if err != nil {
				t.Fatalf("start AI request: %v", err)
			}
			if started.RequestID == "" || started.Conversation.ID == "" {
				t.Fatalf("missing request or conversation ID: %+v", started)
			}
			if _, err := data.SettleAIQuota(ctx, started.RequestID, aicost.TokenUsage{InputTokens: 100, OutputTokens: 50}); err != nil {
				t.Fatalf("commit AI quota: %v", err)
			}
			if err := data.FinishAIRequest(ctx, started.RequestID, "Math is next.", "COMPLETE", "", 25); err != nil {
				t.Fatalf("finish AI request: %v", err)
			}
			followUp, err := data.StartAIRequest(ctx, user.ID, AIStartInput{
				ConversationID:       started.Conversation.ID,
				ClientRequestID:      "request-2",
				Message:              "And after that?",
				Model:                "deepseek-v4-pro",
				EstimatedInputTokens: 200,
			})
			if err != nil {
				t.Fatalf("start follow-up AI request: %v", err)
			}
			if _, err := data.SettleAIQuota(ctx, followUp.RequestID, aicost.TokenUsage{InputTokens: 200, CachedInputTokens: 100, OutputTokens: 80}); err != nil {
				t.Fatalf("commit follow-up AI quota: %v", err)
			}
			if err := data.FinishAIRequest(ctx, followUp.RequestID, "Physics follows.", "COMPLETE", "", 25); err != nil {
				t.Fatalf("finish follow-up AI request: %v", err)
			}

			var messages []model.AIMessage
			if err := data.db.SelectContext(ctx, &messages, data.rebind(`SELECT * FROM ai_messages WHERE conversation_id=? ORDER BY created_at, role DESC`), started.Conversation.ID); err != nil {
				t.Fatalf("list AI messages: %v", err)
			}
			if len(messages) != 4 {
				t.Fatalf("unexpected persisted messages: %+v", messages)
			}
			assistantReplies := map[string]string{}
			for _, message := range messages {
				if message.Role == "USER" && message.Status != "COMPLETE" {
					t.Fatalf("user message was not completed: %+v", message)
				}
				if message.Role == "ASSISTANT" {
					assistantReplies[message.ClientRequestID] = message.Content
				}
			}
			if assistantReplies[started.RequestID] != "Math is next." || assistantReplies[followUp.RequestID] != "Physics follows." {
				t.Fatalf("assistant replies not associated with requests: %+v", assistantReplies)
			}

			usage, _, err := data.ListAIUsageAdmin(ctx, 10, 0)
			if err != nil {
				t.Fatalf("list admin AI usage: %v", err)
			}
			if len(usage) != 1 || usage[0].EffectiveLimit != aicost.FreeMonthlyLimit || usage[0].IsMember {
				t.Fatalf("admin usage did not expose inherited effective limit: %+v", usage)
			}
			if err := data.SetAIQuota(ctx, user.ID, []string{user.Email}, AIQuotaLimited, 2500); err != nil {
				t.Fatalf("set limited AI quota: %v", err)
			}
			usage, _, err = data.ListAIUsageAdmin(ctx, 10, 0)
			if err != nil {
				t.Fatalf("list limited admin AI usage: %v", err)
			}
			if len(usage) != 1 || usage[0].EffectiveLimit != 2500 {
				t.Fatalf("admin usage did not expose custom effective limit: %+v", usage)
			}
		})
	}
}

func TestAICreditWalletCarriesOverAndPaysAfterMonthlyQuota(t *testing.T) {
	for _, dialect := range testDialects(t) {
		t.Run(dialect.name, func(t *testing.T) {
			data := dialect.open(t)
			ctx := context.Background()
			user, err := data.CreateUser(ctx, "credituser", "credituser@example.test", "hash", model.RoleUser)
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			if _, err := data.db.ExecContext(ctx, `UPDATE ai_config SET enabled=1, default_monthly_limit=1, provider_kind='OPENAI_COMPATIBLE', model='deepseek-v4-flash' WHERE id=1`); err != nil {
				t.Fatalf("enable AI: %v", err)
			}
			if _, err := data.SetMembership(ctx, user.ID, user.ID, "ANNUAL", time.Now().Add(24*time.Hour).UnixMilli(), "GRANT"); err != nil {
				t.Fatalf("grant membership: %v", err)
			}
			balance, err := data.GrantAICredits(ctx, user.ID, user.Username, 500, "manual payment")
			if err != nil || balance != 500 {
				t.Fatalf("grant AI credits: balance=%d err=%v", balance, err)
			}

			started, err := data.StartAIRequest(ctx, user.ID, AIStartInput{
				ClientRequestID:      "credit-request",
				Message:              "Summarize my schedule",
				Timetable:            `{"lessons":[{"title":"Math"}]}`,
				Model:                "deepseek-v4-flash",
				EstimatedInputTokens: 1,
			})
			if err != nil {
				t.Fatalf("start request using combined quota: %v", err)
			}
			tokens := aicost.TokenUsage{InputTokens: 1, OutputTokens: 100}
			cost := aicost.Points("deepseek-v4-flash", tokens)
			usage, err := data.SettleAIQuota(ctx, started.RequestID, tokens)
			if err != nil {
				t.Fatalf("settle AI credits: %v", err)
			}
			if usage.Used != 1 || usage.CreditBalance != 500-(cost-1) || usage.Reserved != 0 {
				t.Fatalf("monthly quota was not consumed before persistent credits: cost=%d usage=%+v", cost, usage)
			}

			adminUsage, _, err := data.ListAIUsageAdmin(ctx, 10, 0)
			if err != nil || len(adminUsage) != 1 || adminUsage[0].CreditBalance != usage.CreditBalance {
				t.Fatalf("admin usage missing credit balance: usage=%+v err=%v", adminUsage, err)
			}
			var transactions int
			if err := data.db.GetContext(ctx, &transactions, data.rebind(`SELECT COUNT(*) FROM ai_credit_transactions WHERE user_id=?`), user.ID); err != nil || transactions != 2 {
				t.Fatalf("credit ledger should contain grant and usage: count=%d err=%v", transactions, err)
			}
		})
	}
}

func TestAIFreeQuotaAndExpiredMemberCreditFreeze(t *testing.T) {
	for _, dialect := range testDialects(t) {
		t.Run(dialect.name, func(t *testing.T) {
			data := dialect.open(t)
			ctx := context.Background()
			user, err := data.CreateUser(ctx, "freeuser", "freeuser@example.test", "hash", model.RoleUser)
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			if _, err := data.db.ExecContext(ctx, `UPDATE ai_config SET enabled=1, default_monthly_limit=10000, max_output_tokens=4096, provider_kind='OPENAI_COMPATIBLE', model='deepseek-v4-flash' WHERE id=1`); err != nil {
				t.Fatalf("enable AI: %v", err)
			}
			if _, err := data.GrantAICredits(ctx, user.ID, user.Email, 2500, "previous purchase"); err != nil {
				t.Fatalf("grant credits: %v", err)
			}
			usage, err := data.AIUsage(ctx, user.ID)
			if err != nil {
				t.Fatalf("free usage: %v", err)
			}
			if usage.Limit != aicost.FreeMonthlyLimit || usage.IsMember || !usage.CreditFrozen || usage.CreditAvailable != 0 || usage.CreditBalance != 2500 {
				t.Fatalf("unexpected free usage: %+v", usage)
			}
			if _, err := data.db.ExecContext(ctx, data.rebind(`UPDATE ai_usage_monthly SET used=? WHERE user_id=? AND period=?`), 450, user.ID, usage.Period); err != nil {
				t.Fatalf("seed free usage: %v", err)
			}
			_, err = data.StartAIRequest(ctx, user.ID, AIStartInput{ClientRequestID: "free-over-limit", Message: "question", Timetable: `{"lessons":[{"title":"Math"}]}`, Model: "deepseek-v4-flash", EstimatedInputTokens: 1})
			if err != ErrUnavailable {
				t.Fatalf("frozen credits should not extend free quota: %v", err)
			}
			if _, err := data.SetMembership(ctx, user.ID, user.ID, "ANNUAL", time.Now().Add(24*time.Hour).UnixMilli(), "GRANT"); err != nil {
				t.Fatalf("activate membership: %v", err)
			}
			usage, err = data.AIUsage(ctx, user.ID)
			if err != nil || !usage.IsMember || usage.CreditFrozen || usage.CreditAvailable != 2500 || usage.Limit != 10000 {
				t.Fatalf("unexpected member usage: %+v err=%v", usage, err)
			}
			if _, err := data.SetMembership(ctx, user.ID, user.ID, "FREE", 0, "REVOKE"); err != nil {
				t.Fatalf("expire membership: %v", err)
			}
			usage, err = data.AIUsage(ctx, user.ID)
			if err != nil || usage.IsMember || !usage.CreditFrozen || usage.CreditAvailable != 0 || usage.CreditBalance != 2500 {
				t.Fatalf("expired membership did not refreeze credits: %+v err=%v", usage, err)
			}
		})
	}
}
