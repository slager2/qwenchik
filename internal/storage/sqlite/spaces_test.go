package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestSpacesKeepConversationAssignmentAndReturnDeletedDataToUnfiled(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "spaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	space, err := store.CreateSpace(ctx, domain.Space{Title: "Project", Description: "Long-lived context"})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.CreateConversation(ctx, domain.Conversation{Title: "Scoped", Mode: "chat", SpaceID: space.ID, ModelProfile: map[string]any{}, ResearchSettings: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetConversation(ctx, conversation.ID)
	if err != nil || loaded.SpaceID != space.ID {
		t.Fatalf("conversation space = %q, err=%v", loaded.SpaceID, err)
	}
	if err := store.DeleteSpace(ctx, space.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetConversation(ctx, conversation.ID)
	if err != nil || loaded.SpaceID != "" {
		t.Fatalf("deleted space did not release conversation: %q, err=%v", loaded.SpaceID, err)
	}
}
