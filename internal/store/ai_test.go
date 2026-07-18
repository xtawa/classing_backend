package store

import (
	"context"
	"testing"

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
			if len(usage) != 1 || usage[0].EffectiveLimit != 10000 {
				t.Fatalf("admin usage did not expose inherited effective limit: %+v", usage)
			}
			if err := data.SetAIQuota(ctx, user.ID, []string{user.ID}, AIQuotaLimited, 2500); err != nil {
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
