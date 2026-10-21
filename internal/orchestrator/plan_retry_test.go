package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type attemptSearchStub struct {
	queries      []string
	emptyInitial bool
}

func (s *attemptSearchStub) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	s.queries = append(s.queries, options.Query)
	if s.emptyInitial && (len(s.queries) == 1 || len(s.queries) == 4) {
		return []research.SearchHit{}, nil
	}
	return researchSearchStub{}.Search(ctx, options)
}

type attemptFetcherStub struct {
	calls int
	bytes int64
}

func (*attemptFetcherStub) ValidateURL(context.Context, string) error { return nil }

func (f *attemptFetcherStub) Fetch(ctx context.Context, rawURL string, options research.FetchOptions) (research.FetchedDocument, error) {
	f.calls++
	doc, err := (researchFetcherStub{}).Fetch(ctx, rawURL, options)
	doc.ContentSHA256 = fmt.Sprintf("attempt-fetch-%d", f.calls)
	doc.Body = []byte(fmt.Sprintf("<html><body>observation %d</body></html>", f.calls))
	f.bytes += int64(len(doc.Body))
	return doc, err
}

func TestManualVerificationRetryUsesFreshPlanObservations(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		emptyInitial   bool
		duplicateFacet bool
		budget         int
		wantCode       string
	}{
		{name: "repeat original query", budget: 4},
		{name: "old retry does not suppress new broadening", emptyInitial: true, budget: 6},
		{name: "same attempt duplicate rejected", duplicateFacet: true, budget: 4, wantCode: "invalid_model_output"},
		{name: "lifetime query budget enforced", emptyInitial: true, budget: 5, wantCode: "search_budget_exceeded"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "retry.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			artifactStore, err := artifacts.New(t.TempDir(), 1_000_000)
			if err != nil {
				t.Fatal(err)
			}
			search := &attemptSearchStub{emptyInitial: scenario.emptyInitial}
			fetcher := &attemptFetcherStub{}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: search, Fetcher: fetcher, Extractor: researchExtractorStub{}, Artifacts: artifactStore, Repository: store, MaxFetchBytes: 100_000}); err != nil {
				t.Fatal(err)
			}
			base := integrationResearchModel()
			var oldIDs []string
			model := &scriptedLLM{respond: func(request llm.ChatRequest, index int) (string, error) {
				local := index
				if index >= 6 {
					local -= 6
				}
				if index >= 6 && local > 0 {
					for _, id := range oldIDs {
						if strings.Contains(request.Messages[1].Content, id) {
							t.Errorf("historical evidence leaked into active model input: %s", id)
						}
					}
				}
				if index == 7 {
					var input struct {
						Prior []string `json:"prior_search_queries_untrusted"`
					}
					if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
						return "", err
					}
					if len(input.Prior) != 0 {
						t.Errorf("new attempt inherited prompt history: %v", input.Prior)
					}
					if scenario.emptyInitial {
						return `{"status":"continue","summary":"Search again","tool_call":{"name":"search","arguments":{"query":"protocol architecture alternate specification details","language":null,"recency":null,"max_results":8}}}`, nil
					}
				}
				if index == 8 && scenario.duplicateFacet {
					return base.respond(request, 1)
				}
				response, err := base.respond(request, local)
				if index == 5 && err == nil {
					var verdict map[string]any
					if err := json.Unmarshal([]byte(response), &verdict); err != nil {
						return "", err
					}
					verdict["ready_to_answer"] = false
					verdict["missing"] = []string{"The inspected sources lack the required compatibility information."}
					encoded, err := json.Marshal(verdict)
					return string(encoded), err
				}
				return response, err
			}}
			worker := New(store, model, registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxSearchQueries: scenario.budget})
			task, err := store.CreateTask(ctx, domain.Task{ID: "manual-retry", Title: "Protocol review", Description: "Research protocol architecture and limitations."}, "")
			if err != nil {
				t.Fatal(err)
			}
			run := func() domain.Task {
				t.Helper()
				if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
					t.Fatal(err)
				}
				claimed, ok, err := store.ClaimNext(ctx)
				if err != nil || !ok {
					t.Fatalf("claim: ok=%t err=%v", ok, err)
				}
				worker.runTask(ctx, claimed)
				current, err := store.GetTask(ctx, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				return current
			}
			if current := run(); current.Status != domain.StatusFailed {
				t.Fatalf("first attempt status=%s", current.Status)
			}
			if code, err := worker.latestFailureCode(ctx, task.ID); err != nil || code != "verification_incomplete" {
				t.Fatalf("first failure=%s err=%v", code, err)
			}
			first, err := worker.currentCheckpoint(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			oldIDs = collectEvidenceIDs(first.ToolResults)
			if fetcher.calls != 2 || len(oldIDs) != 2 {
				t.Fatalf("first attempt fetches=%d evidence=%v", fetcher.calls, oldIDs)
			}
			current := run()
			state, err := worker.currentCheckpoint(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.PlanAttempt != 1 || len(state.HistoricalObservations) != 1 || len(collectEvidenceIDs(state.HistoricalObservations[0].ToolResults)) != 2 {
				t.Fatalf("history not archived: %#v", state)
			}
			if state.FetchedBytes != fetcher.bytes || state.SearchQueries != len(search.queries) || len(state.LifetimeSearchQueryHistory) != len(search.queries) {
				t.Fatalf("lifetime accounting: bytes=%d/%d queries=%d/%d history=%v", state.FetchedBytes, fetcher.bytes, state.SearchQueries, len(search.queries), state.LifetimeSearchQueryHistory)
			}
			if scenario.wantCode != "" {
				code, err := worker.latestFailureCode(ctx, task.ID)
				if err != nil || code != scenario.wantCode || current.Status != domain.StatusFailed {
					t.Fatalf("retry status=%s code=%s err=%v", current.Status, code, err)
				}
				if fetcher.calls != 2 {
					t.Fatalf("unexpected retry fetches=%d", fetcher.calls)
				}
				return
			}
			if current.Status != domain.StatusCompleted || fetcher.calls != 4 || len(state.SkippedSteps) != 0 {
				t.Fatalf("retry status=%s fetches=%d skipped=%v", current.Status, fetcher.calls, state.SkippedSteps)
			}
			messages, err := store.Messages(ctx, task.ID)
			if err != nil || len(messages) == 0 {
				t.Fatalf("final messages=%v err=%v", messages, err)
			}
			for _, id := range oldIDs {
				if strings.Contains(messages[len(messages)-1].Content, id) {
					t.Errorf("historical evidence cited in new answer: %s", id)
				}
			}
			for _, id := range collectEvidenceIDs(state.ToolResults) {
				if contains(oldIDs, id) {
					t.Errorf("old evidence adopted: %s", id)
				}
			}
			evidence, err := store.Evidence(ctx, task.ID)
			if err != nil || len(evidence) != 4 {
				t.Fatalf("stored lifetime evidence=%d err=%v", len(evidence), err)
			}
			if scenario.emptyInitial {
				key := state.conditionalSearchRetryResultKey("search_1")
				if state.ToolResults[key] == nil || state.HistoricalObservations[0].ToolResults[conditionalSearchRetryResultKey("search_1")] == nil || len(search.queries) != 6 || search.queries[4] != broadenResearchQuery(search.queries[3]) {
					t.Fatalf("retry was not scoped to attempt: queries=%v state=%#v", search.queries, state)
				}
			} else if search.queries[0] != search.queries[2] {
				t.Fatalf("original query was not repeated: %v", search.queries)
			}
		})
	}
}

