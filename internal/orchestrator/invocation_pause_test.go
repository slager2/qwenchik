package orchestrator

import (
	"context"
	"errors"
	"testing"
)

// Regression: two thermal pauses during one planner call failed a live run
// with failed_recovery_required although the model never failed.
func TestInvocationResourcePausesDoNotUseTheAttemptBudget(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research recovery", nil)
	request := invocationFixtureRequest()
	hash, err := modelInvocationHash(request)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		inv, cached, err := prepareInvocation(ctx, f.store, f.task.ID, "llm.complete", "model", "", hash)
		if err != nil || cached || inv.Attempt != attempt {
			t.Fatalf("attempt %d: %+v cached=%v err=%v", attempt, inv, cached, err)
		}
		if err := finishInvocation(ctx, f.store, inv, nil, errResourcePause); err != nil && !errors.Is(err, errResourcePause) {
			t.Fatalf("finish paused attempt: %v", err)
		}
	}
	inv, _, err := prepareInvocation(ctx, f.store, f.task.ID, "llm.complete", "model", "", hash)
	if err != nil || inv.Attempt != 4 {
		t.Fatalf("attempt after three pauses: %+v %v", inv, err)
	}
}
