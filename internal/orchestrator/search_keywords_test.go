package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

const bioGoal = "сделай мне роадмап по биоинформатики и по химий с информатикой ,что я должен знать что бы стать биоинформатиком или биоинжинером, сделай иследования и дай мне этапы и время за сколько я чему научусь"

func TestConciseSearchQueryBoundsAndCleansPlannerText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Search for learning paths and required skills for bioinformatics.", "learning paths and required skills for bioinformatics"},
		{"  Найти программы по биоинженерии с полным грантом.  ", "программы по биоинженерии с полным грантом"},
		{"роадмап биоинформатика этапы обучения сроки", "роадмап биоинформатика этапы обучения сроки"},
		{"one two three four five six seven eight nine ten eleven twelve thirteen fourteen", "one two three four five six seven eight nine ten eleven twelve"},
		{"...", ""},
		{"api_key=secret bioinformatics", ""},
		{"bioinformatics‮ roadmap", ""},
	} {
		if got := conciseSearchQuery(tc.in); got != tc.want {
			t.Errorf("conciseSearchQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("биоинформатикаааааааааа ", 12)
	if got := conciseSearchQuery(long); utf8.RuneCountInString(got) > maxSearchQueryRunes {
		t.Fatalf("query exceeds rune bound: %d", utf8.RuneCountInString(got))
	}
}

func TestDeterministicSearchUsesPlannerQueryNotWholeRequest(t *testing.T) {
	plan := domain.Plan{SchemaVersion: 1, Goal: bioGoal, MaxSteps: 4, FinalDeliverable: "roadmap", Subtasks: []domain.PlanStep{
		{ID: "s1", Kind: "search", Description: "Search for learning paths and required skills for bioinformatics.", Query: "роадмап биоинформатика этапы обучения", SuccessCriteria: []string{"x"}},
		{ID: "s2", Kind: "search", Description: "Search for learning paths and required skills for bioengineering (specifically chemical/bioengineering with informatics).", SuccessCriteria: []string{"x"}},
		{ID: "f1", Kind: "fetch", Description: "fetch", Dependencies: []string{"s1", "s2"}, SuccessCriteria: []string{"x"}},
		{ID: "f2", Kind: "fetch", Description: "fetch", Dependencies: []string{"s1", "s2"}, SuccessCriteria: []string{"x"}},
	}}
	normalized := normalizeResearchPlan(plan, "Роадмап", bioGoal)
	queries := map[string]string{}
	for _, step := range normalized.Subtasks {
		if step.Kind != "search" {
			continue
		}
		decision, deterministic, err := deterministicResearchAction(step, bioGoal, "search", checkpointState{}, nil)
		if err != nil || !deterministic {
			t.Fatalf("step %s not deterministic: %v", step.ID, err)
		}
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(decision.ToolCall.Arguments, &args); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(args.Query, "сделай") || len(strings.Fields(args.Query)) > maxSearchQueryWords {
			t.Fatalf("query still carries the raw request: %q", args.Query)
		}
		queries[step.ID] = args.Query
	}
	if queries["search_1"] != "роадмап биоинформатика этапы обучения" {
		t.Fatalf("planner query not used: %#v", queries)
	}
	if queries["search_2"] != "learning paths and required skills for bioengineering specifically chemical/bioengineering with informatics" {
		t.Fatalf("facet fallback query = %q", queries["search_2"])
	}
}

func TestPinnedPlanWithoutQueryFieldStillRunsFromFacet(t *testing.T) {
	var step domain.PlanStep
	raw := `{"id":"search_1","kind":"search","description":"` + deterministicSearchFacetPrefix + `protocol data structures. User request: Research protocol architecture","dependencies":[],"expected_output":"","success_criteria":["x"]}`
	if err := json.Unmarshal([]byte(raw), &step); err != nil {
		t.Fatal(err)
	}
	decision, deterministic, err := deterministicResearchAction(step, "Research protocol architecture", "search", checkpointState{}, nil)
	if err != nil || !deterministic || !strings.Contains(string(decision.ToolCall.Arguments), `"protocol data structures"`) {
		t.Fatalf("old pinned plan: decision=%+v deterministic=%v err=%v", decision, deterministic, err)
	}
	encoded, _ := json.Marshal(step)
	if strings.Contains(string(encoded), `"query"`) {
		t.Fatalf("query field must stay omitted for old plans: %s", encoded)
	}
}
