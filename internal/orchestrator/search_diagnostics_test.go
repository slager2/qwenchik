package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type diagnosticSearchStub struct {
	report research.SearchReport
	err    error
	calls  int
}

func (s *diagnosticSearchStub) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	report, err := s.SearchWithDiagnostics(ctx, options)
	return report.Hits, err
}

func (s *diagnosticSearchStub) SearchWithDiagnostics(context.Context, research.SearchOptions) (research.SearchReport, error) {
	s.calls++
	return s.report, s.err
}

type providerEventRepository struct {
	Repository
	err         error
	events      []map[string]any
	queryEvents int
}

func (r *providerEventRepository) RecordEvent(ctx context.Context, taskID, kind string, stage *domain.Stage, visibility string, payload map[string]any) (domain.Event, error) {
	if kind != "search.provider" && kind != "query.updated" || visibility != "user" || stage == nil || *stage != domain.StageSearch {
		return domain.Event{}, errors.New("unexpected provider event envelope")
	}
	if kind == "query.updated" {
		r.queryEvents++
		return domain.Event{}, nil
	}
	r.events = append(r.events, payload)
	return domain.Event{}, r.err
}

func TestSearchObserverRecordsSafeOutcomesAndPropagatesPersistenceFailure(t *testing.T) {
	for _, failEvent := range []bool{false, true} {
		outcome := research.ProviderOutcome{Provider: "searxng", State: "blocked", Code: "provider_blocked", Message: "secret query token=SECRET", HTTPStatus: 403, CheckedAt: time.Now().UTC(), LatencyMS: 12}
		providerErr := &research.SearchFailure{Code: "search_unavailable", Outcomes: []research.ProviderOutcome{outcome}}
		provider := &diagnosticSearchStub{report: research.SearchReport{Outcomes: []research.ProviderOutcome{outcome}}, err: providerErr}
		registry := tools.NewRegistry()
		if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider}); err != nil {
			t.Fatal(err)
		}
		persistErr := errors.New("provider event storage failed")
		repo := &providerEventRepository{}
		if failEvent {
			repo.err = persistErr
		}
		worker := New(repo, nil, registry, Options{})
		stage := domain.StageSearch
		ctx := context.Background()
		_, err := worker.executeObservedTool(ctx, "task", &stage, "researcher", "search", json.RawMessage(`{"query":"private user query"}`), "")
		if failEvent && !errors.Is(err, persistErr) || !failEvent && !errors.Is(err, research.ErrSearchUnavailable) {
			t.Fatalf("provider execution failure = %v", err)
		}
		if ctx.Err() != nil || provider.calls != 1 || len(repo.events) != 1 || repo.queryEvents != 1 {
			t.Fatalf("observer execution: calls=%d events=%d ctx=%v", provider.calls, len(repo.events), ctx.Err())
		}
		encoded, _ := json.Marshal(repo.events[0])
		if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "private user query") || repo.events[0]["http_status"] != 403 {
			t.Fatalf("unsafe or incomplete provider event: %s", encoded)
		}
	}
}

