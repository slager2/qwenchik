package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

func semanticTestPlan(facets ...string) domain.Plan {
	p := domain.Plan{SchemaVersion: 1, Goal: "Research protocol", FinalDeliverable: "Supported answer", MaxSteps: len(facets)}
	for i, facet := range facets {
		p.Subtasks = append(p.Subtasks, domain.PlanStep{ID: fmt.Sprintf("original_%d", i), Kind: "search", Description: facet, Dependencies: []string{}, ExpectedOutput: "leads", SuccessCriteria: []string{"results recorded"}})
	}
	return p
}

func semanticPlanJSON(t *testing.T, p domain.Plan) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertSemanticResearchPlan(t *testing.T, w *Worker, plan domain.Plan, goal string, searches int) {
	t.Helper()
	if err := w.validateTaskPlan(plan, "Research protocol", goal); err != nil {
		t.Fatal(err)
	}
	if plan.MaxSteps != len(plan.Subtasks) || countPlanKind(plan, "search") != searches || countPlanKind(plan, "fetch") < 2 {
		t.Fatalf("incorrect normalized counts: %#v", plan)
	}
	ids := []string{}
	for _, step := range plan.Subtasks {
		if step.Kind == "search" {
			ids = append(ids, step.ID)
			if len(step.Dependencies) != 0 {
				t.Fatalf("dependent search: %#v", step)
			}
		}
	}
	for i, step := range plan.Subtasks {
		if step.Kind != "fetch" {
			continue
		}
		if i == 0 && hasUserURL(goal) {
			if len(step.Dependencies) != 0 {
				t.Fatal("user URL fetch has dependencies")
			}
		} else if !reflect.DeepEqual(step.Dependencies, ids) {
			t.Fatalf("fetch dependencies=%v, want %v", step.Dependencies, ids)
		}
	}
}

func TestPlannerSemanticFourSearchesZeroFetchLosslessDeterministic(t *testing.T) {
	facets := []string{"Protocol architecture — точная формулировка", "Compatibility\nincluding all versions", "Implementation and licensing", "Limitations: preserve punctuation!?"}
	original := semanticTestPlan(facets...)
	original.MaxSteps = 12
	for _, goal := range []string{"Research protocol architecture", "Research protocol and compare https://example.org/spec with independent sources"} {
		model := &scriptedLLM{responses: []string{semanticPlanJSON(t, original)}}
		w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
		plan, err := w.createPlan(context.Background(), domain.Task{Title: "Research protocol"}, goal, "")
		if err != nil || model.requests != 1 {
			t.Fatalf("deterministic repair: calls=%d err=%v", model.requests, err)
		}
		assertSemanticResearchPlan(t, w, plan, goal, 3)
		if len(plan.Subtasks) != 5 || (hasUserURL(goal) && plan.Subtasks[0].Kind != "fetch") {
			t.Fatalf("incorrect ordering or size: %#v", plan)
		}
		again, err := normalizeResearchPlanWithinLimit(original, "Research protocol", goal, 12)
		if err != nil || !reflect.DeepEqual(plan, again) {
			t.Fatalf("non-deterministic normalization: %v", err)
		}
		again, err = normalizeResearchPlanWithinLimit(plan, "Research protocol", goal, 12)
		if err != nil || !reflect.DeepEqual(plan, again) {
			t.Fatalf("normalization is not idempotent: %v", err)
		}
		for _, facet := range facets {
			found := 0
			for _, step := range plan.Subtasks {
				if step.Kind == "search" {
					found += strings.Count(step.Description, facet)
				}
			}
			if found != 1 {
				t.Fatalf("original facet must appear exactly once: %q (count %d)", facet, found)
			}
		}
	}
}

func TestPlannerSemanticLongNonAdjacentFacetsAndDependencyRemapping(t *testing.T) {
	facets := []string{strings.Repeat("a", 100), strings.Repeat("b", 900), strings.Repeat("c", 900), strings.Repeat("d", 100)}
	p := semanticTestPlan(facets...)
	p.Subtasks = append(p.Subtasks, domain.PlanStep{ID: "search_1", Kind: "code_execution", Description: "Process results", Dependencies: []string{"original_0", "original_3", "original_2"}, SuccessCriteria: []string{"processed"}})
	p.MaxSteps = 12
	plan, err := normalizeResearchPlanWithinLimit(p, "Research", "Research protocol", 12)
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.ValidatePlan(plan, 12); err != nil {
		t.Fatal(err)
	}
	if plan.MaxSteps != 6 || plan.Subtasks[0].ID != "search_2" || len(plan.Subtasks[5].Dependencies) != 2 {
		t.Fatalf("IDs/dependencies were not consistently remapped: %#v", plan)
	}
	for _, facet := range facets {
		found := false
		for _, step := range plan.Subtasks {
			found = found || (step.Kind == "search" && strings.Contains(step.Description, facet))
			if len(step.Description) > 1000 {
				t.Fatal("description overflow")
			}
		}
		if !found {
			t.Fatal("long facet was truncated")
		}
	}
}

