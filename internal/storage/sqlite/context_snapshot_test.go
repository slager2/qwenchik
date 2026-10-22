package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestContextSnapshotDurabilityDedupeAndBudgetAuthority(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	id := turn.Run.ID
	if _, err := s.ContextSnapshot(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpoint(ctx, id, 1, map[string]any{"future": map[string]any{"kept": true}, "questions": []any{map[string]any{"id": "q1", "status": "open"}}, "current_round": 1}); err != nil {
		t.Fatal(err)
	}
	input := domain.ContextSnapshot{Goal: "untrusted goal", Constraints: []string{"retain exactly"}, DoneCriteria: []string{"verified"}, OpenQuestions: []string{"q1"}, BudgetRemaining: map[string]int64{"llm_calls": 999999}, WorkingSet: map[string]string{"topic": "alpha", "note": "beta"}}
	first, err := s.SaveContextSnapshot(ctx, id, input, "initial")
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.GetResearchSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if first.Number != 1 || first.Version != 1 || first.Goal != session.Goal || first.BudgetRemaining["llm_calls"] != int64(session.Budget.MaxLLMCalls) || first.BudgetRemaining["rounds"] != int64(session.Budget.MaxRounds-1) {
		t.Fatalf("snapshot: %#v", first)
	}
	before, err := s.Checkpoints(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	input.WorkingSet = map[string]string{"note": "beta", "topic": "alpha"}
	input.ID, input.Hash, input.Number = "forged", "forged", 999
	second, err := s.SaveContextSnapshot(ctx, id, input, "repeat")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("dedupe: %#v %v", second, err)
	}
	after, err := s.Checkpoints(ctx, id)
	if err != nil || len(after) != len(before) {
		t.Fatalf("dedupe appended checkpoint: %v", err)
	}
	if _, err := s.ReserveResearchLLMCall(ctx, id); err != nil {
		t.Fatal(err)
	}
	third, err := s.SaveContextSnapshot(ctx, id, first, "compaction")
	if err != nil || third.Number != 2 || third.Hash == first.Hash || third.BudgetRemaining["llm_calls"] != first.BudgetRemaining["llm_calls"]-1 {
		t.Fatalf("budget: %#v %v", third, err)
	}
	var compacted int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='context.compacted'`, id).Scan(&compacted); err != nil || compacted != 0 {
		t.Fatalf("counter emitted compaction: %d %v", compacted, err)
	}
	if _, err := s.SaveCheckpoint(ctx, id, 1, map[string]any{"summary": "ordinary worker update"}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestCheckpoint(ctx, id)
	if err != nil || latest.ContextSnapshotHash != third.Hash {
		t.Fatalf("checkpoint link: %#v %v", latest, err)
	}
	values, _ := json.Marshal(latest.State)
	state := checkpointObject(values)
	if len(state["future"]) == 0 || len(state["questions"]) == 0 || string(state["current_round"]) != "1" {
		t.Fatalf("frontier lost: %s", values)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ContextSnapshot(ctx, id)
	if err != nil || !reflect.DeepEqual(got, third) {
		t.Fatalf("reopen: %#v %v", got, err)
	}
}

func TestContextSnapshotWorkingSetBoundsAndProtection(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	id := turn.Run.ID
	initial, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{Constraints: []string{"must retain"}, DoneCriteria: []string{"done"}}, "initial")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"goal", "constraints", "done_criteria", "accepted_evidence_ids", "budget_remaining", "counters", "hash", "context_snapshot", "Goal", "../goal", "a.b"} {
		if _, err := s.PatchWorkingSet(ctx, id, map[string]string{key: "overwrite"}); err == nil {
			t.Fatalf("accepted key %q", key)
		}
	}
	updated, err := s.PatchWorkingSet(ctx, id, map[string]string{"finding": strings.Repeat("界", 1024)})
	if err != nil || updated.Goal != initial.Goal || !reflect.DeepEqual(updated.Constraints, initial.Constraints) || !reflect.DeepEqual(updated.DoneCriteria, initial.DoneCriteria) || !reflect.DeepEqual(updated.BudgetRemaining, initial.BudgetRemaining) {
		t.Fatalf("patch changed protected fields: %#v %v", updated, err)
	}
	if _, err := s.PatchWorkingSet(ctx, id, map[string]string{"finding": strings.Repeat("界", 1025)}); !errors.Is(err, ErrContextSnapshotTooLarge) {
		t.Fatalf("string overflow: %v", err)
	}
	tooMany := make(map[string]string)
	for i := 0; i < 21; i++ {
		tooMany["key_"+string(rune('a'+i))] = "x"
	}
	if _, err := s.PatchWorkingSet(ctx, id, tooMany); !errors.Is(err, ErrContextSnapshotTooLarge) {
		t.Fatalf("map overflow: %v", err)
	}
	large := map[string]string{}
	for i := 0; i < 6; i++ {
		large["key_"+string(rune('a'+i))] = strings.Repeat("界", 1024)
	}
	if _, err := s.PatchWorkingSet(ctx, id, large); !errors.Is(err, ErrContextSnapshotTooLarge) {
		t.Fatalf("byte overflow: %v", err)
	}
	updated.Constraints = []string{strings.Repeat("x", 33<<10)}
	if _, err := s.SaveContextSnapshot(ctx, id, updated, "oversized"); !errors.Is(err, domain.ErrContextSnapshotTooLarge) {
		t.Fatalf("snapshot overflow: %v", err)
	}
	retained, err := s.ContextSnapshot(ctx, id)
	if err != nil || retained.Number != 2 || !reflect.DeepEqual(retained.Constraints, initial.Constraints) {
		t.Fatalf("overflow replaced snapshot: %#v %v", retained, err)
	}
}

func TestContextSnapshotOwnedSuccessfulEvidence(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	id := turn.Run.ID
	foreign, err := s.CreateTask(ctx, domain.Task{ID: "foreign-context-task", Title: "foreign", Description: "other"}, "foreign-context")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, task, flags, excerpt string
		status                   int
	}{
		{"owned", id, `[]`, "source quotation", 200},
		{"foreign", foreign.ID, `[]`, "source quotation", 200},
		{"failed", id, `[]`, "error body", 500},
		{"flagged", id, `["low_quality"]`, "source quotation", 200},
		{"snippet", id, `[]`, "", 200},
	} {
		_, err := s.db.Exec(`INSERT INTO evidence(id,task_id,provider,original_query,original_url,canonical_url,title,language,retrieved_at,http_status,content_sha256,extraction_method,quote_spans_json,quality_flags_json,excerpt) VALUES(?,?,'http','','https://example.org','https://example.org/'||?,'','',?,?,'hash','text','[]',?,?)`, item.id, item.task, item.id, stamp(time.Now()), item.status, item.flags, item.excerpt)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, evidence := range []string{"missing", "foreign", "failed", "flagged", "snippet"} {
		if _, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{AcceptedEvidenceIDs: []string{evidence}}, "evidence"); !errors.Is(err, ErrCheckpointInvalid) {
			t.Fatalf("accepted %s: %v", evidence, err)
		}
	}
	if _, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{AcceptedEvidenceIDs: []string{"owned"}}, "evidence"); err != nil {
		t.Fatal(err)
	}
}

func TestContextSnapshotRollbackRetainsOldVersion(t *testing.T) {
	for _, boundary := range []string{"event", "checkpoint"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			id := turn.Run.ID
			first, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{Summary: "old"}, "initial")
			if err != nil {
				t.Fatal(err)
			}
			var eventsBefore, checkpointsBefore int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, id).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE task_id=?`, id).Scan(&checkpointsBefore); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER reject_context BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'injected'); END`
			if boundary == "event" {
				trigger = `CREATE TRIGGER reject_context BEFORE INSERT ON events WHEN NEW.type='context.snapshot_saved' BEGIN SELECT RAISE(ABORT,'injected'); END`
			}
			if _, err := s.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			first.Summary = "new"
			if _, err := s.SaveContextSnapshot(ctx, id, first, "update"); err == nil {
				t.Fatal("injected failure accepted")
			}
			got, err := s.ContextSnapshot(ctx, id)
			if err != nil || got.Summary != "old" || got.Number != 1 {
				t.Fatalf("old version lost: %#v %v", got, err)
			}
			var eventsAfter, checkpointsAfter int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, id).Scan(&eventsAfter); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE task_id=?`, id).Scan(&checkpointsAfter); err != nil {
				t.Fatal(err)
			}
			if eventsBefore != eventsAfter || checkpointsBefore != checkpointsAfter {
				t.Fatal("partial commit")
			}
		})
	}
}