func TestProviderFailuresEndTaskWithSafeOutcomeMetadataWithoutRetry(t *testing.T) {
	for _, code := range []string{"provider_blocked", "provider_malformed", "provider_misconfigured", "provider_rate_limited", "search_filtered_by_policy", "search_no_results"} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			store, err := sqlite.Open(ctx, t.TempDir()+"/provider.db")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			task, err := store.CreateTask(ctx, domain.Task{ID: "provider-failure", Title: "Protocol", Description: "объясни устройство протокола"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := store.ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim: %t %v", ok, err)
			}
			plan := domain.Plan{SchemaVersion: 1, Goal: task.Description, MaxSteps: 4, FinalDeliverable: "Supported explanation", Subtasks: []domain.PlanStep{
				{ID: "search_1", Kind: "search", Description: "Protocol architecture", SuccessCriteria: []string{"hits recorded"}},
				{ID: "search_2", Kind: "search", Description: "Protocol limitations", SuccessCriteria: []string{"hits recorded"}},
				{ID: "fetch_1", Kind: "fetch", Description: "Inspect first source", Dependencies: []string{"search_1", "search_2"}, SuccessCriteria: []string{"evidence stored"}},
				{ID: "fetch_2", Kind: "fetch", Description: "Inspect second source", Dependencies: []string{"search_1", "search_2"}, SuccessCriteria: []string{"evidence stored"}},
			}}
			if _, err := store.SavePlan(ctx, task.ID, plan, checkpointState{ToolResults: map[string]any{}}); err != nil {
				t.Fatal(err)
			}
			outcome := research.ProviderOutcome{Provider: "searxng", State: "blocked", Code: code, Message: "secret query", HTTPStatus: 403, Retryable: code == "provider_rate_limited"}
			failureCode := "search_unavailable"
			if code == "search_filtered_by_policy" || code == "search_no_results" {
				failureCode = code
			}
			provider := &diagnosticSearchStub{report: research.SearchReport{Outcomes: []research.ProviderOutcome{outcome}}, err: &research.SearchFailure{Code: failureCode, Outcomes: []research.ProviderOutcome{outcome}}}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider}); err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(tools.Descriptor{Name: "fetch_url", ArgumentsSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), RequiredCapability: "research.fetch", RiskClass: "read", TimeoutSeconds: 1, MaxOutputBytes: 1024, AllowedRoles: []string{"researcher"}}, func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("fetch must not execute") }); err != nil {
				t.Fatal(err)
			}
			model := &scriptedLLM{responses: []string{`{"status":"continue","summary":"Searching protocol architecture","tool_call":{"name":"search","arguments":{"query":"private query","language":null,"recency":null,"max_results":8}}}`}}
			worker := New(store, model, registry, Options{})
			worker.runTask(ctx, claimed)
			current, err := store.GetTask(ctx, task.ID)
			if err != nil || current.Status != domain.StatusFailed || provider.calls != 1 || model.requests != 1 {
				t.Fatalf("failure status=%s calls=%d model=%d err=%v", current.Status, provider.calls, model.requests, err)
			}
			events, err := store.EventsAfter(ctx, task.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.Type != "task.failed" {
					continue
				}
				var payload struct {
					Code          string           `json:"error_code"`
					Outcomes      []map[string]any `json:"provider_outcomes"`
					Alias         []map[string]any `json:"outcomes"`
					Retryable     *bool            `json:"retryable"`
					DiagnosticsID string           `json:"diagnostics_id"`
					Details       map[string]any   `json:"details"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Code != code || len(payload.Outcomes) != 1 || strings.Contains(string(event.Payload), "secret query") {
					t.Fatalf("failure payload = %s", event.Payload)
				}
				if payload.Retryable == nil || payload.DiagnosticsID == "" || len(payload.Details) == 0 || len(payload.Alias) != 1 {
					t.Fatalf("failure diagnostics missing: %s", event.Payload)
				}
				if *payload.Retryable != (code == "provider_rate_limited") {
					t.Fatalf("failure retryability lost: %s", event.Payload)
				}
				found = true
			}
			if !found {
				t.Fatal("task failure event missing")
			}
		})
	}
}

type rejectSyntheticStepRepository struct{ Repository }

func (r rejectSyntheticStepRepository) SetStepStatus(ctx context.Context, taskID, stepID, status string) error {
	if isConditionalSearchRetryStepID(stepID) {
		return errors.New("synthetic retry has no planned SQLite step")
	}
	return r.Repository.SetStepStatus(ctx, taskID, stepID, status)
}

func TestAllEmptySearchFacetsTerminateWithoutVerificationAndRecoverRetry(t *testing.T) {
	for _, recoveryPoint := range []string{"fresh", "after retry start", "after retry completion"} {
		t.Run(recoveryPoint, func(t *testing.T) {
			recoverRetry := recoveryPoint != "fresh"
			ctx := context.Background()
			store, err := sqlite.Open(ctx, t.TempDir()+"/empty.db")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			task, err := store.CreateTask(ctx, domain.Task{ID: "empty-search", Title: "Protocol", Description: "Explain protocol architecture and limitations"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := store.ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim: %t %v", ok, err)
			}
			plan := domain.Plan{SchemaVersion: 1, Goal: task.Description, MaxSteps: 4, FinalDeliverable: "Supported explanation", Subtasks: []domain.PlanStep{
				{ID: "search_1", Kind: "search", Description: "Protocol architecture", SuccessCriteria: []string{"hits recorded"}},
				{ID: "search_2", Kind: "search", Description: "Protocol limitations", SuccessCriteria: []string{"hits recorded"}},
				{ID: "fetch_1", Kind: "fetch", Description: "First source", Dependencies: []string{"search_1", "search_2"}, SuccessCriteria: []string{"evidence stored"}},
				{ID: "fetch_2", Kind: "fetch", Description: "Second source", Dependencies: []string{"search_1", "search_2"}, SuccessCriteria: []string{"evidence stored"}},
			}}
			state := checkpointState{PlanVersion: 1, ToolResults: map[string]any{}}
			if _, err := store.SavePlan(ctx, task.ID, plan, state); err != nil {
				t.Fatal(err)
			}
			stage := domain.StageSearch
			if recoverRetry {
				state.CompletedSteps = []string{"search_1"}
				state.ToolResults["search_1"] = []any{}
				state.SearchQueries = 2
				state.SearchQueryHistory = []string{"unfamiliar protocol rare architecture", "unfamiliar protocol"}
				retryID := conditionalSearchRetryResultKey("search_1")
				state.SearchQueryByStep = map[string]string{"search_1": state.SearchQueryHistory[0], retryID: state.SearchQueryHistory[1]}
				state.CurrentStep = retryID
				state.PendingTool = "search"
				state.PendingToolIdempotent = true
				state.PendingArguments = json.RawMessage(`{"query":"unfamiliar protocol","max_results":8}`)
				for _, id := range []string{"search_1", retryID} {
					if _, err := store.RecordEvent(ctx, task.ID, "tool.started", &stage, "user", map[string]any{"step_id": id, "tool": "search"}); err != nil {
						t.Fatal(err)
					}
					if id == "search_1" {
						if _, err := store.RecordEvent(ctx, task.ID, "tool.completed", &stage, "user", map[string]any{"step_id": id, "tool": "search", "result": []any{}}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := store.SetStepStatus(ctx, task.ID, "search_1", "completed"); err != nil {
					t.Fatal(err)
				}
				if recoveryPoint == "after retry completion" {
					if _, err := store.RecordEvent(ctx, task.ID, "tool.completed", &stage, "user", map[string]any{"step_id": retryID, "tool": "search", "result": []any{}}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
					t.Fatal(err)
				}
				if recoveryPoint == "after retry start" {
					if _, err := store.RecordEvent(ctx, task.ID, "tool.completed", &stage, "user", map[string]any{"step_id": retryID, "tool": "search", "result": []any{}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			provider := &diagnosticSearchStub{report: research.SearchReport{Hits: []research.SearchHit{}, Outcomes: []research.ProviderOutcome{{Provider: "searxng", State: "degraded", Code: "search_no_results", HTTPStatus: 200, BodySize: 52, ContentType: "application/json", ParserVersion: "searxng-json-v1"}}}}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider}); err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(tools.Descriptor{Name: "fetch_url", ArgumentsSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), RequiredCapability: "research.fetch", RiskClass: "read", TimeoutSeconds: 1, MaxOutputBytes: 1024, AllowedRoles: []string{"researcher"}}, func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("fetch must not execute") }); err != nil {
				t.Fatal(err)
			}
			responses := []string{
				`{"status":"continue","summary":"Search architecture","tool_call":{"name":"search","arguments":{"query":"protocol architecture rare phrase","language":null,"recency":null,"max_results":8}}}`,
				`{"status":"continue","summary":"Search limitations","tool_call":{"name":"search","arguments":{"query":"protocol limitations independent facet","language":null,"recency":null,"max_results":8}}}`,
			}
			if recoverRetry {
				responses = responses[1:]
			}
			model := &scriptedLLM{responses: responses}
			worker := New(rejectSyntheticStepRepository{store}, model, registry, Options{})
			worker.runTask(ctx, claimed)
			current, err := store.GetTask(ctx, task.ID)
			if err != nil || current.Status != domain.StatusFailed {
				t.Fatalf("status=%s err=%v", current.Status, err)
			}
			wantCalls := 3
			if recoverRetry {
				wantCalls = 1
			}
			if provider.calls != wantCalls || model.requests != len(responses) {
				t.Fatalf("provider calls=%d model calls=%d", provider.calls, model.requests)
			}
			checkpoint, err := worker.currentCheckpoint(ctx, task.ID)
			if err != nil || checkpoint.SearchQueries != 3 || !searchesExhaustedWithoutResults(plan, checkpoint) {
				t.Fatalf("checkpoint=%#v err=%v", checkpoint, err)
			}
			events, err := store.EventsAfter(ctx, task.ID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			starts, foundFailure := 0, false
			for _, event := range events {
				if event.Type == "tool.started" && strings.Contains(string(event.Payload), "conditional-search-retry:") {
					starts++
				}
				if event.Stage != nil && (*event.Stage == domain.StageVerification || *event.Stage == domain.StageFinalization) {
					t.Fatalf("empty search reached %s", *event.Stage)
				}
				if event.Type == "search.provider" && (event.Stage == nil || *event.Stage != domain.StageSearch) {
					t.Fatal("provider event lost search stage")
				}
				if event.Type == "task.failed" {
					var payload map[string]any
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					if payload["error_code"] != "search_no_results" || payload["retryable"] != false || payload["diagnostics_id"] == "" {
						t.Fatalf("failure=%s", event.Payload)
					}
					foundFailure = true
				}
			}
			if starts != 1 || !foundFailure {
				t.Fatalf("retry starts=%d failure=%t", starts, foundFailure)
			}
		})
	}
}

func TestSafeSearchOutcomePreservesDiagnosticMetadata(t *testing.T) {
	for _, state := range []string{"malformed", "configured"} {
		out := safeSearchOutcome(research.ProviderOutcome{Provider: "searxng", State: state, Code: "provider_malformed", Message: "secret raw query", BodySize: 123, ContentType: "application/json; token=SECRET", ParserVersion: "searxng-json-v1"})
		if out["state"] != state || out["body_size"] != int64(123) || out["content_type"] != "application/json" || out["parser_version"] != "searxng-json-v1" {
			t.Fatalf("metadata=%v", out)
		}
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "secret raw query") {
			t.Fatalf("unsafe metadata: %s", encoded)
		}
	}
}

func TestObservedClassifiedProviderFailureKeepsOutcomeMetadata(t *testing.T) {
	outcome := research.ProviderOutcome{Provider: "searxng", State: "malformed", Code: "provider_malformed", BodySize: 432, ContentType: "application/json", ParserVersion: "searxng-json-v1"}
	provider := &diagnosticSearchStub{report: research.SearchReport{Outcomes: []research.ProviderOutcome{outcome}}, err: &research.ClassifiedError{Code: "provider_malformed", State: "malformed"}}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider}); err != nil {
		t.Fatal(err)
	}
	worker := New(&providerEventRepository{}, nil, registry, Options{})
	stage := domain.StageSearch
	_, err := worker.executeObservedTool(context.Background(), "task", &stage, "researcher", "search", json.RawMessage(`{"query":"private query"}`), "")
	code, outcomes := searchFailureMetadata(err)
	if code != "provider_malformed" || len(outcomes) != 1 || outcomes[0]["body_size"] != int64(432) || !errors.Is(err, research.ErrSearchUnavailable) {
		t.Fatalf("classified failure code=%q outcomes=%v err=%v", code, outcomes, err)
	}
}
