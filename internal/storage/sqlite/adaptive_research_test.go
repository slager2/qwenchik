package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func adaptiveStoreFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) {
		session.Policy.AdaptiveEnabled = true
		session.Policy.MaxContinuations = 2
	})
	claimResearchFixture(t, s)
	p := domain.Plan{SchemaVersion: 1, Goal: "Research", FinalDeliverable: "Report", MaxSteps: 1, Subtasks: []domain.PlanStep{{ID: "initial", Kind: "search", Description: "Initial discovery", SuccessCriteria: []string{"recorded"}}}}
	if _, err := s.SavePlan(context.Background(), turn.Run.ID, p, map[string]any{"completed_steps": []string{}, "tool_results": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	return s, turn.Run.ID
}

func TestAdaptiveRoundTransitionRollsBackAndPreservesQuestionBudget(t *testing.T) {
	for _, boundary := range []string{"event", "checkpoint", "success"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			s, id := adaptiveStoreFixture(t)
			if err := s.CommitVerification(ctx, id, "RESEARCH", "Missing direct support", []string{"Confirm critical criterion"}, nil, nil); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetResearchSession(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "event" {
				_, err = s.db.Exec(`CREATE TRIGGER fail_round BEFORE INSERT ON events WHEN NEW.type='continuation.requested' BEGIN SELECT RAISE(ABORT,'fixture'); END`)
			}
			if boundary == "checkpoint" {
				_, err = s.db.Exec(`CREATE TRIGGER fail_round BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'fixture'); END`)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), []string{"Confirm critical criterion"})
			after, getErr := s.GetResearchSession(ctx, id)
			if getErr != nil {
				t.Fatal(getErr)
			}
			var steps int
			if e := s.db.QueryRow(`SELECT COUNT(*) FROM task_steps WHERE task_id=?`, id).Scan(&steps); e != nil {
				t.Fatal(e)
			}
			if boundary != "success" {
				if err == nil || steps != 1 || after.CurrentRound != before.CurrentRound || after.Counters != before.Counters {
					t.Fatalf("adaptive transition not atomic: err=%v steps=%d before=%+v after=%+v", err, steps, before, after)
				}
				return
			}
			if err != nil || steps != 5 || after.CurrentRound != 1 {
				t.Fatalf("adaptive transition: err=%v steps=%d session=%+v", err, steps, after)
			}
			if err := s.CommitVerification(ctx, id, "RESEARCH", "Still no new evidence", []string{"Confirm critical criterion"}, nil, nil); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Adaptive domain.AdaptiveResearchState `json:"adaptive_research"`
			}
			raw, _ := json.Marshal(checkpoint.State)
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Adaptive.StagnantRounds != 2 || saved.Adaptive.StopReason != "stagnation" || len(saved.Adaptive.Questions) != 1 {
				t.Fatalf("stagnation/duplicate questions: %+v", saved.Adaptive)
			}
			if err := s.RequestResearchContinuation(ctx, id, 1, strings.Repeat("c", 64), strings.Repeat("b", 64), []string{"Confirm critical criterion"}); !errors.Is(err, ErrResearchBudgetExceeded) {
				t.Fatalf("stagnant loop admitted: %v", err)
			}
		})
	}
}

func TestAdaptiveAssessmentAuthorityAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s, id := adaptiveStoreFixture(t)
	stale, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	missing := []string{"Critical goal gap"}
	if err := s.CommitVerification(ctx, id, "RESEARCH", "Missing", missing, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitVerification(ctx, id, "RESEARCH", "Missing", missing, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitVerification(ctx, id, "PASS", "Changed assessment", nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("different same-round assessment accepted: %v", err)
	}
	if err := s.StopAdaptiveResearch(ctx, id, "step_budget"); err != nil {
		t.Fatal(err)
	}
	before, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpoint(ctx, id, 1, stale.State); err != nil {
		t.Fatal(err)
	}
	after, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]json.RawMessage
	raw, _ := json.Marshal(before.State)
	_ = json.Unmarshal(raw, &a)
	raw, _ = json.Marshal(after.State)
	_ = json.Unmarshal(raw, &b)
	if string(a["adaptive_research"]) != string(b["adaptive_research"]) {
		t.Fatalf("stale save overwrote adaptive authority: before=%s after=%s", a["adaptive_research"], b["adaptive_research"])
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='verification.completed'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("assessment repeat duplicated events: count=%d err=%v", count, err)
	}
}
