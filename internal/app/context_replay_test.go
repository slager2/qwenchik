package app

import (
	"context"
	"errors"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdempotentTurnReplayDoesNotRequireLiveContextDiscovery(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	c, err := service.CreateConversation(ctx, domain.Conversation{Mode: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.SetModelSnapshotResolver(func(_ context.Context, m domain.ModelProfileSnapshot) (domain.ModelProfileSnapshot, error) {
		calls++
		m.RuntimeFingerprint = strings.Repeat("a", 64)
		return m, nil
	})
	input := domain.SubmitTurn{ConversationID: c.ID, Content: "Hello", Mode: "chat"}
	first, err := service.SubmitTurn(ctx, input, "stable-key")
	if err != nil {
		t.Fatal(err)
	}
	service.SetModelSnapshotResolver(func(context.Context, domain.ModelProfileSnapshot) (domain.ModelProfileSnapshot, error) {
		calls++
		return domain.ModelProfileSnapshot{}, llm.ErrContextUnverified
	})
	replayed, err := service.SubmitTurn(ctx, input, "stable-key")
	if err != nil || replayed.Run.ID != first.Run.ID || calls != 1 {
		t.Fatalf("committed replay depended on model: %v calls=%d", err, calls)
	}
	input.Content = "different"
	if _, err := service.SubmitTurn(ctx, input, "stable-key"); !errors.Is(err, sqlite.ErrIdempotency) {
		t.Fatalf("conflicting replay accepted: %v", err)
	}
}
