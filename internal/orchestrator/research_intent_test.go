package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestRequiresWebResearchRecognizesRussianAndEnglishRequests(t *testing.T) {
	for _, value := range []string{
		"Research the current architecture and limitations of this protocol.",
		"Investigate recent measurements and compare the available approaches.",
		"Compare current information from multiple sources.",
		"Find the latest public information about this topic.",
		"библиотеки в астане",
		"рестораны алматы",
		"по ищи компьютеры в астане на бюджет 400к тенге",
		"новости ии",
		"новинки смартфонов 2026",
		"новинки смартфонов 2026",
		"python 3.13 changelog",
		"who is X",
		"how many libraries are in Astana",
		"cost of living in Astana",
		"Сколько стоит билет в Астану?",
		"restaurants in Astana",
		"What are the opening hours in Astana?",
		"кафе рядом с центром",
		"Исследуй текущую архитектуру и ограничения этой системы.",
		"Поищи актуальные данные и сравни несколько подходов.",
		"Где находится объект в центральной части города?",
		"Where can I find this kind of service near the city center?",
	} {
		if !requiresWebResearch(value) {
			t.Errorf("requiresWebResearch(%q) = false", value)
		}
	}
	for _, value := range []string{
		"Count the words in this sentence",
		"How many words are in this sentence?",
		"How many words are in this sentence?",
		"Count occurrences of the word 'research' in the supplied paragraph",
		"Сосчитай слова в строке раз два три",
		"Найди слово в предоставленном тексте",
		"Summarize the text provided below",
		"посчитай слова в тексте ниже:",
		"summarize the following text:",
		"Count words\nOriginal task:\ncount three words here\n\nUser update:\nKeep the final answer concise.",
		"Fetch source\nRead https://docs.example.com/qwen and summarize the official page.",
	} {
		if requiresWebResearch(value) {
			t.Errorf("requiresWebResearch(%q) = true (info=%t location=%t who=%t self-contained=%t)", value, informationalNounPattern.MatchString(value), locationEntityPattern.MatchString(value), whoIsEntityPattern.MatchString(value), isSelfContainedTextOperation(value))
		}
	}
}

func TestGenericResearchDecomposesIntoBoundedSearchAndFetchSteps(t *testing.T) {
	goal := "Research the architecture, operating behavior, and limitations of an unfamiliar protocol."
	if got := minimumResearchSearches(goal); got != 2 {
		t.Fatalf("research search-facet count = %d, want 2 independent facets", got)
	}
	if got := minimumResearchFetches(goal); got != 2 {
		t.Fatalf("research source count = %d, want 2 distinct sites", got)
	}
	if got := requiredPlanSteps("Technical review", goal); got != 4 {
		t.Fatalf("research plan size = %d, want 2 independent searches and 2 fetches", got)
	}
	if got := requiredPlanSteps("Inspect supplied document", "Summarize https://example.org/document"); got != 1 {
		t.Fatalf("user-URL plan size = %d, want one fetch", got)
	}
	gotKinds := requiredResearchKinds("Technical review", goal)
	wantKinds := []string{"search", "search", "fetch", "fetch"}
	if len(gotKinds) != len(wantKinds) {
		t.Fatalf("generic research plan kinds = %#v", gotKinds)
	}
	for index := range wantKinds {
		if gotKinds[index] != wantKinds[index] {
			t.Fatalf("generic research plan kinds = %#v", gotKinds)
		}
	}
	if got := minimumResearchFetches("Count the words in this sentence"); got != 0 {
		t.Fatalf("local analysis fetch target = %d, want 0", got)
	}
}

