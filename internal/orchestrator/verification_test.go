package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			encoded, _ := json.Marshal(map[string]any{"ready_to_answer": true, "summary": "Supported", "missing": []string{}, "supplementary_criteria": []string{}, "criterion_assessments": assessments})
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
	if _, err := addressedVerification(verificationResponse{Complete: true, Supplementary: []string{"c3"}, Assessments: map[string]criterionVerdict{"c0": {Met: true}, "c1": {Met: true}, "c2": {Met: true}}}, catalog); !errors.Is(err, errInvalidModelOutput) {
		t.Fatalf("unknown supplementary criterion accepted: %v", err)
	}
}

func TestSupplementaryGapsArePartialButEvidenceIntegrityRemainsCritical(t *testing.T) {
	plan := domain.Plan{Subtasks: []domain.PlanStep{{ID: "fetch_1", Kind: "fetch", SuccessCriteria: []string{"Inspect requested source", "Report supplementary publication details"}}}}
	state := checkpointState{CompletedSteps: []string{"fetch_1"}, ToolResults: map[string]any{"fetch_1": map[string]any{"evidence_id": "owned"}}}
	defaultCritical, err := addressedVerification(verificationResponse{Complete: true, Assessments: map[string]criterionVerdict{
		"c0": {Met: false}, "c1": {Met: true, EvidenceIDs: []string{"owned"}},
	}}, verificationCatalog(plan, state))
	if err != nil {
		t.Fatal(err)
	}
	critical, supplementary := verificationCriterionFindings(plan, state, defaultCritical.CriterionAssessments)
	if len(critical) != 1 || len(supplementary) != 0 {
		t.Fatalf("unclassified criteria were not fail-closed: critical=%#v supplementary=%#v", critical, supplementary)
	}
	critical, supplementary = verificationCriterionFindings(plan, state, []criterionAssessment{
		{StepID: "fetch_1", Criterion: 0, Classification: "critical", Met: true, EvidenceIDs: []string{"owned"}},
		{StepID: "fetch_1", Criterion: 1, Classification: "supplementary", Met: false},
	})
	if len(critical) != 0 || len(supplementary) != 1 || !strings.Contains(supplementary[0], "Report supplementary publication details") {
		t.Fatalf("supplementary gap classification: critical=%#v supplementary=%#v", critical, supplementary)
	}
	critical, supplementary = verificationCriterionFindings(plan, state, []criterionAssessment{
		{StepID: "fetch_1", Criterion: 0, Classification: "supplementary", Met: true, EvidenceIDs: []string{"foreign"}},
		{StepID: "fetch_1", Criterion: 1, Classification: "supplementary", Met: false},
	})
	if len(critical) == 0 || !strings.Contains(critical[0], "not returned by its step") || len(supplementary) != 1 {
		t.Fatalf("supplementary classification weakened evidence ownership: critical=%#v supplementary=%#v", critical, supplementary)
	}
}

