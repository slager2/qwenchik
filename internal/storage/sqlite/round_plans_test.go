package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/slager2/qwenchik/internal/domain"
	"strings"
	"testing"
	"time"
)

func boundedRoundStoreFixture(t *testing.T, edits ...func(*domain.ResearchSession)) (*Store, string, domain.Checkpoint) {
	t.Helper()
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, func(r *domain.ResearchSession) {
		r.Policy.Version = 2
		r.Policy.AdaptiveEnabled = true
		r.Policy.MaxContinuations = 9
		r.Budget, _ = domain.RoundResearchBudgetForProfile(domain.ResearchProfileOvernight)
		r.Budget.MaxRoundSteps = 4
		for _, edit := range edits {
			edit(r)
		}
	})
	claimResearchFixture(t, s)
	p := domain.Plan{SchemaVersion: 1, Goal: "Research protocol", FinalDeliverable: "Substantive report", DeliverableCriteria: []string{"Explain limitations"}, MaxSteps: 1, Subtasks: []domain.PlanStep{{ID: "initial", Kind: "search", Description: "Discovery", SuccessCriteria: []string{"recorded"}}}}
	if _, err := s.SavePlan(ctx, turn.Run.ID, p, map[string]any{"completed_steps": []string{}, "tool_results": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepStatus(ctx, turn.Run.ID, "initial", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitVerification(ctx, turn.Run.ID, "RESEARCH", "Substantive evidence missing", []string{"Confirm deployment limitations"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, turn.Run.ID, checkpoint
}

func TestRoundPlanStorageUsesVariableBoundedShape(t *testing.T) {
	ctx := context.Background()
	s, id, _ := boundedRoundStoreFixture(t, func(r *domain.ResearchSession) { r.Budget.MaxRoundSteps = 12 })
	before, _ := s.GetResearchSession(ctx, id)
	stepCount, _, _, _ := domain.NextResearchRoundShape(before)
	if stepCount <= 4 {
		t.Fatal("fixture did not exercise larger bounded round")
	}
	if err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), []string{"Confirm deployment limitations"}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(checkpoint.State)
	var state struct {
		Plans domain.ResearchPlans `json:"research_plans"`
	}
	_ = json.Unmarshal(raw, &state)
	p, err := state.Plans.Current()
	if err != nil || len(p.Subtasks) != stepCount || domain.ValidatePlan(p, 12) != nil {
		t.Fatalf("round shape not persisted: %+v %v", p, err)
	}
	session, _ := s.GetResearchSession(ctx, id)
	if session.Counters.Steps != 1+stepCount {
		t.Fatal("variable steps not reserved")
	}
}

func TestBoundedRoundTransitionAtomicDuplicateAndStaleProtection(t *testing.T) {
	for _, boundary := range []string{"event", "checkpoint", "success"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			s, id, stale := boundedRoundStoreFixture(t)
			before, _ := s.GetResearchSession(ctx, id)
			if boundary == "event" {
				if _, err := s.db.Exec(`CREATE TRIGGER fail_round BEFORE INSERT ON events WHEN NEW.type='continuation.requested' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "checkpoint" {
				if _, err := s.db.Exec(`CREATE TRIGGER fail_round BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), []string{"Confirm deployment limitations"})
			after, _ := s.GetResearchSession(ctx, id)
			steps, _ := s.Steps(ctx, id)
			if boundary != "success" {
				if err == nil || len(steps) != 1 || before.Counters != after.Counters || before.CurrentRound != after.CurrentRound {
					t.Fatalf("non-atomic transition: err=%v before=%+v after=%+v", err, before, after)
				}
				return
			}
			if err != nil || len(steps) != 5 || after.Counters.Steps != 5 || after.CurrentRound != 1 {
				t.Fatalf("transition=%+v err=%v", after, err)
			}
			if err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), nil); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate transition: %v", err)
			}
			if _, err := s.SaveCheckpoint(ctx, id, 1, stale.State); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale round save accepted: %v", err)
			}
			latest, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(latest.State)
			var state struct {
				Plans    domain.ResearchPlans         `json:"research_plans"`
				Adaptive domain.AdaptiveResearchState `json:"adaptive_research"`
			}
			_ = json.Unmarshal(raw, &state)
			if state.Plans.CurrentRound != 2 || len(state.Plans.InitialPlan.Subtasks) != 1 || len(state.Plans.Rounds[1].Plan.Subtasks) != 4 || len(state.Adaptive.Frontier) == 0 {
				t.Fatalf("round/frontier lost: %+v", state)
			}
			task, _ := s.GetTask(ctx, id)
			if len(task.Plan.Subtasks) != 1 {
				t.Fatal("mutated initial task plan")
			}
			var payload string
			if err := s.db.QueryRow(`SELECT payload_json FROM events WHERE task_id=? AND type='continuation.requested'`, id).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var event struct {
				Steps []string `json:"new_steps"`
			}
			_ = json.Unmarshal([]byte(payload), &event)
			if len(event.Steps) != 4 {
				t.Fatalf("continuation omitted step IDs: %s", payload)
			}
		})
	}
}