func TestPlannerSemanticMergeUsesAvailableStepCapacity(t *testing.T) {
	p := semanticTestPlan("Protocol architecture", "Protocol limitations", "Protocol implementation", "Protocol compatibility")
	model := &scriptedLLM{responses: []string{semanticPlanJSON(t, p)}}
	w := New(nil, model, registerPlanValidationResearchTools(t), Options{MaxSteps: 4})
	plan, err := w.createPlan(context.Background(), domain.Task{Title: "Research"}, "Research protocol", "")
	if err != nil || model.requests != 1 {
		t.Fatalf("lossless four-step plan incorrectly rejected: %v calls=%d", err, model.requests)
	}
	assertSemanticResearchPlan(t, w, plan, "Research protocol", 2)
	if plan.MaxSteps != 4 {
		t.Fatalf("step capacity ignored: %d", plan.MaxSteps)
	}
	for _, original := range p.Subtasks {
		found := false
		for _, step := range plan.Subtasks {
			found = found || (step.Kind == "search" && strings.Contains(step.Description, original.Description))
		}
		if !found {
			t.Fatal("step-budget merge lost an original facet")
		}
	}
}

func TestPlannerSemanticCorrectionBoundedSafeAndComplete(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		facets []string
		limit  int
	}{
		{"step capacity", []string{strings.Repeat("private_alpha ", 45), strings.Repeat("private_beta ", 50), strings.Repeat("private_gamma ", 45), "private_delta"}, 4},
		{"text capacity", []string{strings.Repeat("a", 600), strings.Repeat("b", 600), strings.Repeat("c", 600), strings.Repeat("d", 600)}, 12},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := semanticTestPlan(scenario.facets...)
			raw := semanticPlanJSON(t, p)
			model := &scriptedLLM{responses: []string{raw, raw}}
			w := New(nil, model, registerPlanValidationResearchTools(t), Options{MaxSteps: scenario.limit})
			_, err := w.createPlan(context.Background(), domain.Task{Title: "Research"}, "Research private_user_subject", "")
			var failure *plannerFailureError
			if !errors.Is(err, errResearchPlanIncomplete) || errors.Is(err, errInvalidModelOutput) || !errors.As(err, &failure) || model.requests != 2 {
				t.Fatalf("semantic failure lost: err=%v calls=%d", err, model.requests)
			}
			if failure.Role != "planner_correction" || failure.Attempt != 2 || failure.PlanSummary["search"] != 4 || failure.PlanSummary["fetch"] != 0 || failure.Cause == nil {
				t.Fatalf("incorrect diagnostics: %#v", failure)
			}
			var input struct {
				Reason    string      `json:"backend_validation_error"`
				Previous  domain.Plan `json:"previous_plan_untrusted"`
				MinSearch int         `json:"minimum_searches"`
				MaxSearch int         `json:"maximum_searches"`
				MinFetch  int         `json:"minimum_fetches"`
				Kinds     []string    `json:"allowed_kinds"`
			}
			if err := json.Unmarshal([]byte(model.seen[1].Messages[1].Content), &input); err != nil {
				t.Fatal(err)
			}
			if input.Reason != failure.ValidationError || input.MinSearch != 2 || input.MaxSearch != 3 || input.MinFetch != 2 || !reflect.DeepEqual(input.Kinds, []string{"search", "fetch"}) {
				t.Fatalf("missing safe reason or repair constraints: %#v", input)
			}
			if strings.Contains(failure.ValidationError, "private") || strings.Contains(failure.ValidationError, "original_") {
				t.Fatal("untrusted data leaked into diagnostics")
			}
			for i, facet := range scenario.facets {
				if input.Previous.Subtasks[i].Description != facet {
					t.Fatal("semantic correction input truncated a facet")
				}
			}
			system := model.seen[1].Messages[0].Content
			if !strings.Contains(system, "Never return a search-only plan") || !strings.Contains(system, "must depend on every search") {
				t.Fatal("correction omitted executable dependency constraints")
			}
		})
	}
}

