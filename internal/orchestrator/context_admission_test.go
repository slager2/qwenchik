package orchestrator

import (
	"context"
	"errors"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/tools"
	"testing"
)

type contextAdmissionFixture struct{ attempts, generations int }

func (c *contextAdmissionFixture) Complete(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	c.attempts++
	if c.attempts == 1 {
		return llm.ChatResponse{}, llm.ErrContextUnverified
	}
	c.generations++
	return llm.ChatResponse{Content: "recovered"}, nil
}
func (c *contextAdmissionFixture) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	c.attempts++
	if c.attempts == 1 {
		return nil, llm.ErrContextUnverified
	}
	c.generations++
	ch := make(chan llm.ChatEvent, 1)
	ch <- llm.ChatEvent{Text: "recovered"}
	close(ch)
	return ch, nil
}

func TestContextAdmissionFailureCanRecoverWithoutReplayingDispatchedStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			saved := f.submit(t, "Research protocol", nil)
			inner := &contextAdmissionFixture{}
			request := llm.ChatRequest{Model: saved.ModelSnapshot.Model, BaseURL: saved.ModelSnapshot.BaseURL, Messages: []llm.ChatMessage{{Role: "user", Content: "immutable request"}}, MaxTokens: 24}
			client := newInvocationClient(f.store, inner, f.task.ID, nil)
			if stream {
				if _, err := client.Stream(ctx, request); !errors.Is(err, llm.ErrContextUnverified) {
					t.Fatal(err)
				}
			} else {
				if _, err := client.Complete(ctx, request); !errors.Is(err, llm.ErrContextUnverified) {
					t.Fatal(err)
				}
			}
			if inner.generations != 0 {
				t.Fatal("admission failure dispatched generation")
			}
			f.reopen(t)
			client = newInvocationClient(f.store, inner, f.task.ID, nil)
			if stream {
				events, err := client.Stream(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				for event := range events {
					if event.Err != nil {
						t.Fatal(event.Err)
					}
				}
			} else {
				if _, err := client.Complete(ctx, request); err != nil {
					t.Fatal(err)
				}
			}
			if inner.generations != 1 || inner.attempts != 2 {
				t.Fatalf("retry accounting: %+v", inner)
			}
			session := researchRunSession(t, f.store, f.task.ID)
			if session.Counters.LLMCalls != 2 || !session.DeadlineAt.Equal(saved.DeadlineAt) {
				t.Fatal("retry refunded reservation or altered deadline")
			}
			if stream {
				if _, err := client.Stream(ctx, request); !errors.Is(err, ErrRecoveryRequired) {
					t.Fatalf("dispatched completed stream blindly replayed: %v", err)
				}
			}
		})
	}
}

func TestFreshContextMismatchPausesWithCheckpointBeforeInference(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	saved := f.submit(t, "Research protocol", nil)
	calls := 0
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) {
		calls++
		return "", errors.New("must not dispatch")
	}}
	w := New(f.store, model, tools.NewRegistry(), Options{HoldResourcePauses: true, ContextProbe: func(context.Context, domain.ModelProfileSnapshot) (domain.ContextBudget, error) {
		return domain.ContextBudget{}, llm.ErrContextMismatch
	}})
	w.runTask(ctx, f.task)
	task, err := f.store.GetTask(ctx, f.task.ID)
	if err != nil || task.Status != domain.StatusPaused || task.ResourceReason != "context_mismatch" || task.PauseReason != "manual" {
		t.Fatalf("mismatch not safely held: %+v %v", task, err)
	}
	if _, err := f.store.LatestCheckpoint(ctx, f.task.ID); err != nil {
		t.Fatal("checkpoint lost", err)
	}
	session := researchRunSession(t, f.store, f.task.ID)
	if calls != 0 || session.Counters.LLMCalls != 0 || session.ModelSnapshot.ContextLength != saved.ModelSnapshot.ContextLength {
		t.Fatal("mismatch dispatched or rewrote snapshot")
	}
}
