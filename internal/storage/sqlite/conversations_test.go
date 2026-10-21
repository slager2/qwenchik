package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func conversationFixture(t *testing.T, mode string) (*Store, domain.SubmitResponse) {
	t.Helper()
	ctx := context.Background()
	s, e := Open(ctx, filepath.Join(t.TempDir(), "conversations.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	c, e := s.CreateConversation(ctx, domain.Conversation{Title: "Conversation", Mode: mode, ModelProfile: map[string]any{}, ResearchSettings: map[string]any{}})
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "First question", Mode: mode}, "first")
	if e != nil {
		t.Fatal(e)
	}
	return s, r
}

func verifyConversationRun(t *testing.T, s *Store, id string) {
	t.Helper()
	ctx := context.Background()
	claimed, ok, e := s.ClaimNext(ctx)
	if e != nil || !ok || claimed.ID != id {
		t.Fatalf("claim: %#v %t %v", claimed, ok, e)
	}
	for _, status := range []domain.TaskStatus{domain.StatusRunning, domain.StatusVerifying} {
		if _, e = s.TransitionTask(ctx, id, status, nil, "", "user", nil); e != nil {
			t.Fatal(e)
		}
	}
}

func TestConversationDeltaResetCancellationAndRetry(t *testing.T) {
	ctx := context.Background()
	s, first := conversationFixture(t, "chat")
	verifyConversationRun(t, s, first.Run.ID)
	if first.Run.DeadlineAt == nil {
		t.Fatal("missing durable deadline")
	}
	if e := s.TouchRun(ctx, first.Run.ID); e != nil {
		t.Fatal(e)
	}
	for _, chunk := range []struct {
		stream string
		index  int
		text   string
	}{{"old", 0, "discard"}, {"new", 0, "partial"}, {"new", 1, " text"}} {
		if _, e := s.RecordMessageDelta(ctx, first.Run.ID, chunk.stream, "random-research-message", chunk.index, chunk.text); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "task.cancelled", "user", nil); e != nil {
		t.Fatal(e)
	}
	ms, e := s.ConversationMessages(ctx, first.ConversationID)
	if e != nil || len(ms) != 2 || ms[1].Content != "partial text" || ms[1].Status != "cancelled" {
		t.Fatalf("partial lost: %#v %v", ms, e)
	}
	if _, e = s.CompleteTask(ctx, first.Run.ID, "late completion", nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("late completion: %v", e)
	}
	retry, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, SourceRunID: first.Run.ID, Operation: "retry"}, "retry")
	if e != nil {
		t.Fatal(e)
	}
	if retry.UserMessage.ID != first.UserMessage.ID || retry.Run.ID == first.Run.ID || retry.AssistantMessage.ParentMessageID != first.UserMessage.ID || retry.Run.RetryCount != 1 {
		t.Fatalf("wrong retry: %#v", retry)
	}
	replay, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, SourceRunID: first.Run.ID, Operation: "retry"}, "retry")
	if e != nil || replay.Run.ID != retry.Run.ID {
		t.Fatalf("retry replay: %#v %v", replay, e)
	}
	verifyConversationRun(t, s, retry.Run.ID)
	if _, e = s.CompleteTask(ctx, retry.Run.ID, "new answer", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompleteTask(ctx, retry.Run.ID, "new answer", nil); e != nil {
		t.Fatal(e)
	}
	continued, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, SourceRunID: retry.Run.ID, Operation: "continue"}, "")
	if e != nil {
		t.Fatal(e)
	}
	if continued.AssistantMessage.ParentMessageID != retry.AssistantMessage.ID || continued.Run.ContinueFromMessageID != retry.AssistantMessage.ID || continued.UserMessage.ID != first.UserMessage.ID {
		t.Fatalf("wrong continuation: %#v", continued)
	}
	ms, e = s.ConversationMessages(ctx, first.ConversationID)
	if e != nil || len(ms) != 4 {
		t.Fatalf("duplicated user: %#v %v", ms, e)
	}
	legacy, e := s.Messages(ctx, retry.Run.ID)
	if e != nil || len(legacy) != 1 || legacy[0].ID != retry.AssistantMessage.ID {
		t.Fatalf("legacy final projection: %#v %v", legacy, e)
	}
}

func TestConversationAtomicIdempotencyAndConcurrency(t *testing.T) {
	ctx := context.Background()
	s, first := conversationFixture(t, "chat")
	if _, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "different", Mode: "chat"}, "first"); !errors.Is(e, ErrIdempotency) {
		t.Fatalf("hash mismatch accepted: %v", e)
	}
	if _, e := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "", "user", nil); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "race"}, "")
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	ms, e := s.ConversationMessages(ctx, first.ConversationID)
	if e != nil || len(ms) != 4 {
		t.Fatalf("non-atomic submit: %#v %v", ms, e)
	}
	if _, e = s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, SourceRunID: first.Run.ID, Operation: "retry"}, ""); !errors.Is(e, ErrConflict) {
		t.Fatalf("older retry accepted: %v", e)
	}
}

func TestConversationRecoveryPreservesPartialAndDeadline(t *testing.T) {
	for _, mode := range []string{"chat", "research"} {
		for _, partial := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/empty", true: "/partial"}[partial], func(t *testing.T) {
				ctx := context.Background()
				s, r := conversationFixture(t, mode)
				verifyConversationRun(t, s, r.Run.ID)
				if partial {
					if _, e := s.RecordMessageDelta(ctx, r.Run.ID, "stream", "generated", 0, "saved"); e != nil {
						t.Fatal(e)
					}
				}
				if n, e := s.RecoverInterrupted(ctx); e != nil || n != 1 {
					t.Fatalf("recovery %d %v", n, e)
				}
				got, e := s.GetRun(ctx, r.Run.ID)
				if e != nil {
					t.Fatal(e)
				}
				want := domain.StatusQueued
				if mode == "chat" && partial {
					want = domain.StatusFailed
				}
				if got.Status != want || !got.DeadlineAt.Equal(*r.Run.DeadlineAt) {
					t.Fatalf("wrong recovery %#v", got)
				}
				ms, e := s.ConversationMessages(ctx, r.ConversationID)
				if e != nil {
					t.Fatal(e)
				}
				if partial && ms[1].Content != "saved" {
					t.Fatalf("partial erased %#v", ms)
				}
				if want == domain.StatusFailed && (got.ErrorCode != "generation_interrupted" || ms[1].Status != "interrupted") {
					t.Fatalf("missing interruption metadata %#v %#v", got, ms)
				}
			})
		}
	}
}

