package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestCompactionRetainsEveryResultUnderLiveTaskSizedBudget(t *testing.T) {
	results := map[string]any{
		"search_1": []research.SearchHit{{Provider: "local", Query: "independent research question", URL: "https://one.example/page", Title: "Source title", Snippet: strings.Repeat("факты ", 850)}},
		"fetch_1":  map[string]any{"evidence_id": "evidence-live", "url": "https://one.example/page", "excerpt": strings.Repeat("данные ", 650), "fetched_bytes": 4565},
	}
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "recovered"}[recovered], func(t *testing.T) {
			input := results
			if recovered {
				encoded, _ := json.Marshal(results)
				if err := json.Unmarshal(encoded, &input); err != nil {
					t.Fatal(err)
				}
			}
			out, err := compactToolResultsWithinBudget(input, 1800)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(out)
			if len(out) != 2 || len(encoded) > 1800 || !utf8.Valid(encoded) {
				t.Fatalf("compaction result count=%d bytes=%d: %s", len(out), len(encoded), encoded)
			}
			for _, field := range []string{"evidence-live", "local", "independent research question", "https://one.example/page", "Source title", "snippet", "excerpt"} {
				if !strings.Contains(string(encoded), field) {
					t.Errorf("lost %q: %s", field, encoded)
				}
			}
			fetch := out["fetch_1"].(map[string]any)
			hit := out["search_1"].([]any)[0].(map[string]any)
			if len(fetch["excerpt"].(string)) < 400 || len(hit["snippet"].(string)) < 400 {
				t.Fatalf("text budget was not shared: %s", encoded)
			}
			out, err = compactToolResultsWithinBudget(input, 20)
			if !errors.Is(err, errInputBudgetExceeded) || len(out) != 2 {
				t.Fatalf("small budget silently lost results: out=%v err=%v", out, err)
			}
		})
	}
}

