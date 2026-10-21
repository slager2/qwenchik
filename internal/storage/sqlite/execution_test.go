package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func claimResearchFixture(t *testing.T, s *Store) domain.Task {
	t.Helper()
	task, ok, err := s.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	return task
}

func TestExecutionLeaseFencesRenewRecoveryAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	for _, ttl := range []time.Duration{time.Second, 301 * time.Second} {
		if s.EnableWorkerLeases("worker-a", ttl) == nil {
			t.Fatal("invalid TTL accepted")
		}
	}
	if err := s.EnableWorkerLeases("worker-a", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	if task.Lease == nil {
		t.Fatal("missing lease")
	}
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	if _, err := s.SaveCheckpoint(ctx, task.ID, 1, map[string]any{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("unowned write: %v", err)
	}
	if err := s.RenewTaskLease(leased, task.ID); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.EnableWorkerLeases("worker-b", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if n, err := other.RecoverInterrupted(ctx); err != nil || n != 0 {
		t.Fatalf("live owner recovered: %d %v", n, err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET tasklease_expires_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewTaskLease(leased, task.ID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired renewal: %v", err)
	}
	if n, err := other.RecoverInterrupted(ctx); err != nil || n != 1 {
		t.Fatalf("expired recovery: %d %v", n, err)
	}
	claimed := claimResearchFixture(t, other)
	if claimed.Lease.Epoch <= task.Lease.Epoch {
		t.Fatal("epoch did not advance")
	}
	if _, err := s.SaveCheckpoint(leased, task.ID, 1, map[string]any{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale write: %v", err)
	}
	plan := domain.Plan{SchemaVersion: 1, Goal: "Fenced plan", MaxSteps: 1, FinalDeliverable: "Answer", Subtasks: []domain.PlanStep{{ID: "one", Kind: "analysis", Description: "Analyze", SuccessCriteria: []string{"Verified"}}}}
	for name, write := range map[string]func() error{
		"plan":       func() error { _, err := s.SavePlan(leased, task.ID, plan, nil); return err },
		"step":       func() error { return s.SetStepStatus(leased, task.ID, "one", "completed") },
		"completion": func() error { _, err := s.CompleteTask(leased, task.ID, "stale", nil); return err },
		"delta": func() error {
			_, err := s.RecordMessageDelta(leased, task.ID, "stream", "message", 0, "stale")
			return err
		},
		"artifact": func() error {
			_, err := s.SaveArtifact(leased, domain.Artifact{ID: "stale", TaskID: task.ID}, task.ID+"/stale")
			return err
		},
		"evidence": func() error {
			_, err := s.SaveEvidence(leased, domain.Evidence{TaskID: task.ID, Provider: "http", CanonicalURL: "https://example.org", OriginalURL: "https://example.org", ContentSHA256: "hash"})
			return err
		},
		"reservation": func() error { _, err := s.ReserveResearchLLMCall(leased, task.ID); return err },
		"source_audit": func() error {
			_, err := s.RecordEvent(leased, task.ID, "source.updated", nil, "audit", nil)
			return err
		},
	} {
		if err := write(); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale %s: %v", name, err)
		}
	}
	if _, err := other.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if err := other.TouchRun(domain.WithExecutionLease(ctx, *claimed.Lease), task.ID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("cancelled owner heartbeat: %v", err)
	}
}

func TestResourcePauseDurableProbeAndCancelRace(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	if err := s.EnableWorkerLeases("worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	if _, err := s.ReserveResearchLLMCall(leased, task.ID); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(leased)
	cancel()
	paused, err := s.PauseTaskForResource(context.WithoutCancel(canceled), task.ID, "llama_timeout", time.Now().Add(-time.Second), map[string]any{"partial": "retained", "pending_tool": "external_action", "pending_tool_idempotent": false})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Lease != nil || paused.PauseReason != "resource_guard" {
		t.Fatalf("pause: %#v", paused)
	}
	if _, err := s.ReserveResearchLLMCall(ctx, task.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("paused reservation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.RecoverInterrupted(ctx); err != nil || n != 0 {
		t.Fatalf("paused restart: %d %v", n, err)
	}
	due, err := s.ResourcePausedTasks(ctx, time.Now(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due: %v %v", due, err)
	}
	paused, err = s.ResolveResourceProbe(ctx, task.ID, false, "still_hot", time.Now().Add(time.Hour), 2)
	if err != nil || paused.ResourceProbeAttempts != 1 {
		t.Fatalf("probe: %#v %v", paused, err)
	}
	if due, err := s.ResourcePausedTasks(ctx, time.Now(), 10); err != nil || len(due) != 0 {
		t.Fatalf("backoff ignored: %v %v", due, err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET resume_after=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), task.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.ResolveResourceProbe(ctx, task.ID, true, "healthy", time.Time{}, 2)
		if err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		_, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "", "user", nil)
		if err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	final, err := s.GetTask(ctx, task.ID)
	if err != nil || final.Status != domain.StatusCancelled {
		t.Fatalf("resurrected cancel: %#v %v", final, err)
	}
	cp, err := s.LatestCheckpoint(ctx, task.ID)
	if err != nil || cp.State.(map[string]any)["partial"] != "retained" {
		t.Fatalf("partial lost: %#v %v", cp, err)
	}
	session, err := s.GetResearchSession(ctx, task.ID)
	if err != nil || session.Counters.LLMCalls != 1 {
		t.Fatalf("budget lost: %#v %v", session, err)
	}
}

func TestResourceProbeFiniteEscalationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cap", true: "deadline"}[deadline], func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			if _, err := s.PauseTaskForResource(ctx, turn.Run.ID, "oom", time.Now().Add(-time.Second), map[string]any{}); err != nil {
				t.Fatal(err)
			}
			if deadline {
				if _, err := s.db.Exec(`UPDATE tasks SET deadline_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), turn.Run.ID); err != nil {
					t.Fatal(err)
				}
			}
			task, err := s.ResolveResourceProbe(ctx, turn.Run.ID, false, "oom", time.Now().Add(time.Hour), 1)
			want := "blocked"
			if deadline {
				want = "budget_exhausted"
			}
			if err != nil || task.Status != domain.StatusFailed || task.TerminalOutcome != want {
				t.Fatalf("terminal: %#v %v", task, err)
			}
		})
	}
}

func TestCheckpointIntegrityAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	first, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"partial": "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"partial": "two"})
	if err != nil || second.ParentID != first.ID || second.StateHash == "" {
		t.Fatalf("chain: %#v %v", second, err)
	}
	if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, make(chan int)); err == nil {
		t.Fatal("invalid state accepted")
	}
	items, err := s.Checkpoints(ctx, turn.Run.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("prior rows lost: %#v %v", items, err)
	}
	original := mustCheckpointJSON(t, s, first.TaskID, second.ID)
	for _, mutation := range []string{`state_json='{}'`, `parent_id='alien'`, `state_schema_version=9`, `last_event_seq=9000`} {
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE checkpoints SET `+mutation+` WHERE id=?`, second.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LatestCheckpoint(ctx, turn.Run.ID); !errors.Is(err, ErrCheckpointInvalid) {
			t.Fatalf("mutation %s accepted: %v", mutation, err)
		}
		if _, err := s.db.Exec(`UPDATE checkpoints SET state_json=?,parent_id=?,state_schema_version=1,last_event_seq=? WHERE id=?`, original, first.ID, second.LastEventSeq, second.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func mustCheckpointJSON(t *testing.T, s *Store, taskID, id string) string {
	t.Helper()
	var raw string
	if err := s.db.QueryRow(`SELECT state_json FROM checkpoints WHERE task_id=? AND id=?`, taskID, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMigration010OnlyExtendsExistingTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "009.db")
	db := researchLegacyDatabase(t, path, 9)
	seedResearchMigrationLegacy(t, db)
	before := researchMigrationDataSnapshot(t, db)
	schema := researchMigrationSchemaSnapshot(t, db)
	fts := ftsSchemaSnapshot(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !reflect.DeepEqual(before, researchMigrationDataSnapshot(t, s.db)) {
		t.Fatal("existing data changed")
	}
	after := researchMigrationSchemaSnapshot(t, s.db)
	if len(after) != len(schema) {
		t.Fatal("schema objects added")
	}
	for name, sql := range schema {
		if name != "table:tasks" && name != "table:checkpoints" && after[name] != sql {
			t.Fatalf("object changed: %s", name)
		}
	}
	if fts != ftsSchemaSnapshot(t, s.db) {
		t.Fatal("FTS changed")
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cp, err := s.LatestCheckpoint(ctx, "legacy")
	if err != nil || cp.StateSchemaVersion != 0 || cp.StateHash != "" {
		t.Fatalf("legacy: %#v %v", cp, err)
	}
}

func TestCommitStepResultIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	plan := domain.Plan{SchemaVersion: 1, Goal: "Atomic outcome", MaxSteps: 1, FinalDeliverable: "Answer", Subtasks: []domain.PlanStep{{ID: "search_1", Kind: "search", Description: "Search", SuccessCriteria: []string{"Found"}}}}
	if _, err := s.SavePlan(ctx, turn.Run.ID, plan, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusRunning, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepStatus(ctx, turn.Run.ID, "search_1", "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_outcome_checkpoint BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"completed_steps": []string{"search_1"}, "tool_results": map[string]any{"search_1": map[string]any{"found": true}}}
	if _, err := s.CommitStepResult(ctx, turn.Run.ID, "search_1", "search", "found", map[string]any{"found": true}, state); err == nil {
		t.Fatal("checkpoint failure ignored")
	}
	steps, err := s.Steps(ctx, turn.Run.ID)
	if err != nil || steps[0].Status != "running" {
		t.Fatalf("step survived rollback: %#v %v", steps, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type IN ('tool.completed','step.completed')`, turn.Run.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("events survived rollback: %d %v", count, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_outcome_checkpoint`); err != nil {
		t.Fatal(err)
	}
	cp, err := s.CommitStepResult(ctx, turn.Run.ID, "search_1", "search", "found", map[string]any{"found": true}, state)
	if err != nil || cp.ParentID == "" {
		t.Fatalf("commit: %#v %v", cp, err)
	}
	steps, err = s.Steps(ctx, turn.Run.ID)
	if err != nil || steps[0].Status != "completed" {
		t.Fatalf("step not committed: %#v %v", steps, err)
	}
	if _, err := s.CommitStepResult(ctx, turn.Run.ID, "search_1", "search", "duplicate", nil, state); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate result: %v", err)
	}
}

func TestLeaseRecoveryRetainsUnknownOutcome(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if err := s.EnableWorkerLeases("worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	if _, err := s.SaveCheckpoint(leased, turn.Run.ID, 1, map[string]any{"pending_tool": "external_action", "pending_tool_idempotent": false, "current_step": "effect", "partial": "saved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET tasklease_expires_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), task.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RecoverInterrupted(ctx); err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	task, err := s.GetTask(ctx, task.ID)
	if err != nil || task.Status != domain.StatusPaused || task.PauseReason != "recovery_required" || task.Lease != nil {
		t.Fatalf("unknown retried: %#v %v", task, err)
	}
	if _, ok, err := s.ClaimNext(ctx); err != nil || ok {
		t.Fatalf("unknown auto-claimed: %v %v", ok, err)
	}
	cp, err := s.LatestCheckpoint(ctx, task.ID)
	if err != nil || cp.State.(map[string]any)["pending_tool"] != "external_action" {
		t.Fatalf("unknown erased: %#v %v", cp, err)
	}
}

func TestResearchReservationsRequireActiveExecution(t *testing.T) {
	for _, status := range []domain.TaskStatus{domain.StatusQueued, domain.StatusPaused, domain.StatusWaitingApproval} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			if _, err := s.db.Exec(`UPDATE tasks SET status=? WHERE id=?`, status, turn.Run.ID); err != nil {
				t.Fatal(err)
			}
			for _, reserve := range []func() (domain.ResearchCounters, error){func() (domain.ResearchCounters, error) { return s.ReserveResearchLLMCall(ctx, turn.Run.ID) }, func() (domain.ResearchCounters, error) { return s.ReserveResearchHTTPAttempt(ctx, turn.Run.ID) }, func() (domain.ResearchCounters, error) {
				return s.ReserveResearchOperation(ctx, turn.Run.ID, "search", "test")
			}, func() (domain.ResearchCounters, error) {
				return s.ReserveResearchOperation(ctx, turn.Run.ID, "fetch", "test")
			}} {
				if _, err := reserve(); !errors.Is(err, ErrConflict) {
					t.Fatalf("reservation permitted: %v", err)
				}
			}
		})
	}
}

func TestResourceResumePreservesDeadlineAndManualPauseWins(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "manual"}[manual], func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"partial": "retained"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PauseTaskForResource(ctx, turn.Run.ID, "oom", time.Now().Add(-time.Second), nil); err != nil {
				t.Fatal(err)
			}
			if manual {
				if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusPaused, nil, "", "user", nil); err != nil {
					t.Fatal(err)
				}
			}
			task, err := s.ResolveResourceProbe(ctx, turn.Run.ID, true, "healthy", time.Time{}, 3)
			if err != nil {
				t.Fatal(err)
			}
			if manual {
				if task.Status != domain.StatusPaused || task.PauseReason != "manual" {
					t.Fatalf("manual pause overridden: %#v", task)
				}
			} else if task.Status != domain.StatusQueued || task.PauseReason != "" || task.ResourceReason != "" || task.ResumeAfter != nil || task.Lease != nil {
				t.Fatalf("resource pause not cleared: %#v", task)
			}
			run, err := s.GetRun(ctx, turn.Run.ID)
			if err != nil || !run.DeadlineAt.Equal(*turn.Run.DeadlineAt) {
				t.Fatalf("deadline reset: %#v %v", run, err)
			}
			cp, err := s.LatestCheckpoint(ctx, turn.Run.ID)
			if err != nil || cp.State.(map[string]any)["partial"] != "retained" {
				t.Fatalf("partial lost: %#v %v", cp, err)
			}
		})
	}
}

func TestResourcePauseCheckpointFailureRollsBackOwnershipAndStatus(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if err := s.EnableWorkerLeases("worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	prior, err := s.SaveCheckpoint(leased, turn.Run.ID, 1, map[string]any{"partial": "prior"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_pause_checkpoint BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'pause fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseTaskForResource(leased, turn.Run.ID, "oom", time.Now().Add(time.Minute), map[string]any{"partial": "new"}); err == nil {
		t.Fatal("checkpoint failure ignored")
	}
	after, err := s.GetTask(ctx, turn.Run.ID)
	if err != nil || after.Status != domain.StatusPlanning || after.Lease == nil || after.Lease.Epoch != task.Lease.Epoch || after.PauseReason != "" {
		t.Fatalf("partial pause committed: %#v %v", after, err)
	}
	cp, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || cp.ID != prior.ID {
		t.Fatalf("prior checkpoint lost: %#v %v", cp, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='run.paused'`, turn.Run.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pause event escaped rollback: %d %v", count, err)
	}
}
