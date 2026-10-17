package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestFixedCriterionAddressesPreventMissingOrInventedStepAssessments(t *testing.T) {
	plan := domain.Plan{Subtasks: []domain.PlanStep{
		{ID: "search_astana_libraries_official", Kind: "search", SuccessCriteria: []string{"results recorded"}},
		{ID: "fetch_culture_education_site", Kind: "fetch", SuccessCriteria: []string{"page inspected", "limitations disclosed"}},
	}}
	catalog := verificationCatalog(plan, checkpointState{ToolResults: map[string]any{"fetch_culture_education_site": map[string]any{"evidence_id": "evidence-1"}}})
	if len(catalog) != 3 || catalog[2].ID != "c2" || catalog[2].StepID != "fetch_culture_education_site" || catalog[2].Index != 1 {
		t.Fatalf("criterion catalog = %#v", catalog)
	}
	schema := verificationOutputSchema(catalog)
	verdict := map[string]any{"met": true, "evidence_ids": []string{"evidence-1"}, "rationale": "Recorded source supports the criterion."}
	for _, test := range []struct {
		name  string
		keys  []string
		valid bool
	}{
		{name: "complete coverage", keys: []string{"c0", "c1", "c2"}, valid: true},
		{name: "omitted search", keys: []string{"c1", "c2"}},
		{name: "unknown index", keys: []string{"c0", "c1", "c3"}},
		{name: "invented step", keys: []string{"c0", "c1", "fetch_0"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assessments := make(map[string]any)
			for _, key := range test.keys {
				if key == "c0" {
					assessments[key] = map[string]any{"met": true, "evidence_ids": []string{}, "rationale": "Search results support the criterion."}
				} else {
					assessments[key] = verdict
				}
			}
			encoded, _ := json.Marshal(map[string]any{"ready_to_answer": true, "summary": "Supported", "missing": []string{}, "criterion_assessments": assessments})
			err := tools.ValidateJSONSchema(schema, encoded)
			if (err == nil) != test.valid {
				t.Fatalf("assessment validation = %v", err)
			}
		})
	}
	result, err := addressedVerification(verificationResponse{Assessments: map[string]criterionVerdict{
		"c0": {Met: true}, "c1": {Met: true}, "c2": {Met: false, Rationale: "No source support"},
	}}, catalog)
	if err != nil || len(result.CriterionAssessments) != 3 || result.CriterionAssessments[2].StepID != "fetch_culture_education_site" || result.CriterionAssessments[2].Criterion != 1 || result.CriterionAssessments[2].Met {
		t.Fatalf("backend addressing changed meaning: %#v err=%v", result, err)
	}
}

func TestResearchPlanCannotInventExtraUserAcceptanceRequirements(t *testing.T) {
	plan := domain.Plan{SchemaVersion: 1, Goal: "Libraries", MaxSteps: 3, FinalDeliverable: "Library names and sources", Subtasks: []domain.PlanStep{
		{ID: "search_1", Kind: "search", Description: "Discover public libraries", SuccessCriteria: []string{"Find exactly three university libraries with official schedules"}},
		{ID: "search_2", Kind: "search", Description: "Find independent library information", SuccessCriteria: []string{"Prove phone numbers from snippets"}},
		{ID: "fetch_1", Kind: "fetch", Description: "Inspect a discovered source", SuccessCriteria: []string{"Confirm all opening hours"}},
	}}
	normalized := normalizeResearchPlan(plan, "Библиотеки", "Найди библиотеки в Астане. Непроверенные часы не придумывай.")
	for _, step := range normalized.Subtasks {
		if len(step.SuccessCriteria) != 1 || step.SuccessCriteria[0] == plan.Subtasks[0].SuccessCriteria[0] || step.SuccessCriteria[0] == plan.Subtasks[1].SuccessCriteria[0] || step.SuccessCriteria[0] == plan.Subtasks[2].SuccessCriteria[0] {
			t.Fatalf("planner invented a mandatory user requirement: %#v", normalized)
		}
	}
}

func TestResearchNormalizerAddsMissingReadActionsAndRepairsStepBudget(t *testing.T) {
	plan := domain.Plan{SchemaVersion: 1, Goal: "Research a topic", MaxSteps: 1, FinalDeliverable: "Supported comparison", Subtasks: []domain.PlanStep{
		{ID: "overview", Kind: "search", Description: "Find overview sources", SuccessCriteria: []string{"Results recorded"}},
		{ID: "inspect", Kind: "fetch", Description: "Inspect a discovery source", SuccessCriteria: []string{"Evidence stored"}},
	}}
	normalized := normalizeResearchPlan(plan, "Research", "Исследуй протокол и сравни источники")
	if len(normalized.Subtasks) != 4 || normalized.MaxSteps != 4 || countPlanKind(normalized, "search") != 2 || countPlanKind(normalized, "fetch") != 2 {
		t.Fatalf("small-model read plan was not normalized: %#v", normalized)
	}
	worker := New(nil, nil, registerPlanValidationResearchTools(t), Options{})
	if err := worker.validateTaskPlan(normalized, "Research", "Исследуй протокол и сравни источники"); err != nil {
		t.Fatal(err)
	}
	plan.Subtasks[0].Dependencies = []string{"inspect"}
	plan.Subtasks[1].Dependencies = []string{"overview"}
	invalid := normalizeResearchPlan(plan, "Research", "Исследуй протокол")
	if domain.ValidatePlan(invalid, 12) == nil {
		t.Fatal("normalization silently accepted a cycle")
	}
}

func TestResearchFacetsCannotSilentlyChangeTheUserSubject(t *testing.T) {
	goal := "библиотеки в Астане"
	plan := domain.Plan{SchemaVersion: 1, Goal: "Libraries", MaxSteps: 2, FinalDeliverable: "Sources", Subtasks: []domain.PlanStep{
		{ID: "software_libraries", Kind: "search", Description: "Find software libraries for developers", SuccessCriteria: []string{"Leads found"}},
		{ID: "it_courses", Kind: "fetch", Description: "Inspect unrelated programming courses", SuccessCriteria: []string{"Source fetched"}},
	}}
	normalized := normalizeResearchPlan(plan, "Libraries", goal)
	for _, step := range normalized.Subtasks {
		if !strings.Contains(step.Description, goal) || strings.Contains(step.Description, "software libraries") || strings.Contains(step.Description, "programming courses") {
			t.Fatalf("model changed the requested entity: %#v", step)
		}
	}
}
