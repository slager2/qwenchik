package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func ftsSchemaSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE name='memory_fts' OR name LIKE 'memories_fts_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result strings.Builder
	count := 0
	for rows.Next() {
		var name, definition string
		if err = rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		result.WriteString(name)
		result.WriteString(definition)
		count++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("unexpected FTS objects %d", count)
	}
	return result.String()
}

func TestLinkedDeltaReplayWorksAcrossResetAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	s, turn := conversationFixture(t, "chat")
	verifyConversationRun(t, s, turn.Run.ID)
	first, e := s.RecordMessageDelta(ctx, turn.Run.ID, "old-stream", "random-id", 0, "old")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RecordMessageDelta(ctx, turn.Run.ID, "old-stream", "random-id", 1, " text"); e != nil {
		t.Fatal(e)
	}
	reset, e := s.RecordMessageDelta(ctx, turn.Run.ID, "new-stream", "another-id", 0, "replacement")
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.RecordMessageDelta(ctx, turn.Run.ID, "old-stream", "third-generated-id", 0, "old")
	if e != nil || replay.ID != first.ID {
		t.Fatalf("historical replay: %#v %v", replay, e)
	}
	messages, e := s.ConversationMessages(ctx, turn.ConversationID)
	if e != nil || messages[1].Content != "replacement" || messages[1].LastEventSeq != reset.Seq {
		t.Fatalf("old replay changed reset: %#v %v", messages, e)
	}
	if _, e = s.RecordMessageDelta(ctx, turn.Run.ID, "old-stream", "random-id", 0, "conflicting"); !errors.Is(e, ErrConflict) {
		t.Fatalf("conflicting replay: %v", e)
	}
	if _, e = s.CompleteTask(ctx, turn.Run.ID, "replacement", nil); e != nil {
		t.Fatal(e)
	}
	replay, e = s.RecordMessageDelta(ctx, turn.Run.ID, "new-stream", "random-id", 0, "replacement")
	if e != nil || replay.ID != reset.ID {
		t.Fatalf("completed replay: %#v %v", replay, e)
	}
	if _, e = s.RecordMessageDelta(ctx, turn.Run.ID, "new-stream", "random-id", 1, "late"); !errors.Is(e, ErrConflict) {
		t.Fatalf("late append accepted: %v", e)
	}
}

func TestConversationExecutionSettingsFreezeAndWebPermission(t *testing.T) {
	ctx := context.Background()
	s, turn := conversationFixture(t, "research")
	for _, patch := range []map[string]any{{"research_settings": map[string]any{"web_enabled": false}}, {"model_profile": map[string]any{"temperature": 0.5}}, {"mode": "chat"}} {
		if _, e := s.UpdateConversation(ctx, turn.ConversationID, patch); !errors.Is(e, ErrConflict) {
			t.Fatalf("changed active settings %#v: %v", patch, e)
		}
	}
	if _, e := s.UpdateConversation(ctx, turn.ConversationID, map[string]any{"title": "Renamed"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "", "user", nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.UpdateConversation(ctx, turn.ConversationID, map[string]any{"research_settings": map[string]any{"web_enabled": false}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Mode: "research", Content: "No internet permitted"}, ""); !errors.Is(e, ErrWebDisabled) {
		t.Fatalf("research escaped web permission: %v", e)
	}
	if _, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Operation: "retry", SourceRunID: turn.Run.ID}, ""); !errors.Is(e, ErrWebDisabled) {
		t.Fatalf("retry escaped web permission: %v", e)
	}
	chat, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Mode: "chat", Content: "Offline chat"}, "")
	if e != nil || chat.Run.Mode != "chat" {
		t.Fatalf("offline chat rejected: %#v %v", chat, e)
	}
}

func TestLinkedCancelledDeltaReplayIsReadOnly(t *testing.T) {
	ctx := context.Background()
	s, turn := conversationFixture(t, "chat")
	verifyConversationRun(t, s, turn.Run.ID)
	first, e := s.RecordMessageDelta(ctx, turn.Run.ID, "stream", "generated", 0, "partial")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "task.cancelled", "user", nil); e != nil {
		t.Fatal(e)
	}
	before, e := s.ConversationMessages(ctx, turn.ConversationID)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.RecordMessageDelta(ctx, turn.Run.ID, "stream", "different-generated", 0, "partial")
	if e != nil || replay.ID != first.ID {
		t.Fatalf("cancelled replay %#v %v", replay, e)
	}
	after, e := s.ConversationMessages(ctx, turn.ConversationID)
	if e != nil || after[1].Status != "cancelled" || after[1].Content != "partial" || after[1].LastEventSeq != before[1].LastEventSeq {
		t.Fatalf("cancelled replay mutated message %#v %v", after, e)
	}
}

func TestQueuedResearchRecoveryRetainsDeadlineAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, turn := conversationFixture(t, "research")
	checkpoint, e := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"operation": "queued-research", "evidence_ids": []string{"saved"}})
	if e != nil {
		t.Fatal(e)
	}
	if n, e := s.RecoverInterrupted(ctx); e != nil || n != 0 {
		t.Fatalf("queued run incorrectly recovered: %d %v", n, e)
	}
	if _, ok, e := s.ClaimNext(ctx); e != nil || !ok {
		t.Fatal(e)
	}
	if n, e := s.RecoverInterrupted(ctx); e != nil || n != 1 {
		t.Fatalf("planning recovery: %d %v", n, e)
	}
	r, e := s.GetRun(ctx, turn.Run.ID)
	if e != nil || r.Status != domain.StatusQueued || !r.DeadlineAt.Equal(*turn.Run.DeadlineAt) {
		t.Fatalf("recovery changed deadline/state %#v %v", r, e)
	}
	got, e := s.LatestCheckpoint(ctx, turn.Run.ID)
	if e != nil || got.ID != checkpoint.ID {
		t.Fatalf("recovery replaced checkpoint %#v %v", got, e)
	}
	if _, ok, e := s.ClaimNext(ctx); e != nil || !ok {
		t.Fatal(e)
	}
	if _, ok, e := s.ClaimNext(ctx); e != nil || ok {
		t.Fatalf("duplicate claim ok=%t error=%v", ok, e)
	}
}

func TestPreviouslyQueuedResearchCannotBypassDisabledWeb(t *testing.T) {
	ctx := context.Background()
	s, turn := conversationFixture(t, "research")
	if _, e := s.db.Exec(`UPDATE conversations SET research_settings_json='{"web_enabled":false}' WHERE id=?`, turn.ConversationID); e != nil {
		t.Fatal(e)
	}
	if _, ok, e := s.ClaimNext(ctx); e != nil || ok {
		t.Fatalf("disabled research dispatched ok=%t error=%v", ok, e)
	}
	r, e := s.GetRun(ctx, turn.Run.ID)
	if e != nil || r.Status != domain.StatusFailed || r.ErrorCode != "web_disabled" {
		t.Fatalf("permission failure not persisted %#v %v", r, e)
	}
	messages, e := s.ConversationMessages(ctx, turn.ConversationID)
	if e != nil || messages[1].Status != "failed" {
		t.Fatalf("placeholder status %#v %v", messages, e)
	}
}
