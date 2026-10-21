package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

func TestConversationServiceValidatesSettingsAndSubmitsDefaults(t *testing.T) {
	ctx := context.Background()
	store, e := sqlite.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	service := New(store)
	for _, c := range []domain.Conversation{
		{Mode: "remote"},
		{ModelProfile: map[string]any{"api_key": "never-store"}},
		{ResearchSettings: map[string]any{"token": "never-store"}},
		{ModelProfile: map[string]any{"max_output_tokens": 1.5}},
		{ResearchSettings: map[string]any{"profile": "unknown"}},
		{ResearchSettings: map[string]any{"time_budget_seconds": 59}},
		{ResearchSettings: map[string]any{"time_budget_seconds": 60.5}},
		{ResearchSettings: map[string]any{"time_budget_seconds": "600"}},
	} {
		if _, e = service.CreateConversation(ctx, c); !errors.Is(e, domain.ErrInvalidConversation) {
			t.Fatalf("accepted invalid settings %#v: %v", c, e)
		}
	}
	c, e := service.CreateConversation(ctx, domain.Conversation{})
	if e != nil || c.Mode != "chat" || c.Title == "" {
		t.Fatalf("defaults %#v %v", c, e)
	}
	if _, e = service.UpdateConversation(ctx, c.ID, map[string]any{"model_profile": map[string]any{"base_url": "https://remote"}}); !errors.Is(e, domain.ErrInvalidConversation) {
		t.Fatal(e)
	}
	if _, e = service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "   "}, ""); !errors.Is(e, domain.ErrInvalidConversation) {
		t.Fatal(e)
	}
	turn, e := service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: " hi "}, "key")
	if e != nil || turn.UserMessage.Content != "hi" || turn.Run.Mode != "chat" {
		t.Fatalf("submit %#v %v", turn, e)
	}
	if _, e = service.CancelRun(ctx, turn.Run.ID); e != nil {
		t.Fatal(e)
	}
	retry, e := service.RepeatRun(ctx, turn.Run.ID, "retry", "retry-key")
	if e != nil || retry.UserMessage.ID != turn.UserMessage.ID {
		t.Fatalf("retry %#v %v", retry, e)
	}
}
