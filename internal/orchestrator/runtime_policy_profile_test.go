package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/tools"
)

// Regression: deep/long/overnight sessions failed at once with
// research_runtime_changed because their profile-derived breadth targets
// differ from the server's default (standard) policy.
func TestResearchExecutorAcceptsProfileBreadthTargets(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Гранты на обучение IT за границей", func(session *domain.ResearchSession) {
		session.Policy.FetchTarget, session.Policy.SearchTarget = 9, 5
	})
	saved := researchRunSession(t, f.store, f.task.ID)
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) { return "OK", nil }}
	current := saved.Policy
	current.Profile, current.FetchTarget, current.SearchTarget = domain.ResearchProfileStandard, 6, 3
	w := New(f.store, model, tools.NewRegistry(), Options{RuntimeResearchPolicy: &current})
	if _, _, err := w.researchExecutor(ctx, f.task.ID); errors.Is(err, errResearchRuntimeChanged) {
		t.Fatalf("profile breadth targets rejected as a runtime change: %v", err)
	}
	changed := current
	changed.PrivacyMode = domain.PrivacyOffline
	w = New(f.store, model, tools.NewRegistry(), Options{RuntimeResearchPolicy: &changed})
	if _, _, err := w.researchExecutor(ctx, f.task.ID); !errors.Is(err, errResearchRuntimeChanged) {
		t.Fatalf("privacy change accepted: %v", err)
	}
}
