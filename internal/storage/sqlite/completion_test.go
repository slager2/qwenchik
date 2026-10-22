package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestContinuationAtomicRollbackCancellationAndCeiling(t *testing.T) {
	for _, boundary := range []string{"event", "checkpoint", "cancel", "success"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			id := turn.Run.ID
			plan := domain.Plan{SchemaVersion: 1, Goal: "Inspect source", FinalDeliverable: "Source report", MaxSteps: 1, Subtasks: []domain.PlanStep{{ID: "original", Kind: "search", Description: "Inspect source", SuccessCriteria: []string{"Source found"}}}}
			if _, err := s.SavePlan(ctx, id, plan, map[string]any{"completed_steps": []string{}, "summary": "retained"}); err != nil {
				t.Fatal(err)
			}
			if boundary == "event" {
				if _, err := s.db.Exec(`CREATE TRIGGER reject_continuation BEFORE INSERT ON events WHEN NEW.type='continuation.requested' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "checkpoint" {
				if _, err := s.db.Exec(`CREATE TRIGGER reject_continuation BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "cancel" {
				if _, err := s.TransitionTask(ctx, id, domain.StatusCancelled, nil, "task.cancelled", "user", nil); err != nil {
					t.Fatal(err)
				}
			}
			err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), []string{"missing"})
			var count, steps int
			s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='continuation.requested'`, id).Scan(&count)
			s.db.QueryRow(`SELECT COUNT(*) FROM task_steps WHERE task_id=?`, id).Scan(&steps)
			if boundary != "success" {
				if err == nil || count != 0 || steps != 1 {
					t.Fatalf("rollback: %v events=%d steps=%d", err, count, steps)
				}
				return
			}
			if err != nil || count != 1 || steps != 3 {
				t.Fatalf("commit: %v events=%d steps=%d", err, count, steps)
			}
			if err := s.RequestResearchContinuation(ctx, id, 1, strings.Repeat("c", 64), strings.Repeat("d", 64), nil); !errors.Is(err, ErrConflict) {
				t.Fatalf("ceiling escaped: %v", err)
			}
			checkpoint, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(checkpoint.State)
			if !strings.Contains(string(raw), `"continuations":1`) || !strings.Contains(string(raw), `"summary":"retained"`) {
				t.Fatalf("state=%s", raw)
			}
		})
	}
}

func TestLocalReviewCeilingPersistsBeforeInference(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	for index, role := range []string{"verifier", "verifier", "critic_0"} {
		invocation := invocationFixture(turn.Run.ID, "llm.complete", 1)
		invocation.Role = role
		invocation.OperationID = role + string(rune('a'+index))
		invocation.IdempotencyKey = invocation.OperationID
		if _, err := s.BeginInvocation(ctx, invocation); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	invocation := invocationFixture(turn.Run.ID, "llm.complete", 1)
	invocation.Role = "critic_1"
	invocation.OperationID = "fourth-review"
	invocation.IdempotencyKey = "fourth-review"
	if _, err := reopened.BeginInvocation(ctx, invocation); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("fourth review escaped: %v", err)
	}
	session, err := reopened.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || session.Counters.LLMCalls != 3 {
		t.Fatalf("unexpected charge: %+v %v", session.Counters, err)
	}
}