func TestSuppliedURLWithoutDiscoveryDoesNotAddSearchSteps(t *testing.T) {
	for _, goal := range []string{
		"Прочитай https://example.org/page и опиши страну. Используй только содержимое указанной страницы, без дополнительного поиска.",
		"Прочитай https://example.org/page, не выполняй поиск в интернете.",
		"Read https://example.org/page without additional search.",
		"Read https://example.org/page; do not search the web.",
		"Read https://example.org/page with no external research.",
	} {
		if requiresWebResearch(goal) || minimumResearchSearches(goal) != 0 || minimumResearchFetches(goal) != 1 {
			t.Errorf("supplied-URL restriction triggered discovery: %q", goal)
		}
		if kinds := requiredResearchKinds("Inspect source", goal); len(kinds) != 1 || kinds[0] != "fetch" {
			t.Errorf("restricted URL kinds=%v for %q", kinds, goal)
		}
	}
}

func TestConditionalRetryRequiresSuccessfulZeroHitResult(t *testing.T) {
	worker := &Worker{tools: tools.NewRegistry()}
	state := checkpointState{ToolResults: map[string]any{
		"one-hit":        []research.SearchHit{{URL: "https://one.example/source"}},
		"provider-error": map[string]any{"error": "provider unavailable"},
	}}
	if searchResultIsSuccessfulEmpty(state.ToolResults["one-hit"]) {
		t.Fatal("one search hit was treated as an empty result")
	}
	if searchResultIsSuccessfulEmpty(state.ToolResults["provider-error"]) {
		t.Fatal("provider error object was treated as an empty result")
	}
	if !searchResultIsSuccessfulEmpty([]research.SearchHit{}) || !searchResultIsSuccessfulEmpty([]any{}) {
		t.Fatal("successful empty search result was not recognized")
	}
	if worker.searchResultsMeetSourceFloor("source-threshold", state.ToolResults["one-hit"], checkpointState{}, 2) {
		t.Fatal("one candidate site incorrectly met a two-site source floor")
	}
	state.ToolResults["search_1"] = state.ToolResults["one-hit"]
	if err := worker.retryInitialSearchIfEmpty(context.Background(), "task", nil, domain.PlanStep{ID: "search_1", Kind: "search"}, &state); err != nil {
		t.Fatalf("non-empty search result unexpectedly attempted retry: %v", err)
	}
	if _, exists := state.ToolResults[conditionalSearchRetryResultKey("search_1")]; exists {
		t.Fatal("a non-empty result below the distinct-source floor triggered retry")
	}
	state.ToolResults["search_1"] = state.ToolResults["provider-error"]
	if err := worker.retryInitialSearchIfEmpty(context.Background(), "task", nil, domain.PlanStep{ID: "search_1", Kind: "search"}, &state); err != nil {
		t.Fatalf("provider error was treated as an empty result: %v", err)
	}
	if _, exists := state.ToolResults[conditionalSearchRetryResultKey("search_1")]; exists {
		t.Fatal("provider error triggered a conditional retry")
	}
}

func TestResearchPlanValidationBoundsIndependentSearchesAndFetches(t *testing.T) {
	registry := registerPlanValidationResearchTools(t)
	worker := New(nil, &scriptedLLM{}, registry, Options{MaxSteps: 12})
	makePlan := func(searchCount, fetchCount int) domain.Plan {
		searchIDs := make([]string, 0, searchCount)
		steps := make([]domain.PlanStep, 0, searchCount+fetchCount)
		for index := 1; index <= searchCount; index++ {
			id := "search_" + strconv.Itoa(index)
			searchIDs = append(searchIDs, id)
			steps = append(steps, domain.PlanStep{
				ID: id, Kind: "search", Description: "Research independent facet " + id,
				SuccessCriteria: []string{"facet results are recorded"},
			})
		}
		for index := 1; index <= fetchCount; index++ {
			id := "fetch_" + strconv.Itoa(index)
			steps = append(steps, domain.PlanStep{
				ID: id, Kind: "fetch", Description: "Fetch independent source " + id,
				Dependencies: append([]string(nil), searchIDs...), SuccessCriteria: []string{"source evidence is stored"},
			})
		}
		return domain.Plan{SchemaVersion: 1, Goal: "Research the protocol", Subtasks: steps, MaxSteps: len(steps), FinalDeliverable: "cited summary"}
	}
	if err := worker.validateTaskPlan(makePlan(3, 4), "Protocol research", "Research the protocol."); err != nil {
		t.Fatalf("valid three-facet, four-fetch plan rejected: %v", err)
	}
	for _, invalid := range []domain.Plan{makePlan(4, 2), makePlan(2, 5)} {
		if err := worker.validateTaskPlan(invalid, "Protocol research", "Research the protocol."); !errors.Is(err, errResearchPlanIncomplete) {
			t.Errorf("out-of-bounds research plan accepted: searches/fetches=%d/%d, err=%v", countPlanKind(invalid, "search"), countPlanKind(invalid, "fetch"), err)
		}
	}
}