func TestReplannedPendingSearchDoesNotRecoverHistoricalResult(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "pending-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "pending-retry", Title: "Research protocol"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"tool.started", "tool.completed"} {
		if _, err := store.RecordEvent(ctx, task.ID, kind, nil, "user", map[string]any{"step_id": "search_1", "tool": "search", "result": []any{}}); err != nil {
			t.Fatal(err)
		}
	}
	worker := New(store, nil, tools.NewRegistry(), Options{})
	state, err := worker.resetPlanCheckpoint(ctx, task.ID, checkpointState{
		SearchQueries: 1, FetchedBytes: 123,
		SearchQueryHistory: []string{"protocol architecture"},
		SearchQueryByStep:  map[string]string{"search_1": "protocol architecture"},
		ToolResults:        map[string]any{"search_1": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentStep = "search_1"
	state.PendingTool = "search"
	state.PendingToolIdempotent = true
	state.PendingArguments = json.RawMessage(`{"query":"protocol architecture"}`)
	if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := worker.reconcileResearchState(ctx, task.ID, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.ToolResults) != 0 || len(recovered.CompletedSteps) != 0 || recovered.PendingTool != "" || recovered.SearchQueries != 1 || recovered.FetchedBytes != 123 {
		t.Fatalf("prior attempt completion replayed: %#v", recovered)
	}
	if len(recovered.SearchQueryHistory) != 0 || len(recovered.SearchQueryByStep) != 0 || len(recovered.LifetimeSearchQueryHistory) != 1 || recovered.HistoricalObservations[0].SearchQueryByStep["search_1"] != "protocol architecture" {
		t.Fatalf("query histories not separated: %#v", recovered)
	}
}
