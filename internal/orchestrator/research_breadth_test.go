package orchestrator

import (
	"context"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func breadthSessionContext(profile string, target int) context.Context {
	policy := domain.DefaultRoundResearchPolicy()
	policy.Profile, policy.FetchTarget = profile, target
	budget, _ := domain.RoundResearchBudgetForProfile(profile)
	policy.Budget = budget
	return context.WithValue(context.Background(), researchExecutionKey{}, domain.ResearchSession{Policy: policy, Budget: budget})
}

func TestResearchFetchTargetFollowsSavedProfileBreadth(t *testing.T) {
	text := "Research protocol architecture and deployment limitations"
	if minimumResearchFetches(text) != 2 {
		t.Fatalf("fixture must be a research request")
	}
	if got := researchFetchTarget(context.Background(), text); got != 2 {
		t.Fatalf("legacy target = %d, want minimum 2", got)
	}
	if got := researchFetchTarget(breadthSessionContext(domain.ResearchProfileStandard, 0), text); got != 2 {
		t.Fatalf("pre-target session = %d, want minimum 2", got)
	}
	if got := researchFetchTarget(breadthSessionContext(domain.ResearchProfileStandard, 6), text); got != 6 {
		t.Fatalf("standard target = %d, want 6", got)
	}
	if got := researchFetchTarget(breadthSessionContext(domain.ResearchProfileQuick, 2), text); got != 2 {
		t.Fatalf("quick target = %d, want 2", got)
	}
	if got := researchFetchTarget(breadthSessionContext(domain.ResearchProfileStandard, 6), "Count the words in this text: a b c"); got != 0 {
		t.Fatalf("self-contained request target = %d, want 0", got)
	}
	if domain.DefaultRoundResearchPolicy().FetchTarget != 6 || domain.ResearchFetchTargetForProfile(domain.ResearchProfileOvernight) != domain.MaxResearchFetchTarget {
		t.Fatal("profile breadth defaults changed")
	}
}

func TestExpandResearchFetchBreadthAddsDependentFetchesWithinCapacity(t *testing.T) {
	plan := domain.Plan{SchemaVersion: 1, Goal: "g", FinalDeliverable: "d", Subtasks: []domain.PlanStep{
		{ID: "search_1", Kind: "search", Description: "a", SuccessCriteria: []string{"x"}},
		{ID: "search_2", Kind: "search", Description: "b", SuccessCriteria: []string{"x"}},
		{ID: "fetch_1", Kind: "fetch", Description: "f", Dependencies: []string{"search_1", "search_2"}, SuccessCriteria: []string{"x"}},
		{ID: "fetch_2", Kind: "fetch", Description: "f", Dependencies: []string{"search_1", "search_2"}, SuccessCriteria: []string{"x"}},
	}}
	plan.MaxSteps = len(plan.Subtasks)
	expanded := expandResearchFetchBreadth(plan, 6, 12)
	if countPlanKind(expanded, "fetch") != 6 || expanded.MaxSteps != 8 || len(expanded.Subtasks) != 8 {
		t.Fatalf("expanded plan = %#v", expanded)
	}
	seen := map[string]bool{}
	for _, step := range expanded.Subtasks {
		if seen[step.ID] {
			t.Fatalf("duplicate step id %s", step.ID)
		}
		seen[step.ID] = true
		if step.Kind == "fetch" && (len(step.Dependencies) != 2 || step.Dependencies[0] != "search_1" || step.Dependencies[1] != "search_2") {
			t.Fatalf("fetch %s does not depend on every search: %v", step.ID, step.Dependencies)
		}
	}
	w := New(nil, &scriptedLLM{}, registerPlanValidationResearchTools(t), Options{MaxSteps: 12})
	if err := w.validateTaskPlanWithFetchLimit(expanded, "Research", "Research protocol architecture and deployment limitations", 6); err != nil {
		t.Fatalf("expanded plan rejected: %v", err)
	}
	if err := w.validateTaskPlan(expanded, "Research", "Research protocol architecture and deployment limitations"); err == nil {
		t.Fatal("planner fetch bound must still reject six planner-produced fetches")
	}
	if capped := expandResearchFetchBreadth(plan, 6, 5); len(capped.Subtasks) != 5 {
		t.Fatalf("capacity ignored: %d steps", len(capped.Subtasks))
	}
	if same := expandResearchFetchBreadth(plan, 2, 12); len(same.Subtasks) != 4 {
		t.Fatalf("target at minimum must not change the plan")
	}
}

func TestResearchPlanCapacityKeepsRoomForRounds(t *testing.T) {
	ctx := breadthSessionContext(domain.ResearchProfileStandard, 6)
	if got := researchPlanCapacity(ctx, 12); got != 10 { // MaxTotalSteps 20 / 2
		t.Fatalf("standard capacity = %d", got)
	}
	if got := researchPlanCapacity(context.Background(), 12); got != 12 {
		t.Fatalf("legacy capacity = %d", got)
	}
}