func TestResearchAnalysisOnlyPlanIsRepairedBeforeAnyWebAction(t *testing.T) {
	registry := registerPlanValidationResearchTools(t)
	validPlan := `{"schema_version":1,"goal":"research the protocol","subtasks":[{"id":"search_1","kind":"search","description":"Research the protocol specification","dependencies":[],"expected_output":"specification hits","success_criteria":["search results are recorded"]},{"id":"search_2","kind":"search","description":"Research protocol implementation behavior independently","dependencies":[],"expected_output":"implementation hits","success_criteria":["independent search results are recorded"]},{"id":"fetch_1","kind":"fetch","description":"Fetch the protocol specification","dependencies":["search_1","search_2"],"expected_output":"evidence","success_criteria":["source evidence is stored"]},{"id":"fetch_2","kind":"fetch","description":"Fetch a second source","dependencies":["search_1","search_2"],"expected_output":"evidence","success_criteria":["second source evidence is stored"]}],"max_steps":4,"final_deliverable":"cited summary"}`
	model := &scriptedLLM{responses: []string{
		`{"schema_version":1,"goal":"research the protocol","subtasks":[{"id":"inspect","kind":"analysis","description":"Inspect the prompt only","dependencies":[],"expected_output":"analysis","success_criteria":["analysis is recorded"]}],"max_steps":1,"final_deliverable":"summary"}`,
		validPlan,
	}}
	worker := New(nil, model, registry, Options{MaxSteps: 12, MaxOutputTokens: 512})
	plan, err := worker.createPlan(context.Background(), domain.Task{ID: "research-plan", Title: "Research the protocol"}, "Research the protocol.", "")
	if err != nil {
		t.Fatalf("analysis-only draft was not repaired: %v", err)
	}
	if err := worker.validateTaskPlan(plan, "Research the protocol", "Research the protocol."); err != nil {
		t.Fatalf("repaired plan is invalid: %v", err)
	}
	if model.requests != 2 {
		t.Fatalf("planner requests = %d, want initial draft and repair before execution", model.requests)
	}
	if got := truthfulActionSummary("text.inspect", "Searching the web for the latest information."); got != "Inspecting the supplied text." {
		t.Fatalf("text.inspect summary claims web search: %q", got)
	}
}

