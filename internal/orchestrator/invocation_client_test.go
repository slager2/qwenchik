package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
)

func invocationFixtureRequest() llm.ChatRequest {
	return llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "system", Content: "Return the schema object"}, {Role: "user", Content: "compact input"}}, Model: "local-qwen", BaseURL: "http://127.0.0.1:8000/v1", MaxTokens: 64, TemperatureSet: true, TopP: 0.8, TopPSet: true, JSONSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)}
}

func TestInvocationCompletionCacheSurvivesReopenWithoutNewCharge(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research recovery", nil)
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) { return `{"ok":true}`, nil }}
	request := invocationFixtureRequest()
	client := newInvocationClient(f.store, model, f.task.ID)
	first, err := client.Complete(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	before := researchRunSession(t, f.store, f.task.ID).Counters
	f.reopen(t)
	client = newInvocationClient(f.store, model, f.task.ID)
	second, err := client.Complete(ctx, request)
	if err != nil || second.Content != first.Content || len(model.calls()) != 1 || researchRunSession(t, f.store, f.task.ID).Counters != before {
		t.Fatalf("cached=%+v err=%v calls=%d", second, err, len(model.calls()))
	}
}

func TestInvocationAbandonedJSONRetryCannotRepeatAfterSecondCrash(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research recovery", nil)
	request := invocationFixtureRequest()
	hash, err := modelInvocationHash(request)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		inv, cached, err := prepareInvocation(ctx, f.store, f.task.ID, "llm.complete", "model", "", hash)
		if err != nil || cached || inv.Attempt != attempt {
			t.Fatalf("attempt=%+v cached=%v err=%v", inv, cached, err)
		}
		f.reopen(t)
	}
	model := &researchRunModel{}
	_, err = newInvocationClient(f.store, model, f.task.ID).Complete(ctx, request)
	if !errors.Is(err, ErrRecoveryRequired) || len(model.calls()) != 0 || researchRunSession(t, f.store, f.task.ID).Counters.LLMCalls != 2 {
		t.Fatalf("third attempt was not stopped: %v", err)
	}
}

func TestInvocationInvalidJSONDoesNotPersistReasoningOrRetryForever(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research recovery", nil)
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) {
		return "PRIVATE_REASONING not valid JSON", nil
	}}
	request := invocationFixtureRequest()
	response, err := newInvocationClient(f.store, model, f.task.ID).Complete(ctx, request)
	if err != nil || !strings.Contains(response.Content, "PRIVATE_REASONING") {
		t.Fatal("caller lost its single in-memory repair opportunity")
	}
	events, err := f.store.EventsAfter(ctx, f.task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "PRIVATE_REASONING") {
			t.Fatal("invalid model text was persisted")
		}
	}
	f.reopen(t)
	_, err = newInvocationClient(f.store, model, f.task.ID).Complete(ctx, request)
	if !errors.Is(err, llm.ErrInvalidModelOutput) || len(model.calls()) != 1 {
		t.Fatalf("known invalid output was regenerated: %v", err)
	}
}

func TestInvocationToolOutcomeRecoversBeforeStepCommitWithoutDoubleBytes(t *testing.T) {
	ctx := context.Background()
	store, worker, task, _, _ := newInterruptedFetch(t, ctx)
	defer store.Close()
	const source = "https://docs.example.com/page"
	body := "<main>Durable source text for recovery without another HTTP request.</main>"
	fetcher := &extractionFallbackFetcher{bodies: map[string]string{source: body}, calls: map[string]int{}}
	artifactStore, err := artifacts.New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{Fetcher: fetcher, Extractor: research.NewHTMLExtractorWithPDF(10000, nil), Artifacts: artifactStore, Repository: store, MaxFetchBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	registry.SeedTaskURLs(task.ID, source)
	worker.tools = registry
	ctx = tools.WithTaskID(ctx, task.ID)
	stage := domain.StageFetch
	if _, err := worker.executeObservedTool(ctx, task.ID, &stage, "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/page","original_query":"research"}`), "", "fetch_1"); err != nil {
		t.Fatal(err)
	}
	state, err := worker.currentCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, changed, err := worker.reconcileResearchState(ctx, task.ID, state)
	if err != nil || !changed || recovered.PendingTool != "" || !contains(recovered.CompletedSteps, "fetch_1") || recovered.FetchedBytes != int64(len(body)) {
		t.Fatalf("recovered=%+v changed=%v err=%v", recovered, changed, err)
	}
	if _, err := store.SaveCheckpoint(ctx, task.ID, 1, recovered); err != nil {
		t.Fatal(err)
	}
	second, _, err := worker.reconcileResearchState(ctx, task.ID, recovered)
	if err != nil || second.FetchedBytes != int64(len(body)) || fetcher.calls[source] != 1 {
		t.Fatalf("duplicate work/charge: bytes=%d calls=%d err=%v", second.FetchedBytes, fetcher.calls[source], err)
	}
}