func TestContextSnapshotCompactionEventIsBounded(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	id := turn.Run.ID
	first, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{Summary: strings.Repeat("sensitive transcript", 100), Constraints: []string{"must preserve"}}, "initial")
	if err != nil {
		t.Fatal(err)
	}
	first.Summary = "compact"
	second, err := s.SaveContextSnapshot(ctx, id, first, "compaction")
	if err != nil {
		t.Fatal(err)
	}
	var payload, visibility string
	if err := s.db.QueryRow(`SELECT payload_json,visibility FROM events WHERE task_id=? AND type='context.compacted'`, id).Scan(&payload, &visibility); err != nil {
		t.Fatal(err)
	}
	if len(payload) > 1024 || visibility != "user" || strings.Contains(payload, "sensitive") || strings.Contains(payload, first.Goal) || strings.Contains(payload, "must preserve") || !strings.Contains(payload, second.Hash) {
		t.Fatalf("event payload: %s", payload)
	}
}

func TestContextSnapshotRejectsCorruptHistory(t *testing.T) {
	for _, mutation := range []string{"hash", "number", "version", "foreign", "link", "old_hash"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			id := turn.Run.ID
			first, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{Summary: "first"}, "initial")
			if err != nil {
				t.Fatal(err)
			}
			old, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			first.Summary = "second"
			if _, err := s.SaveContextSnapshot(ctx, id, first, "update"); err != nil {
				t.Fatal(err)
			}
			cp, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "old_hash" {
				cp = old
			}
			raw := mustCheckpointJSON(t, s, id, cp.ID)
			state := checkpointObject([]byte(raw))
			var snapshot domain.ContextSnapshot
			if err := json.Unmarshal(state["context_snapshot"], &snapshot); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "hash", "old_hash":
				snapshot.Hash = strings.Repeat("0", 64)
			case "number":
				snapshot.Number = 99
			case "version":
				snapshot.Version = 99
			case "foreign":
				snapshot.ID = snapshotID("foreign-task", snapshot.Hash)
			case "link":
				cp.ContextSnapshotHash = "wrong"
			}
			state["context_snapshot"], _ = json.Marshal(snapshot)
			encoded, _ := json.Marshal(state)
			digest := sha256.Sum256(encoded)
			if _, err := s.db.Exec(`UPDATE checkpoints SET state_json=?,state_hash=?,context_snapshot_hash=? WHERE id=?`, string(encoded), hex.EncodeToString(digest[:]), cp.ContextSnapshotHash, cp.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ContextSnapshot(ctx, id); !errors.Is(err, ErrCheckpointInvalid) {
				t.Fatalf("corruption accepted: %v", err)
			}
			if _, err := s.LatestCheckpoint(ctx, id); !errors.Is(err, ErrCheckpointInvalid) {
				t.Fatalf("checkpoint corruption accepted: %v", err)
			}
		})
	}
}