func TestPlannerSemanticCorrectionIsNormalizedBeforeValidation(t *testing.T) {
	p := semanticTestPlan("Protocol architecture", "Protocol architecture")
	corrected := semanticTestPlan("Protocol architecture", "Protocol limitations", "Protocol implementation", "Protocol compatibility")
	model := &scriptedLLM{responses: []string{semanticPlanJSON(t, p), semanticPlanJSON(t, corrected)}}
	w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
	plan, err := w.createPlan(context.Background(), domain.Task{Title: "Research"}, "Research protocol", "")
	if err != nil || model.requests != 2 {
		t.Fatalf("corrected four-search plan was not normalized: calls=%d err=%v", model.requests, err)
	}
	assertSemanticResearchPlan(t, w, plan, "Research protocol", 3)
}

func TestPlannerSemanticInitialOrderingDependenciesAndMissingKindsNormalize(t *testing.T) {
	for _, goal := range []string{"Research protocol", "Research protocol and compare https://example.org/spec with other sources"} {
		for searches := 1; searches <= 3; searches++ {
			facets := []string{"Protocol architecture", "Protocol limitations", "Protocol implementation"}[:searches]
			p := semanticTestPlan(facets...)
			p.Subtasks = append([]domain.PlanStep{{ID: "source", Kind: "fetch", Description: "Inspect original source description", Dependencies: []string{}, SuccessCriteria: []string{"stored"}}}, p.Subtasks...)
			p.MaxSteps = 12
			// A dependent later search is a schema-valid structure that the
			// backend can deterministically make independent.
			if searches > 1 {
				p.Subtasks[2].Dependencies = []string{"original_0"}
			}
			model := &scriptedLLM{responses: []string{semanticPlanJSON(t, p)}}
			w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
			plan, err := w.createPlan(context.Background(), domain.Task{Title: "Research protocol"}, goal, "")
			if err != nil || model.requests != 1 {
				t.Fatalf("schema-valid initial structure required model correction: %v calls=%d", err, model.requests)
			}
			assertSemanticResearchPlan(t, w, plan, goal, max(2, searches))
			for _, facet := range facets {
				count := 0
				for _, step := range plan.Subtasks {
					if step.Kind == "search" {
						count += strings.Count(step.Description, facet)
					}
				}
				if count != 1 {
					t.Fatalf("within-limit facet was changed or merged away: %q", facet)
				}
			}
		}
	}
}

func TestPlannerSemanticCorrectionCannotDropAnOriginalFacet(t *testing.T) {
	original := semanticTestPlan("Protocol architecture", "Protocol architecture", "Protocol private_required_facet")
	corrected := semanticTestPlan("Protocol architecture", "Protocol limitations")
	model := &scriptedLLM{responses: []string{semanticPlanJSON(t, original), semanticPlanJSON(t, corrected)}}
	w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
	_, err := w.createPlan(context.Background(), domain.Task{Title: "Research"}, "Research protocol", "")
	if !errors.Is(err, errResearchPlanIncomplete) || errors.Is(err, errInvalidModelOutput) || model.requests != 2 || strings.Contains(err.Error(), "private_required_facet") {
		t.Fatalf("lossy correction accepted or unsafe failure: %v calls=%d", err, model.requests)
	}
}

func TestPlannerSemanticBackendRequiresResearchKinds(t *testing.T) {
	w := New(nil, nil, registerPlanValidationResearchTools(t), Options{})
	p := semanticTestPlan("Protocol architecture", "Protocol limitations")
	if err := w.validateTaskPlan(p, "Research", "Research protocol"); !errors.Is(err, errResearchPlanIncomplete) {
		t.Fatalf("search-only research accepted: %v", err)
	}
	for i := range p.Subtasks {
		p.Subtasks[i].Kind = "fetch"
	}
	if err := w.validateTaskPlan(p, "Research", "Research protocol"); !errors.Is(err, errResearchPlanIncomplete) {
		t.Fatalf("fetch-only research accepted: %v", err)
	}
	p = normalizeResearchPlan(semanticTestPlan("Protocol architecture", "Protocol limitations"), "Research", "Research protocol")
	if err := w.validateTaskPlan(p, "Research", "Research protocol and compare https://example.org/spec"); !errors.Is(err, errResearchPlanIncomplete) {
		t.Fatalf("URL research without first fetch accepted: %v", err)
	}
}