func TestSupplementaryClassificationCannotHideUnsuitableEvidence(t *testing.T) {
	assessments, missing := filterVerificationEvidence([]criterionAssessment{{
		StepID: "fetch_1", Criterion: 0, Classification: "supplementary", Met: true,
		EvidenceIDs: []string{"owned-good", "owned-flagged", "missing"},
	}}, []domain.Evidence{
		{ID: "owned-good", HTTPStatus: 200, Excerpt: "usable excerpt"},
		{ID: "owned-flagged", HTTPStatus: 200, Excerpt: "flagged excerpt", QualityFlags: []string{"low_quality"}},
	})
	if len(assessments) != 1 || len(assessments[0].EvidenceIDs) != 1 || assessments[0].EvidenceIDs[0] != "owned-good" || len(missing) != 1 || !strings.Contains(missing[0], "unavailable or unsuitable") {
		t.Fatalf("unsuitable evidence was not failed closed: assessments=%#v missing=%#v", assessments, missing)
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

func TestResearchNormalizerPreservesUniqueUserGroundedSearchFacets(t *testing.T) {
	goal := "Research the protocol architecture and interoperability limitations"
	facets := []string{
		"Compare protocol architecture, message structures, and execution flow",
		"Find protocol interoperability limitations and compatibility risks",
	}
	plan := domain.Plan{SchemaVersion: 1, Goal: goal, MaxSteps: 4, FinalDeliverable: "Supported protocol summary", Subtasks: []domain.PlanStep{
		{ID: "search_architecture", Kind: "search", Description: facets[0], Dependencies: []string{}, ExpectedOutput: "architecture results", SuccessCriteria: []string{"results recorded"}},
		{ID: "search_compatibility", Kind: "search", Description: facets[1], Dependencies: []string{}, ExpectedOutput: "compatibility results", SuccessCriteria: []string{"results recorded"}},
		{ID: "fetch_architecture", Kind: "fetch", Description: "Inspect an architecture source", Dependencies: []string{"search_architecture", "search_compatibility"}, ExpectedOutput: "first evidence", SuccessCriteria: []string{"evidence stored"}},
		{ID: "fetch_compatibility", Kind: "fetch", Description: "Inspect an independent source", Dependencies: []string{"search_architecture", "search_compatibility"}, ExpectedOutput: "second evidence", SuccessCriteria: []string{"evidence stored"}},
	}}
	normalized := normalizeResearchPlan(plan, "Protocol research", goal)
	searchDescriptions := []string{}
	for _, step := range normalized.Subtasks {
		if step.Kind != "search" {
			continue
		}
		searchDescriptions = append(searchDescriptions, step.Description)
		if !strings.Contains(step.Description, goal) {
			t.Fatalf("user request missing from normalized facet: %q", step.Description)
		}
	}
	if len(searchDescriptions) != 2 || !strings.Contains(searchDescriptions[0], facets[0]) || !strings.Contains(searchDescriptions[1], facets[1]) {
		t.Fatalf("unique search facets were not preserved: %#v", searchDescriptions)
	}
	worker := New(nil, nil, registerPlanValidationResearchTools(t), Options{})
	if err := worker.validateTaskPlan(normalized, "Protocol research", goal); err != nil {
		t.Fatalf("preserved facets failed plan validation: %v", err)
	}
	model := &scriptedLLM{responses: []string{
		`{"status":"continue","summary":"Search architecture facet","tool_call":{"name":"search","arguments":{"query":"protocol architecture data structures"}}}`,
		`{"status":"continue","summary":"Search compatibility facet","tool_call":{"name":"search","arguments":{"query":"protocol interoperability compatibility risks"}}}`,
	}}
	worker = New(nil, model, registerPlanValidationResearchTools(t), Options{})
	searchIndex := 0
	for _, step := range normalized.Subtasks {
		if step.Kind != "search" {
			continue
		}
		if _, err := worker.chooseAction(context.Background(), domain.Task{ID: "facet-input"}, goal, "", step, "researcher", "search", "", checkpointState{}, nil); err != nil {
			t.Fatalf("actor rejected preserved facet %q: %v", step.Description, err)
		}
		var input struct {
			Step string `json:"step"`
		}
		if err := json.Unmarshal([]byte(model.seen[searchIndex].Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(input.Step, facets[searchIndex]) {
			t.Fatalf("actor input lost facet %q: %q", facets[searchIndex], input.Step)
		}
		searchIndex++
	}
}

func TestResearchNormalizerDoesNotManufactureDistinctDuplicateFacets(t *testing.T) {
	goal := "Research the protocol architecture and limitations"
	plan := domain.Plan{SchemaVersion: 1, Goal: goal, MaxSteps: 4, FinalDeliverable: "Supported protocol summary", Subtasks: []domain.PlanStep{
		{ID: "search_1", Kind: "search", Description: "Search protocol architecture and limitations", Dependencies: []string{}, ExpectedOutput: "first results", SuccessCriteria: []string{"results recorded"}},
		{ID: "search_2", Kind: "search", Description: "Search protocol architecture and limitations", Dependencies: []string{}, ExpectedOutput: "second results", SuccessCriteria: []string{"results recorded"}},
		{ID: "fetch_1", Kind: "fetch", Description: "Inspect a source", Dependencies: []string{"search_1", "search_2"}, ExpectedOutput: "first evidence", SuccessCriteria: []string{"evidence stored"}},
		{ID: "fetch_2", Kind: "fetch", Description: "Inspect another source", Dependencies: []string{"search_1", "search_2"}, ExpectedOutput: "second evidence", SuccessCriteria: []string{"evidence stored"}},
	}}
	normalized := normalizeResearchPlan(plan, "Protocol research", goal)
	if normalizeResearchText(normalized.Subtasks[0].Description) != normalizeResearchText(normalized.Subtasks[1].Description) {
		t.Fatalf("duplicate facets were rewritten to appear unique: %#v", normalized.Subtasks[:2])
	}
	if err := validateDistinctSearchQuestions(normalized.Subtasks); !errors.Is(err, errResearchPlanIncomplete) {
		t.Fatalf("duplicate search facets were accepted: %v", err)
	}
}

func TestResearchNormalizerNeverSilentlyDropsOverLimitFacets(t *testing.T) {
	goal := "Research protocol architecture, compatibility, implementation, and limitations"
	for _, includeAnalysis := range []bool{false, true} {
		plan := domain.Plan{SchemaVersion: 1, Goal: goal, MaxSteps: 8, FinalDeliverable: "Supported protocol summary"}
		for index, facet := range []string{"architecture", "compatibility", "implementation", "limitations"} {
			plan.Subtasks = append(plan.Subtasks, domain.PlanStep{ID: fmt.Sprintf("search_%d", index+1), Kind: "search", Description: "Research protocol " + facet, Dependencies: []string{}, ExpectedOutput: "results", SuccessCriteria: []string{"results recorded"}})
		}
		plan.Subtasks = append(plan.Subtasks,
			domain.PlanStep{ID: "fetch_1", Kind: "fetch", Description: "Inspect source one", Dependencies: []string{}, ExpectedOutput: "evidence", SuccessCriteria: []string{"evidence stored"}},
			domain.PlanStep{ID: "fetch_2", Kind: "fetch", Description: "Inspect source two", Dependencies: []string{}, ExpectedOutput: "evidence", SuccessCriteria: []string{"evidence stored"}},
		)
		if includeAnalysis {
			plan.Subtasks = append(plan.Subtasks, domain.PlanStep{ID: "analysis", Kind: "analysis", Description: "Inspect final result", Dependencies: []string{}, ExpectedOutput: "summary", SuccessCriteria: []string{"summary recorded"}})
		}
		before := append([]domain.PlanStep(nil), plan.Subtasks...)
		normalized := normalizeResearchPlan(plan, "Protocol research", goal)
		if len(normalized.Subtasks) != len(before) {
			t.Fatalf("over-limit plan silently dropped steps: got %d of %d, analysis=%t", len(normalized.Subtasks), len(before), includeAnalysis)
		}
		searchCount := 0
		for _, step := range normalized.Subtasks {
			if step.Kind == "search" {
				searchCount++
			}
		}
		if searchCount != 4 {
			t.Fatalf("over-limit facet count changed to %d, analysis=%t", searchCount, includeAnalysis)
		}
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