func registerPlanValidationResearchTools(t *testing.T) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	for _, descriptor := range []tools.Descriptor{
		{Name: "search", ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"array"}`), RequiredCapability: "research.search", RiskClass: "read", TimeoutSeconds: 1, MaxOutputBytes: 1024, AllowedRoles: []string{"researcher"}},
		{Name: "fetch_url", ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object"}`), RequiredCapability: "research.fetch", RiskClass: "read", TimeoutSeconds: 1, MaxOutputBytes: 1024, AllowedRoles: []string{"researcher"}},
	} {
		if err := registry.Register(descriptor, func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func countPlanKind(plan domain.Plan, kind string) int {
	count := 0
	for _, step := range plan.Subtasks {
		if step.Kind == kind {
			count++
		}
	}
	return count
}

func TestResearchRequestWithUserURLKeepsURLFirstAndAddsDiscovery(t *testing.T) {
	title := "Compare two sources"
	goal := "Compare https://docs.example.org/current with other current information."
	if got := minimumResearchSearches(title + "\n" + goal); got != 2 {
		t.Fatalf("URL-plus-research search count = %d, want one query plus one conditional retry", got)
	}
	if got := minimumResearchFetches(title + "\n" + goal); got != 2 {
		t.Fatalf("URL-plus-research source count = %d, want supplied URL plus one discovered source", got)
	}
	want := []string{"fetch", "search", "search", "fetch"}
	got := requiredResearchKinds(title, goal)
	if len(got) != len(want) {
		t.Fatalf("URL-plus-research required kinds = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("URL-plus-research required kinds = %#v, want %#v", got, want)
		}
	}
	if got := requiredPlanSteps(title, goal); got != 4 {
		t.Fatalf("URL-plus-research baseline plan steps = %d, want 4", got)
	}
}

func TestResearchQuestionsAndQueriesRejectDuplicatesGenerically(t *testing.T) {
	steps := []domain.PlanStep{
		{ID: "search_1", Kind: "search", Description: "Compare system architecture and components"},
		{ID: "search_2", Kind: "search", Description: "Compare system architecture and components!"},
		{ID: "search_3", Kind: "search", Description: "Review operational limitations"},
	}
	if err := validateDistinctSearchQuestions(steps); err == nil {
		t.Fatal("duplicate normalized search questions were accepted")
	}
	if !duplicateResearchQuery("system architecture components", []string{"Components! system architecture"}) {
		t.Fatal("a repeated query with reordered punctuation was not recognized")
	}
	if duplicateResearchQuery("new protocol benchmark", []string{"system architecture components"}) {
		t.Fatal("a distinct research query was incorrectly marked duplicate")
	}
}

func TestBroadeningDuplicateRetryQueryDropsRestrictiveTerms(t *testing.T) {
	for _, query := range []string{"protocol specification rare version filter", "protocol overview"} {
		broadened := broadenResearchQuery(query)
		if broadened == "" || duplicateResearchQuery(broadened, []string{query}) {
			t.Errorf("broadenResearchQuery(%q) = %q, want a distinct non-empty query", query, broadened)
		}
	}
}

func TestResearchSourceCoverageUsesDistinctSites(t *testing.T) {
	results := map[string]any{
		"fetch_1": map[string]any{"evidence_id": "e1", "url": "https://alpha.example.org/document-a"},
		"fetch_2": map[string]any{"evidence_id": "e2", "url": "https://www.alpha.example.org/document-b"},
		"fetch_3": map[string]any{"evidence_id": "e3", "url": "https://beta.example.net/document"},
	}
	if got := distinctResearchSourceCount(results); got != 2 {
		t.Fatalf("distinct source sites = %d, want 2", got)
	}
}

func TestAvailableFetchURLsPreferUnfetchedDistinctSites(t *testing.T) {
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{}); err != nil {
		t.Fatal(err)
	}
	registry.SeedTaskURLs("task-fetch", "Read https://user.example.org/source")
	registry.RememberURLs("task-fetch", []research.SearchHit{
		{URL: "https://alpha-source.com/document-1"},
		{URL: "https://beta-source.net/document-2"},
		{URL: "https://gamma-source.org/document-3"},
		{URL: "https://delta-source.io/document-4"},
		{URL: "https://epsilon-source.dev/document-5"},
		{URL: "https://zeta-source.app/document-6"},
		{URL: "https://eta-source.info/document-7"},
		{URL: "https://theta-source.biz/document-8"},
	})
	registry.RememberFetchedURLs("task-fetch", map[string]any{
		"evidence_id": "previous", "requested_url": "https://user.example.org/source", "url": "https://user.example.org/final",
	})
	worker := &Worker{tools: registry}
	urls := worker.availableFetchURLs("task-fetch", map[string]any{
		"search": []research.SearchHit{
			{URL: "https://alpha-source.com/document-1"},
			{URL: "https://beta-source.net/document-2"},
			{URL: "https://gamma-source.org/document-3"},
			{URL: "https://delta-source.io/document-4"},
			{URL: "https://epsilon-source.dev/document-5"},
			{URL: "https://zeta-source.app/document-6"},
			{URL: "https://eta-source.info/document-7"},
			{URL: "https://theta-source.biz/document-8"},
		},
	}, nil, nil, true)
	if len(urls) != 6 {
		t.Fatalf("fetch candidate pool has %d URLs, want 6: %#v", len(urls), urls)
	}
	seenSites := make(map[string]struct{}, len(urls))
	for _, rawURL := range urls {
		site := researchSourceSite(rawURL)
		if _, exists := seenSites[site]; exists {
			t.Fatalf("fetch candidate pool repeats site %q: %#v", site, urls)
		}
		seenSites[site] = struct{}{}
	}
	for _, rawURL := range urls {
		if rawURL == "https://user.example.org/source/" || rawURL == "https://user.example.org/final/" {
			t.Fatalf("previously fetched user URL reappeared: %#v", urls)
		}
	}
	urls = worker.availableFetchURLs("task-fetch", map[string]any{}, []string{"https://alpha-source.com/document-1"}, nil, false)
	if contains(urls, "https://alpha-source.com/document-1") {
		t.Fatalf("previously failed fetch URL was offered again: %#v", urls)
	}
}

func TestAvailableFetchURLsRetryFailedUserURLButExcludeFailedDiscoveryURL(t *testing.T) {
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{}); err != nil {
		t.Fatal(err)
	}
	const taskID = "task-user-url-retry"
	goal := "Read https://docs.example.com/qwen and summarize it."
	registry.SeedTaskURLs(taskID, goal)
	registry.RememberURLs(taskID, []research.SearchHit{{URL: "https://discovery.example.net/page"}})
	worker := &Worker{tools: registry}
	failedURLs := []string{"https://docs.example.com/qwen", "https://discovery.example.net/page"}
	urls := worker.availableFetchURLs(taskID, map[string]any{
		"search_1": []research.SearchHit{{URL: "https://discovery.example.net/page"}},
	}, failedURLs, userProvidedURLs(goal), false)
	if len(urls) != 1 || urls[0] != "https://docs.example.com/qwen" {
		t.Fatalf("retry candidates = %#v, want only the failed user-provided URL", urls)
	}

	registry.RememberFetchedURLs(taskID, map[string]any{
		"evidence_id": "evidence-user-url", "url": "https://docs.example.com/qwen", "requested_url": "https://docs.example.com/qwen",
	})
	urls = worker.availableFetchURLs(taskID, map[string]any{}, failedURLs, userProvidedURLs(goal), false)
	if contains(urls, "https://docs.example.com/qwen") {
		t.Fatalf("successfully fetched user URL was offered again: %#v", urls)
	}
}