func TestConversationDeleteCancelsAndPreservesData(t *testing.T) {
	ctx := context.Background()
	s, r := conversationFixture(t, "chat")
	ids, e := s.DeleteConversation(ctx, r.ConversationID)
	if e != nil || len(ids) != 1 || ids[0] != r.Run.ID {
		t.Fatalf("delete %#v %v", ids, e)
	}
	if _, e = s.GetConversation(ctx, r.ConversationID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	run, e := s.GetRun(ctx, r.Run.ID)
	if e != nil || run.Status != domain.StatusCancelled || !run.CancelRequested {
		t.Fatalf("run not cancelled %#v %v", run, e)
	}
	var count int
	if e = s.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id=?`, r.ConversationID).Scan(&count); e != nil || count != 2 {
		t.Fatalf("data removed %d %v", count, e)
	}
}

func TestConversationMigrationPreservesLegacy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, e := sql.Open("sqlite3", path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)`); e != nil {
		t.Fatal(e)
	}
	entries, e := migrationFiles.ReadDir("migrations")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Name() >= "008" {
			continue
		}
		body, e := migrationFiles.ReadFile("migrations/" + entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(body)); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(`INSERT INTO schema_migrations VALUES(?,?)`, entry.Name(), stamp(time.Now())); e != nil {
			t.Fatal(e)
		}
	}
	now := stamp(time.Now())
	if _, e = db.Exec(`INSERT INTO tasks(id,title,description,status,created_at,updated_at) VALUES('legacy','Legacy','Preserve','inbox',?,?)`, now, now); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO messages(id,task_id,role,content,created_at,idempotency_key) VALUES('message','legacy','user','Historical',?,'original-key')`, now); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO memories(id,kind,status,text,metadata_json,scope,created_at,updated_at) VALUES('legacy-user-memory','preference','accepted','Legacy durable memory citrus','{}','global',?,?)`, now, now); e != nil {
		t.Fatal(e)
	}
	ftsBefore := ftsSchemaSnapshot(t, db)
	db.Close()
	s, e := Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ms, e := s.Messages(ctx, "legacy")
	if e != nil || len(ms) != 1 || ms[0].Content != "Historical" {
		t.Fatalf("legacy changed %#v %v", ms, e)
	}
	if _, e = s.GetRun(ctx, "legacy"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("legacy identified as conversation run: %v", e)
	}
	if after := ftsSchemaSnapshot(t, s.db); after != ftsBefore {
		t.Fatal("conversation migration changed FTS table or trigger definitions")
	}
	matches, e := s.SearchMemories(ctx, "citrus", "", 5)
	if e != nil || len(matches) != 1 || matches[0].Memory.ID != "legacy-user-memory" {
		t.Fatalf("legacy FTS search lost %#v %v", matches, e)
	}
	if _, e = s.UpdateMemory(ctx, "legacy-user-memory", "preference", "Updated durable memory raspberry", nil); e != nil {
		t.Fatal(e)
	}
	matches, e = s.SearchMemories(ctx, "raspberry", "", 5)
	if e != nil || len(matches) != 1 {
		t.Fatalf("legacy FTS update trigger lost %#v %v", matches, e)
	}
	matches, e = s.SearchMemories(ctx, "citrus", "", 5)
	if e != nil || len(matches) != 0 {
		t.Fatalf("legacy FTS retained stale text %#v %v", matches, e)
	}
	rows, e := s.db.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}

func TestConversationSubmitRollbackAndDeltaIdempotency(t *testing.T) {
	ctx := context.Background()
	s, first := conversationFixture(t, "chat")
	if _, e := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "", "user", nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec(`CREATE TRIGGER reject_placeholder BEFORE INSERT ON conversation_messages WHEN new.role='assistant' BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "rollback"}, "rollback"); e == nil {
		t.Fatal("expected transactional failure")
	}
	var tasks, messages, keys int
	if e := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE conversation_id=?`, first.ConversationID).Scan(&tasks); e != nil {
		t.Fatal(e)
	}
	if e := s.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id=?`, first.ConversationID).Scan(&messages); e != nil {
		t.Fatal(e)
	}
	if e := s.db.QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE key='rollback'`).Scan(&keys); e != nil {
		t.Fatal(e)
	}
	if tasks != 1 || messages != 2 || keys != 0 {
		t.Fatalf("partial transaction tasks=%d messages=%d keys=%d", tasks, messages, keys)
	}
	if _, e := s.db.Exec(`DROP TRIGGER reject_placeholder`); e != nil {
		t.Fatal(e)
	}
	next, e := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "retry after rollback"}, "rollback")
	if e != nil {
		t.Fatal(e)
	}
	verifyConversationRun(t, s, next.Run.ID)
	a, e := s.RecordMessageDelta(ctx, next.Run.ID, "stream", "generated", 0, "text")
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.RecordMessageDelta(ctx, next.Run.ID, "stream", "other-generated", 0, "text")
	if e != nil || a.Seq != b.Seq {
		t.Fatalf("duplicate delta %#v %v", b, e)
	}
	if _, e = s.RecordMessageDelta(ctx, next.Run.ID, "stream", "generated", 0, "different"); !errors.Is(e, ErrConflict) {
		t.Fatalf("conflicting delta: %v", e)
	}
	ms, e := s.ConversationMessages(ctx, first.ConversationID)
	if e != nil || ms[len(ms)-1].Content != "text" {
		t.Fatalf("duplicate text: %#v %v", ms, e)
	}
	if _, e = s.TransitionTask(ctx, next.Run.ID, domain.StatusFailed, nil, "task.failed", "user", map[string]any{"error_code": "model_timeout", "message": "Timed out"}); e != nil {
		t.Fatal(e)
	}
	r, e := s.GetRun(ctx, next.Run.ID)
	if e != nil || r.ErrorCode != "model_timeout" || r.CompletedAt == nil {
		t.Fatalf("failure metadata: %#v %v", r, e)
	}
	ms, e = s.ConversationMessages(ctx, first.ConversationID)
	if e != nil || ms[len(ms)-1].Content != "text" || ms[len(ms)-1].Status != "failed" {
		t.Fatalf("failed partial: %#v %v", ms, e)
	}
	if _, e = s.QueueTask(ctx, next.Run.ID, ""); !errors.Is(e, ErrConflict) {
		t.Fatalf("legacy route reused conversation run: %v", e)
	}
}

