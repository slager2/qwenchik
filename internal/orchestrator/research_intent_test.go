package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
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
		"Count occurrences of the word 'research' in the supplied paragraph",
		"Сосчитай слова в строке раз два три",
		"Найди слово в предоставленном тексте",
		"Summarize the text provided below",
	} {
		if requiresWebResearch(value) {
			t.Errorf("requiresWebResearch(%q) = true", value)
		}
	}
}

func TestGenericResearchDecomposesIntoBoundedSearchAndFetchSteps(t *testing.T) {
	goal := "Research the architecture, operating behavior, and limitations of an unfamiliar protocol."
	if got := minimumResearchSearches(goal); got != 3 {
		t.Fatalf("research search-question count = %d, want 3", got)
	}
	if got := minimumResearchFetches(goal); got != 3 {
		t.Fatalf("research source count = %d, want 3", got)
	}
	if got := requiredPlanSteps("Technical review", goal); got != 6 {
		t.Fatalf("research plan size = %d, want 3 discovery steps plus 3 fetches", got)
	}
	if got := requiredPlanSteps("Inspect supplied document", "Summarize https://example.org/document"); got != 1 {
		t.Fatalf("user-URL plan size = %d, want one fetch", got)
	}
	gotKinds := requiredResearchKinds("Technical review", goal)
	wantKinds := []string{"search", "search", "search", "fetch", "fetch", "fetch"}
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

func TestResearchRequestWithUserURLKeepsURLFirstAndAddsDiscovery(t *testing.T) {
	title := "Compare two sources"
	goal := "Compare https://docs.example.org/current with other current information."
	if got := minimumResearchSearches(title + "\n" + goal); got != 3 {
		t.Fatalf("URL-plus-research search count = %d, want 3", got)
	}
	if got := minimumResearchFetches(title + "\n" + goal); got != 3 {
		t.Fatalf("URL-plus-research source count = %d, want supplied URL plus two discovered sources", got)
	}
	want := []string{"fetch", "search", "search", "search", "fetch", "fetch"}
	got := requiredResearchKinds(title, goal)
	if len(got) != len(want) {
		t.Fatalf("URL-plus-research required kinds = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("URL-plus-research required kinds = %#v, want %#v", got, want)
		}
	}
	if got := requiredPlanSteps(title, goal); got != 6 {
		t.Fatalf("URL-plus-research baseline plan steps = %d, want 6", got)
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
		},
	}, nil, nil, true)
	if len(urls) != 3 || researchSourceSite(urls[0]) == researchSourceSite(urls[1]) || researchSourceSite(urls[1]) == researchSourceSite(urls[2]) || researchSourceSite(urls[0]) == researchSourceSite(urls[2]) {
		t.Fatalf("fetch candidates are not distinct sites: %#v", urls)
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
		t.Fatalf("new fetch candidates reuse an already fetched source site: %#v", urls)
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
		ID: "research-unavailable-task", Title: "Technical research", Description: "Investigate a current protocol compatibility issue.",
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