func TestPlannerSemanticBrokenDependenciesRemainSemanticAfterCorrection(t *testing.T) {
	plan := semanticTestPlan("Математика", "Информатика и программирование")
	plan.Subtasks[0].Dependencies = []string{plan.Subtasks[1].ID}
	plan.Subtasks[1].Dependencies = []string{plan.Subtasks[0].ID}
	model := &scriptedLLM{responses: []string{semanticPlanJSON(t, plan), semanticPlanJSON(t, plan)}}
	worker := New(nil, model, registerPlanValidationResearchTools(t), Options{})
	_, err := worker.createPlan(context.Background(), domain.Task{Title: "Research"}, "Исследуй направления обучения", "")
	var failure *plannerFailureError
	if !errors.Is(err, errResearchPlanIncomplete) || errors.Is(err, errInvalidModelOutput) || !errors.As(err, &failure) || model.requests != 2 {
		t.Fatalf("schema-valid cyclic dependencies were not classified as semantic: err=%v calls=%d", err, model.requests)
	}
	if failure.ValidationError != "research plan is incomplete: research plan contains inconsistent step IDs, dependencies, or bounds" {
		t.Fatalf("unsafe or unspecific dependency diagnosis: %+v", failure)
	}
}

func TestPlannerSemanticMalformedCorrectionRemainsInvalidModelOutput(t *testing.T) {
	original := semanticTestPlan("Protocol architecture", "Protocol architecture")
	model := &scriptedLLM{responses: []string{semanticPlanJSON(t, original), `not json`, `not json`}}
	w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
	_, err := w.createPlan(context.Background(), domain.Task{Title: "Research"}, "Research protocol", "")
	var failure *plannerFailureError
	if !errors.Is(err, errInvalidModelOutput) || errors.Is(err, errResearchPlanIncomplete) || !errors.As(err, &failure) || model.requests != 3 || failure.Attempt != 2 || failure.Role != "planner_correction" {
		t.Fatalf("malformed correction classification: err=%v calls=%d", err, model.requests)
	}
}

func TestPlannerSemanticMalformedOutputRemainsInvalidModelOutput(t *testing.T) {
	model := &scriptedLLM{responses: []string{`{"private_raw":"secret"}`, `not json`}}
	w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
	_, err := w.createPlan(context.Background(), domain.Task{Title: "Research"}, "Research protocol", "")
	var failure *plannerFailureError
	if !errors.Is(err, errInvalidModelOutput) || errors.Is(err, errResearchPlanIncomplete) || !errors.As(err, &failure) || model.requests != 2 || failure.Attempt != 1 || failure.Role != "planner_initial" {
		t.Fatalf("malformed classification: err=%v calls=%d", err, model.requests)
	}
	if strings.Contains(failure.ValidationError, "secret") || strings.Contains(failure.ValidationError, "private_raw") {
		t.Fatal("raw schema diagnostics exposed")
	}
}

func TestPlannerSemanticAnalysisOnlyNeverGainsSearch(t *testing.T) {
	p := domain.Plan{SchemaVersion: 1, Goal: "Count text", FinalDeliverable: "Count", MaxSteps: 12, Subtasks: []domain.PlanStep{{ID: "inspect", Kind: "analysis", Description: "Count the provided text", Dependencies: []string{}, SuccessCriteria: []string{"count recorded"}}}}
	registry := registerPlanValidationResearchTools(t)
	model := &scriptedLLM{responses: []string{semanticPlanJSON(t, p)}}
	w := New(nil, model, registry, Options{})
	plan, err := w.createPlan(context.Background(), domain.Task{Title: "Count text"}, "Count words in the following text: research search news", "")
	if err != nil || len(plan.Subtasks) != 1 || plan.Subtasks[0].Kind != "analysis" || plan.MaxSteps != 1 || model.requests != 1 {
		t.Fatalf("analysis acquired unintended research: %#v err=%v", plan, err)
	}
	if contains(w.planKindsForTask("Count text", "Count words in the following text: news"), "search") {
		t.Fatal("self-contained planner schema enables discovery search")
	}
}