func TestConversationStopCompletionRace(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprintf("race-%d", i), func(t *testing.T) {
			ctx := context.Background()
			s, r := conversationFixture(t, "chat")
			verifyConversationRun(t, s, r.Run.ID)
			if _, e := s.RecordMessageDelta(ctx, r.Run.ID, "stream", "generated", 0, "partial"); e != nil {
				t.Fatal(e)
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, e := s.CompleteTask(ctx, r.Run.ID, "complete", nil)
				if e != nil && !errors.Is(e, ErrConflict) {
					t.Error(e)
				}
			}()
			go func() {
				defer wg.Done()
				_, e := s.TransitionTask(ctx, r.Run.ID, domain.StatusCancelled, nil, "task.cancelled", "user", nil)
				if e != nil && !errors.Is(e, domain.ErrInvalidTransition) {
					t.Error(e)
				}
			}()
			wg.Wait()
			got, e := s.GetRun(ctx, r.Run.ID)
			if e != nil {
				t.Fatal(e)
			}
			ms, e := s.ConversationMessages(ctx, r.ConversationID)
			if e != nil {
				t.Fatal(e)
			}
			m := ms[1]
			if got.Status == domain.StatusCompleted {
				if m.Content != "complete" || m.Status != "completed" {
					t.Fatalf("inconsistent completion %#v", m)
				}
			} else if got.Status == domain.StatusCancelled {
				if m.Content != "partial" || m.Status != "cancelled" {
					t.Fatalf("inconsistent stop %#v", m)
				}
			} else {
				t.Fatalf("nonterminal race %#v", got)
			}
		})
	}
}

func TestConversationPartialSurvivesDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateConversation(ctx, domain.Conversation{Title: "Restart", Mode: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Question"}, "durable")
	if err != nil {
		t.Fatal(err)
	}
	verifyConversationRun(t, s, turn.Run.ID)
	if _, err = s.RecordMessageDelta(ctx, turn.Run.ID, "stream", "generated", 0, "Saved before restart"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	messages, err := s.ConversationMessages(ctx, c.ID)
	if err != nil || len(messages) != 2 || messages[1].Content != "Saved before restart" || messages[1].Status != "interrupted" {
		t.Fatalf("restart lost partial: %#v %v", messages, err)
	}
	replay, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Question"}, "durable")
	if err != nil || replay.Run.ID != turn.Run.ID || replay.Run.Status != domain.StatusFailed {
		t.Fatalf("restart lost idempotency: %#v %v", replay, err)
	}
}

func researchConversationFixture(t *testing.T, change func(*domain.ResearchSession)) (*Store, domain.SubmitResponse, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "research-sessions.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c, err := s.CreateConversation(ctx, domain.Conversation{Title: "Research", Mode: "research"})
	if err != nil {
		t.Fatal(err)
	}
	policy, budget, model := standardResearchSessionDefaults()
	defaults := domain.ResearchSession{Profile: policy.Profile, Policy: policy, Budget: budget, ModelSnapshot: model}
	if change != nil {
		change(&defaults)
	}
	defaults.Policy.Profile, defaults.Policy.Budget = defaults.Profile, defaults.Budget
	turn, err := s.SubmitTurnWithResearchDefaults(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Find durable results"}, defaults, "research-first")
	if err != nil {
		t.Fatal(err)
	}
	return s, turn, path
}

func TestResearchSessionStandardDefaultsAndChatIsolation(t *testing.T) {
	ctx := context.Background()
	s, turn := conversationFixture(t, "research")
	session, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := session.Budget
	if session.ID != turn.Run.ID || session.TaskID != turn.Run.ID || session.ConversationID != turn.ConversationID || session.Goal != turn.UserMessage.Content || session.Status != "queued" || session.CurrentPhase != "queued" {
		t.Fatalf("session identity: %#v", session)
	}
	if b.WallTimeSeconds != 1800 || b.MaxRounds != 3 || b.MaxSteps != 12 || b.MaxSearchQueries != 8 || b.MaxFetches != 8 || b.MaxLLMCalls != 24 || b.MaxEvidence != 8 ||
		b.MaxQuestionsCreated != 16 || b.MaxQuestionsAccepted != 6 || b.MaxStagnantRounds != 2 || b.MaxCitationSources != 8 || b.MaxCitationClaims != 8 || session.Profile != "standard" || session.Policy != domain.DefaultResearchPolicy() {
		t.Fatalf("incorrect standard fallback: %#v", session)
	}
	m := session.ModelSnapshot
	if m.Version != 1 || m.SchemaVersion != 1 || m.Model != "local-qwen" || m.BaseURL != "" || m.ContextLength <= 0 || m.MaxOutputTokens <= 0 || m.StopSequences == nil || m.PromptVersion == "" {
		t.Fatalf("invalid fallback model: %#v", m)
	}
	if session.StartedAt != nil || session.HeartbeatAt != nil || session.Counters != (domain.ResearchCounters{}) || !session.DeadlineAt.Equal(session.CreatedAt.Add(1800*time.Second)) || !session.DeadlineAt.Equal(*turn.Run.DeadlineAt) {
		t.Fatalf("incorrect initial lifecycle: %#v", session)
	}
	if _, err := s.db.Exec(`INSERT INTO research_sessions(id,task_id,conversation_id,goal,status,profile,policy_json,budget_json,model_snapshot_json,counters_json,deadline_at,created_at,updated_at)
		SELECT id||'-duplicate',task_id,conversation_id,goal,status,profile,policy_json,budget_json,model_snapshot_json,counters_json,deadline_at,created_at,updated_at FROM research_sessions WHERE task_id=?`, turn.Run.ID); err == nil {
		t.Fatal("multiple sessions for one task accepted")
	}
	if _, err := s.db.Exec(`UPDATE research_sessions SET task_id='missing-task' WHERE task_id=?`, turn.Run.ID); err == nil {
		t.Fatal("session without a task accepted")
	}
	chat, chatTurn := conversationFixture(t, "chat")
	if _, err := chat.GetResearchSession(ctx, chatTurn.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chat acquired a research session: %v", err)
	}
	if _, err := chat.ReserveResearchLLMCall(ctx, chatTurn.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chat acquired a research budget: %v", err)
	}
	var count int
	if err := chat.db.QueryRow(`SELECT COUNT(*) FROM research_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("chat session count %d: %v", count, err)
	}
	checkpoint, err := chat.SaveCheckpoint(ctx, chatTurn.Run.ID, 1, map[string]any{"chat": true})
	if err != nil || !reflect.DeepEqual(checkpoint.State, map[string]any{"chat": true}) {
		t.Fatalf("chat checkpoint changed: %#v %v", checkpoint, err)
	}
}

func TestResearchSessionSnapshotsAreImmutableAcrossRetryContinueAndSend(t *testing.T) {
	ctx := context.Background()
	s, first, _ := researchConversationFixture(t, func(session *domain.ResearchSession) {
		session.Profile = domain.ResearchProfileQuick
		session.Budget.WallTimeSeconds = 43
		session.Budget.MaxLLMCalls = 2
		session.ModelSnapshot.Model = "original-model"
		session.ModelSnapshot.BaseURL = "http://127.0.0.1:8000/v1"
		session.ModelSnapshot.StopSequences = []string{"original-stop"}
	})
	verifyConversationRun(t, s, first.Run.ID)
	if _, err := s.ReserveResearchLLMCall(ctx, first.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncResearchCheckpoint(ctx, first.Run.ID, map[string]any{"current_round": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordMessageDelta(ctx, first.Run.ID, "partial", "generated", 0, "Retained answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "", "user", map[string]any{"stop_reason": "user_stopped"}); err != nil {
		t.Fatal(err)
	}
	original, err := s.GetResearchSession(ctx, first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateConversation(ctx, first.ConversationID, map[string]any{"model_profile": map[string]any{"temperature": 1.9}, "research_settings": map[string]any{"profile": "overnight_laptop"}}); err != nil {
		t.Fatal(err)
	}
	policy, budget, model := standardResearchSessionDefaults()
	model.Model = "replacement-model"
	model.StopSequences = []string{"replacement-stop"}
	for _, operation := range []string{"retry", "continue"} {
		priorID := first.Run.ID
		if operation == "continue" {
			c, err := s.GetConversation(ctx, first.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			priorID = c.LatestRunID
		}
		turn, err := s.SubmitTurn(ctx, domain.SubmitTurn{
			ConversationID: first.ConversationID, Operation: operation, SourceRunID: priorID,
			ResearchProfile: "standard", ResearchPolicy: &policy, ResearchBudget: &budget, ModelSnapshot: &model,
		}, operation)
		if err != nil {
			t.Fatal(err)
		}
		cloned, err := s.GetResearchSession(ctx, turn.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cloned.Profile != original.Profile || cloned.Policy != original.Policy || cloned.Budget != original.Budget || !reflect.DeepEqual(cloned.ModelSnapshot, original.ModelSnapshot) {
			t.Fatalf("%s changed source snapshots: %#v", operation, cloned)
		}
		if cloned.Counters != (domain.ResearchCounters{}) || cloned.CurrentRound != 0 || cloned.PlanVersion != 0 || cloned.StartedAt != nil || cloned.HeartbeatAt != nil || cloned.StopReason != "" || !cloned.DeadlineAt.Equal(cloned.CreatedAt.Add(43*time.Second)) {
			t.Fatalf("%s inherited execution state: %#v", operation, cloned)
		}
		for _, column := range []string{"policy_json", "budget_json", "model_snapshot_json"} {
			var source, copy string
			if err := s.db.QueryRow(`SELECT `+column+` FROM research_sessions WHERE task_id=?`, first.Run.ID).Scan(&source); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT `+column+` FROM research_sessions WHERE task_id=?`, turn.Run.ID).Scan(&copy); err != nil || source != copy {
				t.Fatalf("%s %s snapshot changed: %v", operation, column, err)
			}
		}
		verifyConversationRun(t, s, turn.Run.ID)
		if _, err := s.CompleteTask(ctx, turn.Run.ID, "A complete answer", nil); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := s.GetResearchSession(ctx, first.Run.ID)
	if err != nil || !reflect.DeepEqual(changed, original) {
		t.Fatalf("source session changed: %#v %v", changed, err)
	}
	provided := domain.SubmitTurn{ConversationID: first.ConversationID, Content: "A new question", DeadlineSeconds: 9, ResearchProfile: "standard", ResearchPolicy: &policy, ResearchBudget: &budget, ModelSnapshot: &model}
	next, err := s.SubmitTurn(ctx, provided, "new-send")
	if err != nil {
		t.Fatal(err)
	}
	model.StopSequences[0] = "mutated-after-submit"
	newSession, err := s.GetResearchSession(ctx, next.Run.ID)
	if err != nil || newSession.Profile != "standard" || newSession.ModelSnapshot.Model != "replacement-model" || newSession.ModelSnapshot.StopSequences[0] != "replacement-stop" || !newSession.DeadlineAt.Equal(newSession.CreatedAt.Add(9*time.Second)) {
		t.Fatalf("new send ignored provided snapshot: %#v %v", newSession, err)
	}
	if _, err := s.SubmitTurn(ctx, provided, "new-send"); err != nil {
		t.Fatalf("server snapshot change broke request idempotency: %v", err)
	}
}

func TestResearchReservationsSurviveReopenAndDenyBeforeCall(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, func(session *domain.ResearchSession) { session.Budget.MaxLLMCalls = 2 })
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var called atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			if _, err := store.ReserveResearchLLMCall(ctx, turn.Run.ID); err == nil {
				called.Add(1)
			} else if !errors.Is(err, ErrResearchBudgetExceeded) {
				t.Error(err)
			}
		}([]*Store{s, other}[i%2])
	}
	wg.Wait()
	if called.Load() != 2 {
		t.Fatalf("budget allowed %d model calls", called.Load())
	}
	for want := 3; want <= 4; want++ {
		got, err := s.ReserveResearchHTTPAttempt(ctx, turn.Run.ID)
		if err != nil || got.LLMCalls != 2 || got.HTTPAttempts != want {
			t.Fatalf("fallback HTTP attempt: %#v %v", got, err)
		}
	}
	if _, err := s.ReserveResearchHTTPAttempt(ctx, turn.Run.ID); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("fallback cap not enforced: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ReserveResearchLLMCall(ctx, turn.Run.ID); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("crash refunded a logical call: %v", err)
	}
	checkpoint, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{
		"llm_calls": 0, "http_attempts": 0, "counters": map[string]any{"llm_calls": 0, "http_attempts": 0},
		"session_id": turn.Run.ID, "profile": "untrusted", "model_snapshot": map[string]any{"model": "untrusted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || got.Counters.LLMCalls != 2 || got.Counters.HTTPAttempts != 4 {
		t.Fatalf("stale checkpoint reset reservations: %#v %v", got, err)
	}
	values := checkpoint.State.(map[string]any)
	counters := values["counters"].(map[string]any)
	if values["session_id"] != turn.Run.ID || values["profile"] != "standard" || counters["llm_calls"] != float64(2) || counters["http_attempts"] != float64(4) || values["model_snapshot"].(map[string]any)["model"] != "local-qwen" {
		t.Fatalf("checkpoint did not mirror row authority: %#v", values)
	}
	var reservations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='research.operation_reserved'`, turn.Run.ID).Scan(&reservations); err != nil || reservations != 4 {
		t.Fatalf("reservation events %d: %v", reservations, err)
	}
}

func TestResearchOperationReservationsAreAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, func(session *domain.ResearchSession) {
		session.Budget.MaxSearchQueries, session.Budget.MaxFetches = 1, 1
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "plan:1:search:1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "fetch", "plan:1:search:1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("operation ID reused for another kind: %v", err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "plan:1:search:2"); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("search cap not enforced: %v", err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "fetch_url", "plan:1:fetch:1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncResearchCheckpoint(ctx, turn.Run.ID, map[string]any{"search_queries": 0, "counters": domain.ResearchCounters{}, "completed_steps": []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "plan:1:search:1")
	if err != nil || got.SearchQueries != 1 || got.Fetches != 1 || got.LLMCalls != 0 || got.HTTPAttempts != 0 {
		t.Fatalf("operation replay changed counters: %#v %v", got, err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "fetch", "plan:1:fetch:2"); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("fetch cap not enforced: %v", err)
	}
	var reservations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='research.operation_reserved'`, turn.Run.ID).Scan(&reservations); err != nil || reservations != 2 {
		t.Fatalf("duplicate operation events %d: %v", reservations, err)
	}
}

