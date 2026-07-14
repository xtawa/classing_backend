package store

import (
	"context"
	"testing"

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
			if _, err := data.db.ExecContext(ctx, `UPDATE ai_config SET enabled=1, default_monthly_limit=10, provider_kind='OPENAI_COMPATIBLE', model='test-model' WHERE id=1`); err != nil {
				t.Fatalf("enable AI: %v", err)
			}

			started, err := data.StartAIRequest(ctx, user.ID, AIStartInput{
				ClientRequestID: "request-1",
				Message:         "What class is next?",
				Timetable:       `{"lessons":[{"title":"Math"}]}`,
			})
			if err != nil {
				t.Fatalf("start AI request: %v", err)
			}
			if started.RequestID == "" || started.Conversation.ID == "" {
				t.Fatalf("missing request or conversation ID: %+v", started)
			}
			if _, err := data.CommitAIQuota(ctx, started.RequestID); err != nil {
				t.Fatalf("commit AI quota: %v", err)
			}
			if err := data.FinishAIRequest(ctx, started.RequestID, "Math is next.", "COMPLETE", "", 25); err != nil {
				t.Fatalf("finish AI request: %v", err)
			}

			var messages []model.AIMessage
			if err := data.db.SelectContext(ctx, &messages, data.rebind(`SELECT * FROM ai_messages WHERE conversation_id=? ORDER BY created_at, role DESC`), started.Conversation.ID); err != nil {
				t.Fatalf("list AI messages: %v", err)
			}
			if len(messages) != 2 || messages[0].Role != "USER" || messages[0].Status != "COMPLETE" || messages[1].Role != "ASSISTANT" || messages[1].Content != "Math is next." {
				t.Fatalf("unexpected persisted messages: %+v", messages)
			}
		})
	}
}