func TestBoundedRoundDeadlineAndStagnationDoNotDispatchOrRefund(t *testing.T) {
	for _, mode := range []string{"deadline", "stagnation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, id, _ := boundedRoundStoreFixture(t)
			before, _ := s.GetResearchSession(ctx, id)
			if mode == "deadline" {
				past := stamp(time.Now().Add(-time.Second))
				if _, err := s.db.Exec(`UPDATE research_sessions SET deadline_at=? WHERE task_id=?`, past, id); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE tasks SET deadline_at=? WHERE id=?`, past, id); err != nil {
					t.Fatal(err)
				}
				err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), nil)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expired deadline admitted: %v", err)
				}
			} else {
				if err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), []string{"Confirm deployment limitations"}); err != nil {
					t.Fatal(err)
				}
				steps, _ := s.Steps(ctx, id)
				for _, step := range steps {
					if err := s.SetStepStatus(ctx, id, step.StepID, "completed"); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.CommitVerification(ctx, id, "RESEARCH", "Still no substantive evidence", []string{"Confirm deployment limitations"}, nil, nil); err != nil {
					t.Fatal(err)
				}
				err := s.RequestResearchContinuation(ctx, id, 1, strings.Repeat("c", 64), strings.Repeat("b", 64), nil)
				if err == nil {
					t.Fatal("stagnant continuation admitted")
				}
				checkpoint, _ := s.LatestCheckpoint(ctx, id)
				raw, _ := json.Marshal(checkpoint.State)
				var state struct {
					Adaptive domain.AdaptiveResearchState `json:"adaptive_research"`
				}
				_ = json.Unmarshal(raw, &state)
				if state.Adaptive.StopReason != "stagnation" {
					t.Fatalf("stagnation not saved: %+v", state.Adaptive)
				}
			}
			after, _ := s.GetResearchSession(ctx, id)
			if after.Counters.QuestionsAccepted < before.Counters.QuestionsAccepted || after.Counters.Steps < before.Counters.Steps {
				t.Fatal("refunded consumed budget")
			}
		})
	}
}

func TestRoleInputDecoderCacheStillRejectsMutationAndDuplicateFields(t *testing.T) {
	slot := strings.Repeat("a", 64)
	input := json.RawMessage(`{"a":1}`)
	hash := roleInputHash(input)
	known := map[string]pinnedRoleInput{}
	valid, _ := json.Marshal(map[string]pinnedRoleInput{slot: {Version: 1, InputHash: hash, Input: input}})
	if _, err := decodeRoleInputsCached(valid, known); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"` + slot + `":{"version":1,"version":1,"input_hash":"` + hash + `","input":{"a":1}}}`,
		`{"` + slot + `":{"version":1,"input_hash":"` + hash + `","input":{"a":2}}}`,
		`{"` + slot + `":{"version":1,"input_hash":"` + hash + `","input":{"a":1,"a":1}}}`,
	} {
		if _, err := decodeRoleInputsCached([]byte(raw), known); err == nil {
			t.Fatal("cache accepted corrupt/duplicate input")
		}
	}
}

func TestCachedCheckpointChainRevalidatesChangedAncestorEvent(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	slot := roleSlot("planner")
	if _, err := s.PinRoleContext(ctx, turn.Run.ID, slot, json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	pin, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"progress": "retained"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestCheckpoint(ctx, turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	history, err := s.Checkpoints(ctx, turn.Run.ID)
	if err != nil || len(history) < 2 {
		t.Fatal("history unavailable", err)
	}
	for _, c := range history {
		if c.State == nil {
			t.Fatal("history endpoint dropped checkpoint state")
		}
	}
	if _, err := s.db.Exec(`UPDATE events SET payload_json=json_set(payload_json,'$.input_hash',?) WHERE task_id=? AND seq=?`, strings.Repeat("0", 64), turn.Run.ID, pin.LastEventSeq); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestCheckpoint(ctx, turn.Run.ID); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("cached ancestor tampering accepted: %v", err)
	}
}