func TestResearchSubmitAndReservationFailuresRollback(t *testing.T) {
	ctx := context.Background()
	s, first := conversationFixture(t, "research")
	if _, err := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_research_session BEFORE INSERT ON research_sessions BEGIN SELECT RAISE(ABORT,'session fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "Rollback all writes"}, "failed-session"); err == nil {
		t.Fatal("expected failed session insertion")
	}
	var tasks, messages, sessions, keys int
	var head string
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM tasks`:                                       &tasks,
		`SELECT COUNT(*) FROM conversation_messages`:                       &messages,
		`SELECT COUNT(*) FROM research_sessions`:                           &sessions,
		`SELECT COUNT(*) FROM idempotency_keys WHERE key='failed-session'`: &keys,
	} {
		if err := s.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.QueryRow(`SELECT head_message_id FROM conversations WHERE id=?`, first.ConversationID).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || messages != 2 || sessions != 1 || keys != 0 || head != first.AssistantMessage.ID {
		t.Fatalf("partial submission: tasks=%d messages=%d sessions=%d keys=%d head=%s", tasks, messages, sessions, keys, head)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_research_session`); err != nil {
		t.Fatal(err)
	}
	next, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "Accepted"}, "failed-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_research_reservation BEFORE INSERT ON events WHEN NEW.type='research.operation_reserved' BEGIN SELECT RAISE(ABORT,'event fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	for _, reserve := range []func() error{
		func() error { _, err := s.ReserveResearchLLMCall(ctx, next.Run.ID); return err },
		func() error { _, err := s.ReserveResearchHTTPAttempt(ctx, next.Run.ID); return err },
		func() error {
			_, err := s.ReserveResearchOperation(ctx, next.Run.ID, "search", "rollback-op")
			return err
		},
	} {
		if err := reserve(); err == nil {
			t.Fatal("expected failed reservation event")
		}
	}
	session, err := s.GetResearchSession(ctx, next.Run.ID)
	if err != nil || session.Counters != (domain.ResearchCounters{}) {
		t.Fatalf("failed event consumed counters: %#v %v", session, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_research_checkpoint BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'checkpoint fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpoint(ctx, next.Run.ID, 3, map[string]any{"search_queries": 3, "fetched_bytes": 128}); err == nil {
		t.Fatal("expected failed checkpoint")
	}
	session, err = s.GetResearchSession(ctx, next.Run.ID)
	if err != nil || session.Counters != (domain.ResearchCounters{}) || session.PlanVersion != 0 {
		t.Fatalf("failed checkpoint changed session: %#v %v", session, err)
	}
}