func TestVerifierReceivesCompactedEvidenceOrFailsBeforeModelCall(t *testing.T) {
	plan := domain.Plan{Goal: "Inspect source", FinalDeliverable: "Supported answer", Subtasks: []domain.PlanStep{
		{ID: "search_1", Kind: "search", Description: "Discover source", SuccessCriteria: []string{"results recorded"}},
		{ID: "fetch_1", Kind: "fetch", Description: "Inspect source", SuccessCriteria: []string{"evidence recorded"}},
	}}
	state := checkpointState{CompletedSteps: []string{"search_1", "fetch_1"}, ToolResults: map[string]any{
		"fetch_1": map[string]any{"evidence_id": "evidence-live", "url": "https://one.example/", "excerpt": strings.Repeat("fetch ", 761)},
	}}
	hits := make([]any, 0, 8)
	for index := 0; index < 8; index++ {
		hits = append(hits, map[string]any{
			"provider": "searxng", "query": "IT universities full scholarship outside Kazakhstan admission",
			"url":   fmt.Sprintf("https://university-%d.example/admissions/international/full-scholarship/computer-science/program", index),
			"title": "International computer science full scholarship program", "snippet": strings.Repeat("scholarship details ", 18),
			"score": 1.0, "published_at": "0001-01-01T00:00:00Z", "taint": research.TaintExternal,
		})
	}
	state.ToolResults["search_1"] = hits
	for _, budget := range []int{10240, 1024} {
		response := `{"ready_to_answer":false,"summary":"Insufficient support","missing":["criterion unsupported"],"supplementary_criteria":[],"criterion_assessments":{"c0":{"met":false,"evidence_ids":[],"rationale":"criterion unsupported"},"c1":{"met":false,"evidence_ids":[],"rationale":"criterion unsupported"}}}`
		model := &scriptedLLM{responses: []string{response, response}}
		worker := New(nil, model, nil, Options{MaxInputBytes: budget})
		_, err := worker.verify(context.Background(), domain.Task{Title: "Inspect source"}, "Inspect the supplied URL", "", plan, state)
		if budget == 1024 {
			if !errors.Is(err, errInputBudgetExceeded) || model.requests != 0 {
				t.Fatalf("small verifier budget: requests=%d err=%v", model.requests, err)
			}
			continue
		}
		if err != nil || model.requests != 1 {
			t.Fatalf("verifier requests=%d err=%v", model.requests, err)
		}
		var input struct {
			Results map[string]any `json:"tool_results"`
		}
		if err := json.Unmarshal([]byte(model.seen[0].Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		if len(input.Results) != 2 || len(collectEvidenceIDs(input.Results)) != 1 {
			t.Fatalf("verifier lost evidence: %#v", input.Results)
		}
		if len(input.Results["search_1"].([]any)) != 8 {
			t.Fatal("verifier lost executed search hits")
		}
		request := model.seen[0]
		var originalInput map[string]any
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &originalInput); err != nil {
			t.Fatal(err)
		}
		searchJSON, _ := json.Marshal(compactToolResult(state.ToolResults["search_1"]))
		fetchJSON, _ := json.Marshal(compactToolResult(state.ToolResults["fetch_1"]))
		wholeItemDropBudget := min(len(searchJSON), len(fetchJSON)) - 1
		currentResultsBudget := toolResultsBudget(budget, request.Messages[0].Content, request.JSONSchema, originalInput, "tool_results")
		worker.options.MaxInputBytes = budget - currentResultsBudget + wholeItemDropBudget
		if _, err := worker.verify(context.Background(), domain.Task{Title: "Inspect source"}, "Inspect the supplied URL", "", plan, state); err != nil {
			t.Fatalf("budget that previously dropped both whole results: %v", err)
		}
		if err := json.Unmarshal([]byte(model.seen[1].Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		if len(input.Results) != 2 || len(collectEvidenceIDs(input.Results)) != 1 {
			t.Fatalf("live all-results-lost regression: %#v", input.Results)
		}
	}
	model := &scriptedLLM{}
	worker := New(nil, model, nil, Options{})
	if _, err := worker.verify(context.Background(), domain.Task{}, "", "", plan, checkpointState{}); err == nil || model.requests != 0 {
		t.Fatalf("empty evidence reached verifier: requests=%d err=%v", model.requests, err)
	}
}

func TestUniversalResearchDefaultAndRussianSuppliedTextExceptions(t *testing.T) {
	for _, value := range []string{"универы айти\nмне надо чтобы ты нашел все айти универы вне казахстана с польным грантам куда я могу подать документы и учиться там", "объясни устройство протокола", "расскажи про квантовую запутанность", "плюсы и минусы подхода", "по ищи компьютеры в астане на бюджет 400к тенге", "Protocol architecture and limitations", "Unfamiliar topic", "Analyze text processing techniques in this market", "Проанализируй текстовые модели в этом году", "Проанализируй текст ниже и найди источники в интернете: исходный текст"} {
		if !requiresWebResearch(value) || minimumResearchSearches(value) != 2 || minimumResearchFetches(value) != 2 {
			t.Errorf("external inquiry bypassed research: %q", value)
		}
	}
	for _, value := range []string{"проанализируй текст ниже", "посчитай слова в этом тексте", "проанализируй текст ниже: библиотеки в Астане", "Summarize the following text: news and prices in Astana", "https://example.com/page", "Read https://example.com/page and summarize it", "Summarize documentation at https://example.com/page"} {
		if requiresWebResearch(value) {
			t.Errorf("self-contained operation triggered discovery: %q", value)
		}
	}
}

func TestOrdinaryPlannerSchemaAndMalformedRealFormatRepair(t *testing.T) {
	schema := planOutputSchemaWithRequiredKinds(12, []string{"search", "fetch"}, []string{"search", "search", "fetch", "fetch"})
	for _, keyword := range []string{"prefixItems", "anyOf", "oneOf"} {
		if strings.Contains(string(schema), keyword) {
			t.Fatalf("planner schema uses %s", keyword)
		}
	}
	valid := `{"schema_version":1,"goal":"объясни устройство протокола","subtasks":[{"id":"search_1","kind":"search","description":"Protocol architecture","dependencies":[],"expected_output":"hits","success_criteria":["results recorded"]},{"id":"search_2","kind":"search","description":"Protocol limitations","dependencies":[],"expected_output":"hits","success_criteria":["results recorded"]},{"id":"fetch_1","kind":"fetch","description":"Inspect architecture source","dependencies":[],"expected_output":"evidence","success_criteria":["source stored"]},{"id":"fetch_2","kind":"fetch","description":"Inspect limitations source","dependencies":["search_1"],"expected_output":"evidence","success_criteria":["source stored"]}],"max_steps":4,"final_deliverable":"Supported explanation"}`
	for _, malformed := range []string{
		strings.Replace(valid, `"dependencies":[]`, `"dependencies":["search_1"]`, 1),
		strings.Replace(valid, `"id":"search_2"`, `"id":"search_1"`, 1),
		strings.Replace(valid, `"dependencies":[]`, `"dependencies":"none"`, 1),
	} {
		model := &scriptedLLM{responses: []string{malformed, valid}}
		worker := New(nil, model, registerPlanValidationResearchTools(t), Options{})
		plan, err := worker.createPlan(context.Background(), domain.Task{Title: "Protocol"}, "объясни устройство протокола", "")
		if err != nil || model.requests != 2 {
			t.Fatalf("malformed plan repair: requests=%d err=%v", model.requests, err)
		}
		for _, step := range plan.Subtasks[2:] {
			if !contains(step.Dependencies, "search_1") || !contains(step.Dependencies, "search_2") {
				t.Errorf("fetch dependencies not normalized: %#v", step)
			}
		}
		if !strings.Contains(model.seen[1].Messages[1].Content, "протокола") {
			t.Fatal("planner repair lost original inquiry")
		}
	}
}

func TestLatestScholarshipPlanCannotPassResearchValidation(t *testing.T) {
	goal := "мне надо чтобы ты нашел все айти универы вне казахстана с польным грантам куда я могу подать документы и учиться там"
	plan := domain.Plan{SchemaVersion: 1, Goal: "Найти IT-университеты вне Казахстана с полным грантом.", MaxSteps: 3, FinalDeliverable: "Список университетов с полным финансированием", Subtasks: []domain.PlanStep{
		{ID: "How_to_find_IT_universities_with_full_scholarships_outside_Kazak", Kind: "search", Description: "Выполнить поиск IT universities full scholarship outside Kazakhstan", SuccessCriteria: []string{"Найти список университетов"}},
		{ID: "Verify_full_scholarship_availability_for_each_university", Kind: "fetch", Description: "Проверить наличие полного гранта", Dependencies: []string{"How_to_find_IT_universities_with_full_scholarships_outside_Kazak"}, SuccessCriteria: []string{"Подтвердить наличие финансовой поддержки", "Подтвердить отсутствие финансовой поддержки"}},
	}}
	worker := New(nil, nil, registerPlanValidationResearchTools(t), Options{})
	if err := worker.validateTaskPlan(plan, "универы айти", goal); !errors.Is(err, errResearchPlanIncomplete) {
		t.Fatalf("live one-search/one-fetch plan was accepted: %v", err)
	}
	if requiredPlanSteps("универы айти", goal) != 4 {
		t.Fatal("scholarship inquiry did not require two facets and two source sites")
	}
}

func TestFetchFallbackRejectsSafetyBudgetAndTaskCancellationErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("%w: HTTP 403", research.ErrFetchSourceUnavailable), true},
		{fmt.Errorf("%w: HTTP 503", research.ErrFetchSourceUnavailable), true},
		{fmt.Errorf("%w: HTTP 404", research.ErrFetchSourceUnavailable), true},
		{research.ErrFetchRequestFailed, true},
		{context.DeadlineExceeded, true},
		{research.ErrUnsafeURL, false},
		{research.ErrResponseTooBig, true},
		{research.ErrPDFEncrypted, true},
		{research.ErrPDFNoText, true},
		{research.ErrUnsupportedMIME, true},
		{context.Canceled, false},
		{errFetchBudgetExceeded, false},
	} {
		if got := isRetryableFetchError(context.Background(), test.err); got != test.want {
			t.Errorf("fetch fallback for %v = %t", test.err, got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if isRetryableFetchError(ctx, research.ErrFetchRequestFailed) {
		t.Fatal("task cancellation retried another source")
	}
}