func TestPlannerSemanticFailureEventContainsOnlySafeDiagnostics(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "planner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := semanticTestPlan(strings.Repeat("private_alpha ", 45), strings.Repeat("private_beta ", 50), strings.Repeat("private_gamma ", 45), "private_delta")
	raw := semanticPlanJSON(t, p)
	model := &scriptedLLM{responses: []string{raw, raw}}
	w := New(store, model, registerPlanValidationResearchTools(t), Options{MaxSteps: 4})
	task, err := store.CreateTask(ctx, domain.Task{ID: "planner-failure", Title: "Research", Description: "Research private_user_subject"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	w.runTask(ctx, claimed)
	events, err := store.EventsAfter(ctx, task.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "task.failed" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["error_code"] != "research_plan_incomplete" || payload["failure_stage"] != "planning" || payload["failure_role"] != "planner_correction" || payload["planner_attempt"] != float64(2) || payload["validation_error"] == nil || payload["plan_summary"] == nil {
			t.Fatalf("missing planner failure fields: %s", event.Payload)
		}
		if strings.Contains(string(event.Payload), "private_") || strings.Contains(string(event.Payload), "original_") {
			t.Fatalf("untrusted data in public diagnostics: %s", event.Payload)
		}
		return
	}
	t.Fatal("no task.failed event")
}

func russianResearchFacets() (string, []string) {
	return "Найди университеты вне Казахстана с полным грантом по математике, программированию, химии и биологии, биоинженерии. Укажи условия поступления и сроки подачи документов.", []string{
		"Найти университеты с полным грантом по математике: требования к абитуриентам и сроки подачи документов.",
		"Найти программы по программированию с полным финансированием и доступностью для иностранных абитуриентов.",
		"Найти гранты по химии и биологии: покрытие расходов, язык обучения и требования к поступающим.",
		"Найти программы по биоинженерии с полным грантом: условия поступления, ограничения и дедлайны.",
	}
}

func TestPlannerSemanticRussianFourFacetsRemainVerbatim(t *testing.T) {
	goal, facets := russianResearchFacets()
	p := semanticTestPlan(facets...)
	p.Goal = goal
	model := &scriptedLLM{responses: []string{semanticPlanJSON(t, p)}}
	w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
	plan, err := w.createPlan(context.Background(), domain.Task{Title: "Университеты и гранты"}, goal, "")
	if err != nil || model.requests != 1 {
		t.Fatalf("Russian four-facet repair: calls=%d err=%v", model.requests, err)
	}
	assertSemanticResearchPlan(t, w, plan, goal, 3)
	for _, facet := range facets {
		count := 0
		for _, step := range plan.Subtasks {
			if step.Kind == "search" {
				count += strings.Count(step.Description, facet)
			}
		}
		if count != 1 {
			t.Fatalf("lost or duplicated verbatim Russian facet (%d): %q", count, facet)
		}
	}
}

func TestPlannerSemanticRussianFourSearchRunCommitsPlanBeforeProviderInvocation(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	goal, facets := russianResearchFacets()
	saved := f.submit(t, goal, nil)
	initial := semanticTestPlan(facets...)
	initial.Goal = goal
	raw := semanticPlanJSON(t, initial)
	schema := planOutputSchemaWithRequiredKinds(saved.Budget.MaxSteps, []string{"search", "fetch"}, requiredResearchKinds(f.task.Title, goal))
	if err := tools.ValidateJSONSchema(schema, []byte(raw)); err != nil {
		t.Fatalf("initial four-search/zero-fetch plan must reach backend normalization: %v", err)
	}
	model := &scriptedLLM{responses: []string{
		raw,
		`{"status":"continue","summary":"Поиск университетов с полным грантом","tool_call":{"name":"search","arguments":{"query":"университеты полный грант математика программирование биоинженерия","language":null,"recency":null,"max_results":8}}}`,
	}}
	// An actual provider failure stops execution after the first search. A
	// failed search must not trigger the successful-empty-result broader retry.
	search := &researchRunSearch{fail: research.ErrSearchUnavailable}
	fetcher := &researchRunFetcher{}
	var eventsAtProvider []domain.Event
	var providerReadErr error
	var countersAtProvider domain.ResearchSession
	search.before = func(int) {
		eventsAtProvider, providerReadErr = f.store.EventsAfter(ctx, f.task.ID, 0, 1000)
		if providerReadErr == nil {
			countersAtProvider, providerReadErr = f.store.GetResearchSession(ctx, f.task.ID)
		}
	}
	w := New(f.store, model, researchRunRegistry(t, f.store, search, fetcher), Options{})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusFailed, "search_unavailable")
	if search.calls.Load() != 1 || fetcher.calls.Load() != 0 || model.requests != 2 {
		t.Fatalf("expected planner, first search actor, and one provider call: model=%d search=%d fetch=%d", model.requests, search.calls.Load(), fetcher.calls.Load())
	}
	if providerReadErr != nil {
		t.Fatal(providerReadErr)
	}
	if countersAtProvider.Counters.SearchQueries != 1 || countersAtProvider.Counters.Fetches != 0 {
		t.Fatalf("provider invocation lacks durable search reservation: %+v", countersAtProvider.Counters)
	}
	after := researchRunSession(t, f.store, saved.TaskID)
	if after.Counters.SearchQueries != 1 || after.Counters.Fetches != 0 || after.Counters.LLMCalls != 2 {
		t.Fatalf("unexpected persisted session usage: %+v", after.Counters)
	}
	task, err := f.store.GetTask(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Plan == nil {
		t.Fatal("normalized plan was not committed before search")
	}
	plan := *task.Plan
	assertSemanticResearchPlan(t, w, plan, goal, 3)
	if len(plan.Subtasks) != 5 || countPlanKind(plan, "fetch") != 2 {
		t.Fatalf("saved plan must contain three searches and two fetches: %#v", plan)
	}
	encoded := semanticPlanJSON(t, plan)
	if err := tools.ValidateJSONSchema(schema, []byte(encoded)); err != nil {
		t.Fatalf("saved normalized plan violates planner schema: %v", err)
	}
	for _, facet := range facets {
		count := 0
		for _, step := range plan.Subtasks {
			if step.Kind == "search" {
				count += strings.Count(step.Description, facet)
			}
		}
		if count != 1 {
			t.Fatalf("saved plan lost or duplicated Russian facet (%d): %q", count, facet)
		}
	}
	var planSeq, stepSeq, toolSeq, providerBoundary int64
	for _, event := range eventsAtProvider {
		providerBoundary = max(providerBoundary, event.Seq)
		switch event.Type {
		case "plan.created":
			if planSeq != 0 {
				t.Fatal("more than one plan was committed")
			}
			planSeq = event.Seq
		case "step.started", "tool.started":
			var payload struct {
				StepID string `json:"step_id"`
				Tool   string `json:"tool"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.StepID != plan.Subtasks[0].ID || (event.Type == "tool.started" && payload.Tool != "search") {
				t.Fatalf("provider reached from unexpected step/tool: %s", event.Payload)
			}
			if event.Type == "step.started" {
				stepSeq = event.Seq
			} else {
				toolSeq = event.Seq
			}
		case "task.failed":
			t.Fatal("task failed before provider invocation")
		}
	}
	if !(0 < planSeq && planSeq < stepSeq && stepSeq < toolSeq && toolSeq <= providerBoundary) {
		t.Fatalf("expected plan.created < step.started < tool.started <= provider invocation: %d < %d < %d <= %d", planSeq, stepSeq, toolSeq, providerBoundary)
	}
	events, err := f.store.EventsAfter(ctx, f.task.ID, providerBoundary, 1000)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, event := range events {
		if event.Type == "plan.created" || event.Type == "step.started" || event.Type == "tool.started" {
			t.Fatalf("unexpected additional work after provider sentinel: %s", event.Type)
		}
		if event.Type == "task.failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("provider sentinel did not produce task.failed after search started")
	}
}

func TestPlannerSemanticReasonCatalogueRejectsUntrustedErrorText(t *testing.T) {
	for reason := range plannerSemanticReasons {
		err := fmt.Errorf("%w: %s", errResearchPlanIncomplete, strings.TrimPrefix(reason, errResearchPlanIncomplete.Error()+": "))
		if got := safePlannerReason(err); got != reason || len(got) > 256 {
			t.Fatalf("known bounded reason changed: %q -> %q", reason, got)
		}
	}
	for _, detail := range []string{
		"private_user_text: secret_step_id",
		"search results must be fetched before synthesis\nsecret_step_id",
		"search facets cannot fit losslessly within 999 searches and 1000 bytes per description",
		"search facets cannot fit losslessly within 03 searches and 1000 bytes per description",
		strings.Repeat("private_text ", 10000),
	} {
		err := fmt.Errorf("%w: %s", errResearchPlanIncomplete, detail)
		if got := safePlannerReason(err); got != plannerSemanticFallback {
			t.Fatalf("unknown semantic text was forwarded: %q", got)
		}
	}
	wrapped := fmt.Errorf("private_id: %w", fmt.Errorf("%w: search results must be fetched before synthesis", errResearchPlanIncomplete))
	if safePlannerReason(wrapped) != plannerSemanticFallback || safePlannerReason(errResearchPlanIncomplete) != plannerSemanticFallback {
		t.Fatal("non-catalogue wrapper was exposed")
	}
}

func TestPlannerSemanticPublicMessagesDescribeActualFailure(t *testing.T) {
	if got := publicError("research_plan_incomplete"); strings.Contains(got, "Сохранённый") || !strings.Contains(got, "План исследования не прошёл проверку") {
		t.Fatalf("misleading generic plan failure: %q", got)
	}
	if got := publicPlannerError("research plan is incomplete: search results must be fetched before synthesis"); got != "План исследования не прошёл проверку: поисковые шаги не были дополнены загрузкой источников." {
		t.Fatalf("missing-fetch message: %q", got)
	}
	if got := publicPlannerError("research plan is incomplete: search facets cannot fit losslessly within 3 searches and 1000 bytes per description"); !strings.Contains(got, "без потери исходного текста") || strings.Contains(got, "не были дополнены") {
		t.Fatalf("impossible merge described as a missing fetch: %q", got)
	}
	if got := publicPlannerError("private_error"); got != publicError("research_plan_incomplete") {
		t.Fatalf("unknown public reason was forwarded: %q", got)
	}
}

func TestPlannerSemanticDirectMissingSearchIsUnavailable(t *testing.T) {
	model := &scriptedLLM{}
	w := New(nil, model, tools.NewRegistry(), Options{})
	_, err := w.createPlan(context.Background(), domain.Task{Title: "Исследование"}, "Найди университеты с полным грантом", "")
	var failure *plannerFailureError
	if !errors.Is(err, errSearchUnavailable) || errors.Is(err, errUnsupportedStep) || !errors.As(err, &failure) || model.requests != 0 || failure.Attempt != 1 || failure.Role != "planner_initial" {
		t.Fatalf("direct planner missing-provider classification: %v calls=%d", err, model.requests)
	}
	if failure.ValidationError != safePlannerReason(errSearchUnavailable) || failure.PlanSummary["steps"] != 0 {
		t.Fatalf("unsafe missing-provider diagnostics: %#v", failure)
	}
}

func TestPlannerSemanticSingleCorrectionAllowsOneFormatRepairPerRole(t *testing.T) {
	p := semanticTestPlan("Protocol architecture", "Protocol architecture")
	raw := semanticPlanJSON(t, p)
	model := &scriptedLLM{responses: []string{"not json", raw, "not json", raw}}
	w := New(nil, model, registerPlanValidationResearchTools(t), Options{})
	_, err := w.createPlan(context.Background(), domain.Task{Title: "Research"}, "Research protocol", "")
	var failure *plannerFailureError
	if !errors.Is(err, errResearchPlanIncomplete) || !errors.As(err, &failure) || failure.Attempt != 2 || failure.Role != "planner_correction" || model.requests != 4 {
		t.Fatalf("format repairs changed semantic correction bound: %v calls=%d", err, model.requests)
	}
	for _, index := range []int{1, 3} {
		if !strings.Contains(model.seen[index].Messages[0].Content, "previous response failed the required JSON schema") {
			t.Fatalf("request %d was not the bounded per-role format repair", index)
		}
	}
	if failure.ValidationError != "research plan is incomplete: search questions must be distinct" {
		t.Fatalf("format repair obscured final semantic reason: %q", failure.ValidationError)
	}
}

func TestPlannerSemanticPinnedCorrectionRetainsPreciseConstraints(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	goal, _ := russianResearchFacets()
	f.submit(t, goal, nil)
	p := semanticTestPlan(strings.Repeat("alpha ", 100), strings.Repeat("beta ", 120), strings.Repeat("gamma ", 100), strings.Repeat("delta ", 100))
	raw := semanticPlanJSON(t, p)
	model := &scriptedLLM{responses: []string{raw, raw, raw, raw}}
	w := New(f.store, model, registerPlanValidationResearchTools(t), Options{})
	// A legacy correction pin has a valid snapshot but no validation reason
	// or counts. New correction identities must not replay that payload.
	legacyCtx, snapshot, _, err := w.roleContext(ctx, f.task.ID, "planner_correction", goal)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]any{"context_snapshot": snapshot})
	if _, err := w.pinRoleInput(legacyCtx, f.task.ID, string(legacy)); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := w.createPlan(ctx, f.task, goal, fmt.Sprintf("memory_%d", attempt))
		if !errors.Is(err, errResearchPlanIncomplete) {
			t.Fatalf("expected repeated semantic failure: %v", err)
		}
		var input map[string]json.RawMessage
		if err := json.Unmarshal([]byte(model.seen[attempt*2+1].Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"backend_validation_error", "plan_summary", "allowed_kinds", "minimum_searches", "maximum_searches", "minimum_fetches", "maximum_fetches", "maximum_steps", "previous_plan_untrusted", "context_snapshot"} {
			if len(input[key]) == 0 {
				t.Fatalf("pinned correction omitted %s", key)
			}
		}
		var reason string
		_ = json.Unmarshal(input["backend_validation_error"], &reason)
		if reason != "research plan is incomplete: search facets cannot fit losslessly within 3 searches and 1000 bytes per description" {
			t.Fatalf("precise backend error missing: %q", reason)
		}
		var summary map[string]int
		_ = json.Unmarshal(input["plan_summary"], &summary)
		if summary["search"] != 4 || summary["fetch"] != 0 {
			t.Fatalf("pinned counts missing: %#v", summary)
		}
	}
	if model.requests != 4 || model.seen[1].Messages[1].Content != model.seen[3].Messages[1].Content {
		t.Fatal("pinned correction input changed across replay")
	}
}

func TestPlannerSemanticConversationFailureKeepsEmptyAssistantAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	goal, facets := russianResearchFacets()
	f.submit(t, goal, nil)
	for i := range facets {
		facets[i] += " " + strings.Repeat("private_long_facet ", 30)
	}
	p := semanticTestPlan(facets...)
	raw := semanticPlanJSON(t, p)
	model := &scriptedLLM{responses: []string{raw, raw}}
	w := New(f.store, model, registerPlanValidationResearchTools(t), Options{})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusFailed, "research_plan_incomplete")
	if model.requests != 2 {
		t.Fatalf("semantic correction count was not bounded: calls=%d", model.requests)
	}
	messages, err := f.store.ConversationMessages(ctx, f.turn.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	assistantFound := false
	for _, message := range messages {
		if message.ID == f.turn.AssistantMessage.ID {
			assistantFound = true
			if message.Content != "" {
				t.Fatalf("planning failure should not synthesize an assistant answer: %q", message.Content)
			}
		}
	}
	if !assistantFound {
		t.Fatal("linked assistant placeholder disappeared")
	}
	task, err := f.store.GetTask(ctx, f.task.ID)
	if err != nil || task.Plan != nil {
		t.Fatalf("unrepairable plan was saved: plan=%v err=%v", task.Plan, err)
	}
	events, err := f.store.EventsAfter(ctx, f.task.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, event := range events {
		if event.Type == "plan.created" || event.Type == "step.started" || strings.HasPrefix(event.Type, "tool.") || strings.HasPrefix(event.Type, "search.") || strings.HasPrefix(event.Type, "fetch.") {
			t.Fatalf("planning failure executed or saved work: %s", event.Type)
		}
		if event.Type != "task.failed" {
			continue
		}
		failed++
		var payload struct {
			Code    string         `json:"error_code"`
			Stage   string         `json:"failure_stage"`
			Role    string         `json:"failure_role"`
			Reason  string         `json:"validation_error"`
			Attempt int            `json:"planner_attempt"`
			Summary map[string]int `json:"plan_summary"`
			Message string         `json:"message"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Code != "research_plan_incomplete" || payload.Stage != "planning" || payload.Role != "planner_correction" || payload.Attempt != 2 || !reflect.DeepEqual(payload.Summary, plannerPlanSummary(p)) {
			t.Fatalf("missing full conversation-linked diagnostics: %s", event.Payload)
		}
		if _, safe := plannerSemanticReasons[payload.Reason]; !safe || payload.Reason == plannerSemanticFallback || payload.Message != publicPlannerError(payload.Reason) {
			t.Fatalf("missing precise safe reason/public message: %s", event.Payload)
		}
		if strings.Contains(string(event.Payload), "private_long_facet") || strings.Contains(string(event.Payload), "original_") || strings.Contains(string(event.Payload), facets[0]) {
			t.Fatalf("public diagnostics contain user text/IDs: %s", event.Payload)
		}
	}
	if failed != 1 {
		t.Fatalf("task.failed count=%d, want one", failed)
	}
}