func TestResearchCheckpointReconciliationPreservesFrontierAndRowAuthority(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if _, ok, err := s.ClaimNext(ctx); err != nil || !ok {
		t.Fatalf("claim %t: %v", ok, err)
	}
	if _, err := s.ReserveResearchLLMCall(ctx, turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{SchemaVersion: 1, Goal: "Research durable results", MaxSteps: 3, FinalDeliverable: "A cited answer", Subtasks: []domain.PlanStep{
		{ID: "search_1", Kind: "search", Description: "Search", SuccessCriteria: []string{"Sources found"}},
		{ID: "fetch_1", Kind: "fetch", Description: "Fetch", SuccessCriteria: []string{"Evidence saved"}},
		{ID: "analysis_1", Kind: "analysis", Description: "Analyze", SuccessCriteria: []string{"Claims verified"}},
	}}
	frontier := map[string]any{"schema_version": 7, "future_question": map[string]any{"id": "q-next", "opaque": "retain"}}
	if _, err := s.SavePlan(ctx, turn.Run.ID, plan, map[string]any{"completed_steps": []string{}, "pending_tool": "search", "future_frontier": frontier}); err != nil {
		t.Fatal(err)
	}
	initial, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || initial.State.(map[string]any)["session_id"] != turn.Run.ID || initial.State.(map[string]any)["research_schema_version"] != float64(1) {
		t.Fatalf("SavePlan omitted research envelope: %#v %v", initial, err)
	}
	evidence, err := s.SaveEvidence(ctx, domain.Evidence{TaskID: turn.Run.ID, Provider: "direct_http", OriginalURL: "https://example.org/source", CanonicalURL: "https://example.org/source", ContentSHA256: "saved-hash"})
	if err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []string{"audit", "user"} {
		if _, err := s.RecordEvent(ctx, turn.Run.ID, "tool.failed", nil, visibility, map[string]any{"tool": "fetch_url", "step_id": "fetch_1", "attempt_id": "failed-candidate", "fetched_bytes": 64}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordEvent(ctx, turn.Run.ID, "tool.completed", nil, "user", map[string]any{"tool": "fetch_url", "step_id": "fetch_1", "result": map[string]any{"evidence_id": evidence.ID, "fetched_bytes": 128}}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.SaveCheckpoint(ctx, turn.Run.ID, 3, map[string]any{
		"completed_steps": []string{"search_1", "fetch_1", "analysis_1", "fetch_1"},
		"tool_results":    map[string]any{"search_1": []any{}, "fetch_1": map[string]any{"evidence_id": evidence.ID}, "analysis_1": map[string]any{"summary": "done"}},
		"search_queries":  2, "fetched_bytes": 100, "current_round": 2,
		"questions": []any{map[string]any{"id": "q1", "status": "accepted"}, map[string]any{"id": "q2", "status": "open"}},
		"counters":  map[string]any{"llm_calls": 9999, "http_attempts": 9999, "evidence": 9999, "fetches": 9999},
		"llm_calls": 9999, "policy": map[string]any{"budget": "untrusted"}, "budget": map[string]any{"max_llm_calls": 9999},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.ResearchCounters{LLMCalls: 1, HTTPAttempts: 1, SearchQueries: 2, Fetches: 1, FetchedBytes: 192, Evidence: 1, QuestionsCreated: 2, QuestionsAccepted: 1, Rounds: 2}
	if session.Counters != want || session.PlanVersion != 3 || session.CurrentRound != 2 {
		t.Fatalf("incorrect reconciliation: %#v", session)
	}
	values := checkpoint.State.(map[string]any)
	if values["pending_tool"] != nil || !reflect.DeepEqual(values["future_frontier"], map[string]any{"schema_version": float64(7), "future_question": map[string]any{"id": "q-next", "opaque": "retain"}}) || values["llm_calls"] != float64(1) {
		t.Fatalf("checkpoint merge changed frontier or resurrected pending state: %#v", values)
	}
	if err := s.SyncResearchCheckpoint(ctx, turn.Run.ID, map[string]any{"plan_version": 1, "search_queries": 0, "fetched_bytes": 0, "current_round": 0, "completed_steps": []string{}, "tool_results": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || again.Counters != want || again.PlanVersion != 3 || again.CurrentRound != 2 {
		t.Fatalf("stale checkpoint lowered counters: %#v %v", again, err)
	}
	for _, status := range []domain.TaskStatus{domain.StatusRunning, domain.StatusVerifying} {
		if _, err := s.TransitionTask(ctx, turn.Run.ID, status, nil, "", "user", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CompleteTask(ctx, turn.Run.ID, "Durable answer", nil); err != nil {
		t.Fatal(err)
	}
	final, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	values = final.State.(map[string]any)
	if final.PlanVersion != 3 || values["research_schema_version"] != float64(1) || values["session_id"] != turn.Run.ID || values["current_phase"] != "completed" || values["future_frontier"] == nil || len(values["questions"].([]any)) != 2 {
		t.Fatalf("CompleteTask lost versioned state: %#v", final)
	}
}

func TestResearchEvidenceCapDeduplicationAndRollback(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) { session.Budget.MaxEvidence = 1 })
	artifact, err := s.SaveArtifact(ctx, domain.Artifact{ID: "source-raw", TaskID: turn.Run.ID, Name: "source.raw", MIMEType: "text/html", SHA256: "source-hash", SizeBytes: 16}, turn.Run.ID+"/source-raw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_evidence_counter BEFORE UPDATE OF counters_json ON research_sessions BEGIN SELECT RAISE(ABORT,'evidence fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	item := domain.Evidence{TaskID: turn.Run.ID, Provider: "direct_http", OriginalURL: "https://example.org/a", CanonicalURL: "https://example.org/a", ContentSHA256: "hash-a"}
	if _, err := s.SaveEvidence(ctx, item); err == nil {
		t.Fatal("expected evidence counter failure")
	}
	items, err := s.Evidence(ctx, turn.Run.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("evidence insert survived failed counter update: %#v %v", items, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_evidence_counter`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := item
			copy.CanonicalURL = fmt.Sprintf("https://example.org/source-%d", i)
			if _, err := s.SaveEvidence(ctx, copy); err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrResearchBudgetExceeded) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("evidence cap accepted %d inserts", accepted.Load())
	}
	items, err = s.Evidence(ctx, turn.Run.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("evidence: %#v %v", items, err)
	}
	duplicate, err := s.SaveEvidence(ctx, items[0])
	if err != nil || duplicate.ID != items[0].ID {
		t.Fatalf("duplicate at cap denied: %#v %v", duplicate, err)
	}
	session, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || session.Counters.Evidence != 1 {
		t.Fatalf("evidence counter drift: %#v %v", session, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='evidence.created'`, turn.Run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate or failed evidence emitted events: %d %v", count, err)
	}
	if _, err := s.SaveArtifact(ctx, artifact, turn.Run.ID+"/source-raw"); err != nil {
		t.Fatalf("artifact replay at evidence cap denied: %v", err)
	}
	underreported := session.Counters
	underreported.Evidence = 0
	encoded, err := json.Marshal(underreported)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE research_sessions SET counters_json=? WHERE task_id=?`, string(encoded), turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	rejected, err := s.SaveArtifact(ctx, domain.Artifact{ID: "orphan-source", TaskID: turn.Run.ID, Name: "source.txt", MIMEType: "text/plain", SHA256: "new-hash", SizeBytes: 16}, turn.Run.ID+"/orphan-source")
	if !errors.Is(err, ErrResearchBudgetExceeded) || rejected.ID != "" {
		t.Fatalf("new artifact at evidence cap accepted: %#v %v", rejected, err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "fetch", "after-evidence-cap"); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("fetch at evidence cap accepted: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE task_id=?`, turn.Run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("evidence cap left orphan artifact metadata: %d %v", count, err)
	}
}

func TestResearchCheckpointBytesAndQueriesRemainMonotonicAcrossAttempts(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) { session.Budget.MaxSearchQueries = 2 })
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "search-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "fetch", "fetch-1"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		attempt string
		bytes   int64
	}{{"candidate-a", 40}, {"candidate-b", 60}} {
		for _, visibility := range []string{"audit", "user"} {
			if _, err := s.RecordEvent(ctx, turn.Run.ID, "tool.failed", nil, visibility, map[string]any{
				"tool": "fetch_url", "step_id": "fetch_1", "operation_id": "fetch-1", "attempt_id": test.attempt, "fetched_bytes": test.bytes,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.RecordEvent(ctx, turn.Run.ID, "tool.completed", nil, "user", map[string]any{
		"tool": "fetch_url", "step_id": "fetch_1", "operation_id": "fetch-1", "attempt_id": "candidate-c",
		"result": map[string]any{"evidence_id": "stored-source", "fetched_bytes": 80},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncResearchCheckpoint(ctx, turn.Run.ID, map[string]any{"search_queries": 0, "fetched_bytes": 12}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "search-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncResearchCheckpoint(ctx, turn.Run.ID, map[string]any{"search_queries": 0, "fetched_bytes": 0}); err != nil {
		t.Fatal(err)
	}
	session, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || session.Counters.SearchQueries != 2 || session.Counters.Fetches != 1 || session.Counters.FetchedBytes != 180 {
		t.Fatalf("attempt bytes lost or duplicate events double-charged: %#v %v", session, err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "search-3"); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("stale checkpoint restored query capacity: %v", err)
	}
	if err := s.SyncResearchCheckpoint(ctx, turn.Run.ID, map[string]any{"fetched_bytes": session.Policy.TaskFetchBudgetBytes}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "fetch", "fetch-2"); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("known exhausted byte budget allowed a fetch: %v", err)
	}
	after, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || after.Counters.Fetches != 1 || after.Counters.SearchQueries != 2 || after.Counters.FetchedBytes != session.Policy.TaskFetchBudgetBytes {
		t.Fatalf("denied fetch changed counters: %#v %v", after, err)
	}
}

func TestResearchReservationsRejectCancelledTerminalAndExpiredRuns(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Store, string) context.Context
		want    error
	}{
		{"parent-cancelled", func(t *testing.T, _ *Store, _ string) context.Context {
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			return cancelled
		}, context.Canceled},
		{"parent-expired", func(t *testing.T, _ *Store, _ string) context.Context {
			expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return expired
		}, context.DeadlineExceeded},
		{"session-expired", func(t *testing.T, s *Store, id string) context.Context {
			if _, err := s.db.Exec(`UPDATE research_sessions SET deadline_at=? WHERE task_id=?`, stamp(time.Now().Add(-time.Second)), id); err != nil {
				t.Fatal(err)
			}
			return ctx
		}, context.DeadlineExceeded},
		{"cancel-requested", func(t *testing.T, s *Store, id string) context.Context {
			if _, err := s.db.Exec(`UPDATE tasks SET cancel_requested=1 WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			return ctx
		}, ErrConflict},
		{"cancelled", func(t *testing.T, s *Store, id string) context.Context {
			if _, err := s.TransitionTask(ctx, id, domain.StatusCancelled, nil, "", "user", nil); err != nil {
				t.Fatal(err)
			}
			return ctx
		}, ErrConflict},
		{"completed", func(t *testing.T, s *Store, id string) context.Context {
			verifyConversationRun(t, s, id)
			if _, err := s.CompleteTask(ctx, id, "Completed", nil); err != nil {
				t.Fatal(err)
			}
			return ctx
		}, ErrConflict},
		{"failed", func(t *testing.T, s *Store, id string) context.Context {
			verifyConversationRun(t, s, id)
			if _, err := s.TransitionTask(ctx, id, domain.StatusFailed, nil, "", "user", nil); err != nil {
				t.Fatal(err)
			}
			return ctx
		}, ErrConflict},
		{"task-terminal-session-stale", func(t *testing.T, s *Store, id string) context.Context {
			if _, err := s.db.Exec(`UPDATE tasks SET status='completed' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			return ctx
		}, ErrConflict},
		{"session-terminal-task-stale", func(t *testing.T, s *Store, id string) context.Context {
			if _, err := s.db.Exec(`UPDATE research_sessions SET status='failed' WHERE task_id=?`, id); err != nil {
				t.Fatal(err)
			}
			return ctx
		}, ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, turn, _ := researchConversationFixture(t, nil)
			if _, err := s.ReserveResearchLLMCall(ctx, turn.Run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "already-reserved"); err != nil {
				t.Fatal(err)
			}
			parent := test.prepare(t, s, turn.Run.ID)
			before, err := s.GetResearchSession(ctx, turn.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var eventCount int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, turn.Run.ID).Scan(&eventCount); err != nil {
				t.Fatal(err)
			}
			for _, reserve := range []func() (domain.ResearchCounters, error){
				func() (domain.ResearchCounters, error) { return s.ReserveResearchLLMCall(parent, turn.Run.ID) },
				func() (domain.ResearchCounters, error) { return s.ReserveResearchHTTPAttempt(parent, turn.Run.ID) },
				func() (domain.ResearchCounters, error) {
					return s.ReserveResearchOperation(parent, turn.Run.ID, "search", "new-search")
				},
				func() (domain.ResearchCounters, error) {
					return s.ReserveResearchOperation(parent, turn.Run.ID, "fetch", "new-fetch")
				},
				func() (domain.ResearchCounters, error) {
					return s.ReserveResearchOperation(parent, turn.Run.ID, "search", "already-reserved")
				},
			} {
				if _, err := reserve(); !errors.Is(err, test.want) {
					t.Fatalf("reservation error = %v, want %v", err, test.want)
				}
			}
			after, err := s.GetResearchSession(ctx, turn.Run.ID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected reservation changed session: %#v %v", after, err)
			}
			var afterEvents int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, turn.Run.ID).Scan(&afterEvents); err != nil || afterEvents != eventCount {
				t.Fatalf("rejected reservation appended events: %d -> %d: %v", eventCount, afterEvents, err)
			}
		})
	}
}

func TestResearchCheckpointsRejectForeignSessionsAndFutureVersions(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"research_schema_version": 1, "session_id": turn.Run.ID, "future_frontier": map[string]any{"schema_version": 7, "unknown": "retained"}}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var eventCount, checkpointCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, turn.Run.ID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE task_id=?`, turn.Run.ID).Scan(&checkpointCount); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		state map[string]any
	}{
		{"future-version", map[string]any{"research_schema_version": 2}},
		{"invalid-version", map[string]any{"research_schema_version": "1"}},
		{"foreign-session", map[string]any{"session_id": "another-session"}},
		{"invalid-session", map[string]any{"session_id": 1}},
		{"null-counters", map[string]any{"counters": nil}},
		{"invalid-counters", map[string]any{"counters": []int{1}}},
		{"negative-counter", map[string]any{"counters": map[string]any{"llm_calls": -1}}},
		{"negative-bytes", map[string]any{"counters": map[string]any{"fetched_bytes": -1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.state["search_queries"], test.state["fetched_bytes"] = 4, 128
			for _, save := range []func() error{
				func() error { return s.SyncResearchCheckpoint(ctx, turn.Run.ID, test.state) },
				func() error { _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 3, test.state); return err },
			} {
				if err := save(); err == nil {
					t.Fatal("incompatible checkpoint accepted")
				}
			}
		})
	}
	for _, prior := range []string{
		`{"research_schema_version":2,"future_frontier":{"unknown":"retained"}}`,
		`{"research_schema_version":1,"session_id":"another-session"}`,
		`{"research_schema_version":1,"counters":{"llm_calls":-1}}`,
	} {
		if _, err := s.db.Exec(`UPDATE checkpoints SET state_json=? WHERE task_id=?`, prior, turn.Run.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.SyncResearchCheckpoint(ctx, turn.Run.ID, map[string]any{"search_queries": 4, "fetched_bytes": 128}); err == nil {
			t.Fatal("incompatible persisted checkpoint was silently overwritten")
		}
		var persisted string
		if err := s.db.QueryRow(`SELECT state_json FROM checkpoints WHERE task_id=?`, turn.Run.ID).Scan(&persisted); err != nil || persisted != prior {
			t.Fatalf("future state changed: %s %v", persisted, err)
		}
	}
	after, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid checkpoint changed session: %#v %v", after, err)
	}
	var afterEvents, afterCheckpoints int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, turn.Run.ID).Scan(&afterEvents); err != nil || afterEvents != eventCount {
		t.Fatalf("invalid checkpoint appended events: %d %v", afterEvents, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE task_id=?`, turn.Run.ID).Scan(&afterCheckpoints); err != nil || afterCheckpoints != checkpointCount {
		t.Fatalf("invalid checkpoint appended rows: %d %v", afterCheckpoints, err)
	}
}

func TestResearchLifecycleHeartbeatRecoveryAndTerminalState(t *testing.T) {
	ctx := context.Background()
	for _, terminal := range []domain.TaskStatus{domain.StatusCompleted, domain.StatusFailed, domain.StatusCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			s, turn, _ := researchConversationFixture(t, nil)
			verifyConversationRun(t, s, turn.Run.ID)
			before, err := s.GetResearchSession(ctx, turn.Run.ID)
			if err != nil || before.StartedAt == nil || before.HeartbeatAt == nil || before.Status != "verifying" {
				t.Fatalf("missing start timestamps: %#v %v", before, err)
			}
			if _, err := s.db.Exec(`CREATE TRIGGER reject_session_heartbeat BEFORE UPDATE OF heartbeat_at ON research_sessions BEGIN SELECT RAISE(ABORT,'heartbeat fixture failure'); END`); err != nil {
				t.Fatal(err)
			}
			if err := s.TouchRun(ctx, turn.Run.ID); err == nil {
				t.Fatal("expected heartbeat sync failure")
			}
			run, err := s.GetRun(ctx, turn.Run.ID)
			if err != nil || !run.HeartbeatAt.Equal(*before.HeartbeatAt) {
				t.Fatalf("task heartbeat committed without session: %#v %v", run, err)
			}
			if _, err := s.db.Exec(`DROP TRIGGER reject_session_heartbeat`); err != nil {
				t.Fatal(err)
			}
			if err := s.TouchRun(ctx, turn.Run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReserveResearchLLMCall(ctx, turn.Run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			recovered, err := s.GetResearchSession(ctx, turn.Run.ID)
			if err != nil || recovered.Status != "queued" || !recovered.DeadlineAt.Equal(before.DeadlineAt) || !recovered.StartedAt.Equal(*before.StartedAt) || recovered.Counters.LLMCalls != 1 {
				t.Fatalf("recovery restarted session: %#v %v", recovered, err)
			}
			verifyConversationRun(t, s, turn.Run.ID)
			if err := s.SetResearchPhase(ctx, turn.Run.ID, "synthesis", "max_llm_calls"); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"error_code": "model_failed", "stop_reason": "explicit_stop"}
			if terminal == domain.StatusCompleted {
				_, err = s.CompleteTask(ctx, turn.Run.ID, "A partial but valid answer", nil)
			} else {
				_, err = s.TransitionTask(ctx, turn.Run.ID, terminal, nil, "", "user", payload)
			}
			if err != nil {
				t.Fatal(err)
			}
			final, err := s.GetResearchSession(ctx, turn.Run.ID)
			wantStop := "explicit_stop"
			if terminal == domain.StatusCompleted {
				wantStop = "max_llm_calls"
			}
			if err != nil || final.Status != string(terminal) || final.CurrentPhase != string(terminal) || final.StopReason != wantStop || !final.StartedAt.Equal(*before.StartedAt) || !final.DeadlineAt.Equal(before.DeadlineAt) {
				t.Fatalf("incorrect terminal session: %#v %v", final, err)
			}
			if err := s.SetResearchPhase(ctx, turn.Run.ID, "search", ""); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale phase overwrote terminal state: %v", err)
			}
			if err := s.TouchRun(ctx, turn.Run.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("terminal heartbeat accepted: %v", err)
			}
		})
	}
}

func TestResearchSessionReadsRejectInvalidSnapshotsAndOverflow(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	for _, test := range []struct{ name, column, value string }{
		{"malformed", "policy_json", "{"},
		{"whitespace", "budget_json", "   "},
		{"unknown-field", "counters_json", `{"unexpected":1}`},
		{"duplicate-key", "counters_json", `{"llm_calls":0,"llm_calls":1}`},
		{"negative-counter", "counters_json", `{"llm_calls":-1}`},
		{"null-counter", "counters_json", `{"llm_calls":null}`},
		{"oversized", "model_snapshot_json", `{"model":"` + strings.Repeat("x", 65536) + `"}`},
		{"unsupported-version", "model_snapshot_json", `{"version":99,"schema_version":1}`},
		{"trailing-json", "counters_json", `{} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var original string
			if err := s.db.QueryRow(`SELECT `+test.column+` FROM research_sessions WHERE task_id=?`, turn.Run.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE research_sessions SET `+test.column+`=? WHERE task_id=?`, test.value, turn.Run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetResearchSession(ctx, turn.Run.ID); err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("invalid snapshot accepted or hidden: %v", err)
			}
			if _, err := s.db.Exec(`UPDATE research_sessions SET `+test.column+`=? WHERE task_id=?`, original, turn.Run.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	policy, budget, model := standardResearchSessionDefaults()
	budget.MaxLLMCalls = int(^uint(0)>>1)/2 + 1
	policy.Budget = budget
	if _, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Content: "Overflow must fail", ResearchPolicy: &policy, ResearchBudget: &budget, ModelSnapshot: &model}, "overflow"); err == nil {
		t.Fatal("overflowing HTTP attempt budget accepted")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE conversation_id=?`, turn.ConversationID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("overflow rejection left a task: %d %v", count, err)
	}
	encoded, err := json.Marshal(domain.SubmitTurn{ResearchProfile: "deep_laptop", ResearchPolicy: &policy, ResearchBudget: &budget, ModelSnapshot: &model})
	if err != nil || strings.Contains(string(encoded), "research_profile") || strings.Contains(string(encoded), "model_snapshot") || strings.Contains(string(encoded), "max_llm_calls") {
		t.Fatalf("internal snapshots leaked into submit JSON: %s %v", encoded, err)
	}
}