func TestSearchRetryDecisionCountsDistinctFetchableSites(t *testing.T) {
	registry := tools.NewRegistry()
	registry.RememberFetchedURLs("task-search-floor", map[string]any{
		"evidence_id": "user-source", "url": "https://docs.example.com/protocol/",
	})
	worker := &Worker{tools: registry}
	state := checkpointState{}
	if worker.searchResultsMeetSourceFloor("task-search-floor", []research.SearchHit{{URL: "https://www.docs.example.com/another-page"}}, state, 2) {
		t.Fatal("same-site search hits incorrectly satisfied the two-site floor")
	}
	if !worker.searchResultsMeetSourceFloor("task-search-floor", []research.SearchHit{
		{URL: "https://www.docs.example.com/another-page"}, {URL: "https://reference.example.net/protocol"},
	}, state, 2) {
		t.Fatal("a supplied source plus one distinct candidate site did not satisfy the source floor")
	}
	state.FailedFetchURLs = []string{"https://reference.example.net/protocol"}
	if worker.searchResultsMeetSourceFloor("task-search-floor", []research.SearchHit{
		{URL: "https://reference.example.net/protocol"}, {URL: "https://benchmark.example.org/protocol"},
	}, state, 2) {
		t.Fatal("failed candidate URL was counted toward source sufficiency")
	}
}