func TestContextSnapshotExecutionFenceCancellationAndLegacy(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	if err := s.EnableWorkerLeases("context-worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	if _, err := s.SaveContextSnapshot(ctx, task.ID, domain.ContextSnapshot{}, "initial"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("unowned actor: %v", err)
	}
	if _, err := s.SaveContextSnapshot(leased, task.ID, domain.ContextSnapshot{}, "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchWorkingSet(ctx, task.ID, map[string]string{"note": "x"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("unowned patch: %v", err)
	}
	cancelled, cancel := context.WithCancel(leased)
	cancel()
	if _, err := s.PatchWorkingSet(cancelled, task.ID, map[string]string{"note": "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: %v", err)
	}
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveContextSnapshot(leased, task.ID, domain.ContextSnapshot{}, "late"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("cancelled actor: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.ContextSnapshot(ctx, task.ID); err != nil || got.Number != 1 {
		t.Fatalf("cancelled snapshot lost: %#v %v", got, err)
	}
	if _, err := reopened.PatchWorkingSet(ctx, task.ID, map[string]string{"note": "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal task accepted patch: %v", err)
	}
	legacy, err := reopened.CreateTask(ctx, domain.Task{ID: "legacy-context-task", Title: "legacy", Description: "legacy goal"}, "legacy-context")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.TransitionTask(ctx, legacy.ID, domain.StatusQueued, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	claimResearchFixture(t, reopened)
	got, err := reopened.SaveContextSnapshot(ctx, legacy.ID, domain.ContextSnapshot{Goal: "legacy goal", BudgetRemaining: map[string]int64{"llm_calls": 999}}, "initial")
	if err != nil || got.Goal != "legacy goal" || len(got.BudgetRemaining) != 0 {
		t.Fatalf("legacy source: %#v %v", got, err)
	}
}

func TestContextSnapshotGoalOverflowAndActiveFence(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if _, err := s.SaveContextSnapshot(ctx, turn.Run.ID, domain.ContextSnapshot{}, "initial"); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued task accepted snapshot: %v", err)
	}
	claimResearchFixture(t, s)
	goal := strings.Repeat("user requirement ", 3000)
	if _, err := s.db.Exec(`UPDATE research_sessions SET goal=? WHERE task_id=?`, goal, turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveContextSnapshot(ctx, turn.Run.ID, domain.ContextSnapshot{}, "initial"); !errors.Is(err, ErrContextSnapshotTooLarge) {
		t.Fatalf("oversized authoritative goal was truncated: %v", err)
	}
	if _, err := s.ContextSnapshot(ctx, turn.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("overflow persisted: %v", err)
	}
}

func TestContextSnapshotPreservesInt64BudgetAndFutureFields(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) {
		session.Policy.TaskFetchBudgetBytes = 1<<54 + 1
	})
	claimResearchFixture(t, s)
	id := turn.Run.ID
	if _, err := s.SaveCheckpoint(ctx, id, 1, map[string]json.RawMessage{"future": json.RawMessage(`{"integer":18014398509481985}`)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{}, "initial")
	if err != nil || snapshot.BudgetRemaining["fetched_bytes"] != 1<<54+1 {
		t.Fatalf("budget precision: %#v %v", snapshot, err)
	}
	got, err := s.ContextSnapshot(ctx, id)
	if err != nil || !reflect.DeepEqual(snapshot, got) {
		t.Fatalf("read precision: %#v %v", got, err)
	}
	cp, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	state := checkpointObject([]byte(mustCheckpointJSON(t, s, id, cp.ID)))
	if string(state["future"]) != `{"integer":18014398509481985}` {
		t.Fatalf("future field precision lost: %s", state["future"])
	}
}
