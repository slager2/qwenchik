package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func searchBreadthContext(profile string) context.Context {
	policy := domain.DefaultRoundResearchPolicy()
	budget, _ := domain.RoundResearchBudgetForProfile(profile)
	policy.Profile, policy.Budget = profile, budget
	policy.SearchTarget = min(domain.ResearchSearchTargetForProfile(profile), budget.MaxSearchQueries/2)
	return context.WithValue(context.Background(), researchExecutionKey{}, domain.ResearchSession{Policy: policy, Budget: budget})
}

func TestResearchSearchTargetFollowsSavedProfileBreadth(t *testing.T) {
	text := "Research protocol architecture and deployment limitations"
	for _, tc := range []struct {
		ctx  context.Context
		want int
	}{
		{context.Background(), 2},
		{breadthSessionContext(domain.ResearchProfileStandard, 6), 3}, // default round policy
		{searchBreadthContext(domain.ResearchProfileQuick), 2},
		{searchBreadthContext(domain.ResearchProfileStandard), 3},
		{searchBreadthContext(domain.ResearchProfileDeepLaptop), 4},
		{searchBreadthContext(domain.ResearchProfileLongLaptop), 5},
		{searchBreadthContext(domain.ResearchProfileOvernight), 5},
	} {
		if got := researchSearchTarget(tc.ctx, text); got != tc.want {
			t.Fatalf("search target = %d, want %d", got, tc.want)
		}
	}
	legacy := domain.DefaultRoundResearchPolicy()
	legacy.SearchTarget = 0
	if got := researchSearchTarget(context.WithValue(context.Background(), researchExecutionKey{}, domain.ResearchSession{Policy: legacy, Budget: legacy.Budget}), text); got != 2 {
		t.Fatalf("pre-target session = %d, want minimum 2", got)
	}
	if got := researchSearchTarget(searchBreadthContext(domain.ResearchProfileOvernight), "Count the words in this text: a b c"); got != 0 {
		t.Fatalf("self-contained request target = %d", got)
	}
}

func TestWidenResearchSearchFacetsAddsDistinctKeywordFacets(t *testing.T) {
	goal := "Research protocol architecture and deployment limitations"
	plan := normalizeResearchPlan(domain.Plan{SchemaVersion: 1, Goal: goal, FinalDeliverable: "d", Subtasks: []domain.PlanStep{
		{ID: "s1", Kind: "search", Description: "Protocol architecture", Query: "protocol architecture specification", SuccessCriteria: []string{"x"}},
		{ID: "s2", Kind: "search", Description: "Deployment limitations", Query: "protocol deployment limitations", SuccessCriteria: []string{"x"}},
		{ID: "f1", Kind: "fetch", Description: "f", Dependencies: []string{"s1", "s2"}, SuccessCriteria: []string{"x"}},
		{ID: "f2", Kind: "fetch", Description: "f", Dependencies: []string{"s1", "s2"}, SuccessCriteria: []string{"x"}},
	}}, "Research", goal)
	widened := widenResearchSearchFacets(plan, goal, 5, 12)
	if countPlanKind(widened, "search") != 5 || len(widened.Subtasks) != 7 || widened.MaxSteps != 7 {
		t.Fatalf("widened plan = %#v", widened)
	}
	queries := []string{}
	searchIDs := []string{}
	for index, step := range widened.Subtasks {
		if step.Kind == "search" {
			if index >= 5 {
				t.Fatal("added search facets must precede fetch steps")
			}
			if duplicateResearchQuery(step.Query, queries) || len(strings.Fields(step.Query)) > maxSearchQueryWords {
				t.Fatalf("query %q is not a distinct concise query (%v)", step.Query, queries)
			}
			queries, searchIDs = append(queries, step.Query), append(searchIDs, step.ID)
		}
	}
	if !strings.Contains(queries[2], "protocol architecture deployment limitations") {
		t.Fatalf("added facet does not search the request keywords: %q", queries[2])
	}
	for _, step := range widened.Subtasks {
		if step.Kind == "fetch" && len(step.Dependencies) != len(searchIDs) {
			t.Fatalf("fetch %s depends on %v, want all of %v", step.ID, step.Dependencies, searchIDs)
		}
	}
	w := New(nil, &scriptedLLM{}, registerPlanValidationResearchTools(t), Options{MaxSteps: 12})
	if err := w.validateTaskPlanWithFetchLimit(widened, "Research", goal, maxResearchFetches); err != nil {
		t.Fatalf("widened plan rejected: %v", err)
	}
	if err := w.validateTaskPlan(widened, "Research", goal); err == nil {
		t.Fatal("planner search bound must still reject five planner-produced searches")
	}
	decision, ok, err := deterministicResearchAction(widened.Subtasks[2], goal, "search", checkpointState{}, nil)
	if err != nil || !ok || !strings.Contains(string(decision.ToolCall.Arguments), queries[2]) {
		t.Fatalf("added facet is not searched deterministically: %s ok=%v err=%v", decision.ToolCall.Arguments, ok, err)
	}
	if capped := widenResearchSearchFacets(plan, goal, 5, 5); len(capped.Subtasks) != 5 {
		t.Fatalf("capacity ignored: %d steps", len(capped.Subtasks))
	}
	if same := widenResearchSearchFacets(plan, goal, 2, 12); len(same.Subtasks) != len(plan.Subtasks) {
		t.Fatal("target at the current search count must not change the plan")
	}
}

func TestDeterministicSearchUsesStepQueryForMultilineGoals(t *testing.T) {
	goal := "Original task:\nResearch protocol architecture\n\nUser update:\nFocus on deployment limitations"
	plan := normalizeResearchPlan(domain.Plan{SchemaVersion: 1, Goal: goal, FinalDeliverable: "d", Subtasks: []domain.PlanStep{
		{ID: "s1", Kind: "search", Description: "Protocol architecture", Query: "protocol architecture specification", SuccessCriteria: []string{"x"}},
		{ID: "s2", Kind: "search", Description: "Deployment limitations", Query: "protocol deployment limitations", SuccessCriteria: []string{"x"}},
		{ID: "f1", Kind: "fetch", Description: "f", Dependencies: []string{"s1", "s2"}, SuccessCriteria: []string{"x"}},
		{ID: "f2", Kind: "fetch", Description: "f", Dependencies: []string{"s1", "s2"}, SuccessCriteria: []string{"x"}},
	}}, "Research", goal)
	decision, ok, err := deterministicResearchAction(plan.Subtasks[0], goal, "search", checkpointState{}, nil)
	if err != nil || !ok || !strings.Contains(string(decision.ToolCall.Arguments), "protocol architecture specification") {
		t.Fatalf("multi-line goal lost the step query: %s ok=%v err=%v", decision.ToolCall.Arguments, ok, err)
	}
}

func TestGoalKeywordQueryDropsRequestPhrasing(t *testing.T) {
	for goal, want := range map[string]string{
		"Составь, пожалуйста, роадмап по биоинформатике для новичка: что учить и в каком порядке?": "роадмап биоинформатике новичка учить порядке",
		"Original task:\nWhat are the best ways to learn bioinformatics? https://example.org/x":    "best ways learn bioinformatics",
		"и в на": "",
	} {
		if got := goalKeywordQuery(goal, 8); got != want {
			t.Fatalf("goalKeywordQuery(%q) = %q, want %q", goal, got, want)
		}
	}
}