func TestAvailableFetchURLsSkipsAlreadyFetchedSourceSite(t *testing.T) {
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{}); err != nil {
		t.Fatal(err)
	}
	registry.SeedTaskURLs("task-used-site", "Research a technical topic")
	registry.RememberURLs("task-used-site", []research.SearchHit{
		{URL: "https://same-source.com/already-fetched"},
		{URL: "https://www.same-source.com/another-page"},
		{URL: "https://second-source.net/document"},
		{URL: "https://third-source.org/reference"},
	})
	registry.RememberFetchedURLs("task-used-site", map[string]any{
		"evidence_id": "prior", "url": "https://same-source.com/already-fetched", "requested_url": "https://same-source.com/already-fetched",
	})
	worker := New(nil, &scriptedLLM{}, registry, Options{})
	urls := worker.availableFetchURLs("task-used-site", map[string]any{}, nil, nil, true)
	if len(urls) != 2 {
		t.Fatalf("new fetch candidates should include the two remaining available sites: %#v", urls)
	}
	for _, rawURL := range urls {
		if researchSourceSite(rawURL) == "same-source.com" {
			t.Fatalf("already fetched site was offered again: %#v", urls)
		}
	}
}

func TestResearchTaskWithoutSearchFailsClearlyBeforeCallingModel(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "research-unavailable.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{
		ID: "research-unavailable-task", Title: "Компьютеры", Description: "по ищи компьютеры в астане на бюджет 400к тенге",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim task: ok=%t err=%v", ok, err)
	}
	model := &scriptedLLM{}
	worker := New(store, model, tools.NewRegistry(), Options{MaxSteps: 12, TaskTimeout: time.Minute, MaxOutputTokens: 512})
	worker.runTask(ctx, claimed)

	failed, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != domain.StatusFailed {
		t.Fatalf("status = %q, want failed while search is unavailable", failed.Status)
	}
	if model.requests != 0 {
		t.Fatalf("planner was called %d times despite missing required search capability", model.requests)
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 100)
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
		if payload["error_code"] != "search_unavailable" {
			t.Fatalf("task.failed error_code = %#v", payload["error_code"])
		}
		if payload["failure_stage"] != "planning" || payload["failure_role"] != "planner_initial" || payload["planner_attempt"] != float64(1) || payload["validation_error"] != safePlannerReason(errSearchUnavailable) {
			t.Fatalf("missing safe planning diagnostics: %#v", payload)
		}
		summary, ok := payload["plan_summary"].(map[string]any)
		if !ok || summary["steps"] != float64(0) || summary["search"] != float64(0) || summary["fetch"] != float64(0) {
			t.Fatalf("unexpected pre-planner summary: %#v", payload["plan_summary"])
		}
		return
	}
	t.Fatal("missing task.failed event")
}

func TestPersistedPlanReplacementRequiresOnlyIdempotentUntouchedActions(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "plan-replacement.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := New(store, &scriptedLLM{}, tools.NewRegistry(), Options{MaxSteps: 12, TaskTimeout: time.Minute, MaxOutputTokens: 128})

	for _, test := range []struct {
		name             string
		tool             string
		approvalConsumed bool
		want             bool
	}{
		{name: "idempotent read action", tool: "text.inspect", want: true},
		{name: "unknown action", tool: "external.submit", want: false},
		{name: "consumed approval", tool: "text.inspect", approvalConsumed: true, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			taskID := "replace-" + strings.ReplaceAll(test.name, " ", "-")
			if _, err := store.CreateTask(ctx, domain.Task{ID: taskID, Title: test.name}, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveCheckpoint(ctx, taskID, 1, checkpointState{PlanVersion: 1, SearchQueries: 2, FetchedBytes: 123, CurrentStep: "step_1", CompletedSteps: []string{"step_1"}, ToolResults: map[string]any{"existing": map[string]any{"ok": true}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RecordEvent(ctx, taskID, "tool.started", nil, "user", map[string]any{"tool": test.tool}); err != nil {
				t.Fatal(err)
			}
			if test.approvalConsumed {
				if _, err := store.RecordEvent(ctx, taskID, "approval.consumed", nil, "audit", map[string]any{"approval_id": "approval-1"}); err != nil {
					t.Fatal(err)
				}
			}
			state, got, err := worker.canReplacePlan(ctx, taskID)
			if err != nil || got != test.want {
				t.Fatalf("canReplacePlan = %t, err=%v, want %t", got, err, test.want)
			}
			if state.SearchQueries != 2 || state.FetchedBytes != 123 || state.ToolResults["existing"] == nil {
				t.Fatalf("replaceable checkpoint did not preserve budget/result state: %#v", state)
			}
			reset := resetPlanCheckpoint(state)
			if reset.SearchQueries != 2 || reset.FetchedBytes != 123 || len(reset.ToolResults) != 0 || len(reset.CompletedSteps) != 0 || len(reset.HistoricalObservations) != 1 || reset.HistoricalObservations[0].ToolResults["existing"] == nil {
				t.Fatalf("replacement did not archive observations independently: %#v", reset)
			}
		})
	}
}

func TestWasExplicitFailedRetryDoesNotTreatPauseResumeAsRetry(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "failed-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := New(store, &scriptedLLM{}, tools.NewRegistry(), Options{MaxSteps: 12, TaskTimeout: time.Minute, MaxOutputTokens: 128})
	record := func(taskID string, from, to domain.TaskStatus) {
		t.Helper()
		if _, err := store.RecordEvent(ctx, taskID, "task.status_changed", nil, "user", map[string]any{"from": from, "to": to}); err != nil {
			t.Fatal(err)
		}
	}

	retryTaskID := "failed-retry-task"
	if _, err := store.CreateTask(ctx, domain.Task{ID: retryTaskID, Title: "Retry"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, retryTaskID, "task.failed", nil, "user", map[string]any{"error_code": "verification_incomplete"}); err != nil {
		t.Fatal(err)
	}
	record(retryTaskID, domain.StatusFailed, domain.StatusQueued)
	record(retryTaskID, domain.StatusQueued, domain.StatusPlanning)
	if retry, err := worker.wasExplicitFailedRetry(ctx, retryTaskID); err != nil || !retry {
		t.Fatalf("explicit failed retry = %t, err=%v", retry, err)
	}
	if code, err := worker.latestFailureCode(ctx, retryTaskID); err != nil || code != "verification_incomplete" {
		t.Fatalf("latest failed code = %q, err=%v", code, err)
	}

	resumeTaskID := "failed-resume-task"
	if _, err := store.CreateTask(ctx, domain.Task{ID: resumeTaskID, Title: "Resume"}, ""); err != nil {
		t.Fatal(err)
	}
	record(resumeTaskID, domain.StatusFailed, domain.StatusQueued)
	record(resumeTaskID, domain.StatusQueued, domain.StatusPlanning)
	record(resumeTaskID, domain.StatusRunning, domain.StatusPaused)
	record(resumeTaskID, domain.StatusPaused, domain.StatusQueued)
	record(resumeTaskID, domain.StatusQueued, domain.StatusPlanning)
	if retry, err := worker.wasExplicitFailedRetry(ctx, resumeTaskID); err != nil || retry {
		t.Fatalf("paused-task resume was misclassified as failed retry: retry=%t err=%v", retry, err)
	}

	recoveredTaskID := "failed-recovered-task"
	if _, err := store.CreateTask(ctx, domain.Task{ID: recoveredTaskID, Title: "Recovered retry"}, ""); err != nil {
		t.Fatal(err)
	}
	record(recoveredTaskID, domain.StatusFailed, domain.StatusQueued)
	record(recoveredTaskID, domain.StatusQueued, domain.StatusPlanning)
	if _, err := store.RecordEvent(ctx, recoveredTaskID, "task.recovered", nil, "user", map[string]any{"from": "running", "to": "queued"}); err != nil {
		t.Fatal(err)
	}
	record(recoveredTaskID, domain.StatusQueued, domain.StatusPlanning)
	if retry, err := worker.wasExplicitFailedRetry(ctx, recoveredTaskID); err != nil || retry {
		t.Fatalf("recovered retry was misclassified as fresh failed retry: retry=%t err=%v", retry, err)
	}
}
