package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/app"
	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type researchRunFixture struct {
	store *sqlite.Store
	path  string
	turn  domain.SubmitResponse
	task  domain.Task
}

func newResearchRunStore(t *testing.T) *researchRunFixture {
	t.Helper()
	f := &researchRunFixture{path: filepath.Join(t.TempDir(), "research-run.db")}
	var err error
	f.store, err = sqlite.Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	return f
}

func researchRunDefaults() domain.ResearchSession {
	policy := domain.DefaultResearchPolicy()
	policy.Budget.MaxSteps, policy.Budget.MaxSearchQueries = 8, 4
	return domain.ResearchSession{
		Profile: policy.Profile, Policy: policy, Budget: policy.Budget,
		ModelSnapshot: domain.ModelProfileSnapshot{
			Version: 1, Model: "saved-local-model", BaseURL: "http://127.0.0.1:8001/v1",
			ContextLength: 65536, MaxOutputTokens: 2048, Temperature: 0, TopP: 0.45,
			StopSequences: []string{}, PromptVersion: "local-research-v1", SchemaVersion: 1,
		},
	}
}

func (f *researchRunFixture) submit(t *testing.T, goal string, edit func(*domain.ResearchSession)) domain.ResearchSession {
	t.Helper()
	ctx := context.Background()
	defaults := researchRunDefaults()
	if edit != nil {
		edit(&defaults)
	}
	defaults.Policy.Profile, defaults.Policy.Budget = defaults.Profile, defaults.Budget
	conversation, err := f.store.CreateConversation(ctx, domain.Conversation{Title: "Supplied task", Mode: "research"})
	if err != nil {
		t.Fatal(err)
	}
	f.turn, err = f.store.SubmitTurnWithResearchDefaults(ctx, domain.SubmitTurn{ConversationID: conversation.ID, Content: goal}, defaults, "")
	if err != nil {
		t.Fatal(err)
	}
	f.claim(t)
	return researchRunSession(t, f.store, f.turn.Run.ID)
}

func (f *researchRunFixture) claim(t *testing.T) {
	t.Helper()
	var ok bool
	var err error
	f.task, ok, err = f.store.ClaimNext(context.Background())
	if err != nil || !ok || f.task.ID != f.turn.Run.ID || f.task.Status != domain.StatusPlanning {
		t.Fatalf("claim: task=%s status=%s ok=%t error=%v", f.task.ID, f.task.Status, ok, err)
	}
}

func (f *researchRunFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
}

func researchRunSession(t *testing.T, store *sqlite.Store, taskID string) domain.ResearchSession {
	t.Helper()
	session, err := store.GetResearchSession(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

type researchRunModelCall struct {
	request   llm.ChatRequest
	streaming bool
	deadline  time.Time
}

type researchRunModel struct {
	mu      sync.Mutex
	seen    []researchRunModelCall
	respond func(context.Context, llm.ChatRequest, int, bool) (string, error)
	stream  func(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error)
}

func (m *researchRunModel) record(ctx context.Context, request llm.ChatRequest, streaming bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	request.Messages = append([]llm.ChatMessage(nil), request.Messages...)
	request.Stop = append([]string(nil), request.Stop...)
	deadline, _ := ctx.Deadline()
	index := len(m.seen)
	m.seen = append(m.seen, researchRunModelCall{request: request, streaming: streaming, deadline: deadline})
	return index
}

func (m *researchRunModel) calls() []researchRunModelCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]researchRunModelCall(nil), m.seen...)
}

func (m *researchRunModel) Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	index := m.record(ctx, request, false)
	if m.respond == nil {
		return llm.ChatResponse{Content: "{}"}, nil
	}
	content, err := m.respond(ctx, request, index, false)
	return llm.ChatResponse{Content: content}, err
}

func (m *researchRunModel) Stream(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	index := m.record(ctx, request, true)
	if m.stream != nil {
		return m.stream(ctx, request)
	}
	content := "{}"
	if m.respond != nil {
		var err error
		content, err = m.respond(ctx, request, index, true)
		if err != nil {
			return nil, err
		}
	}
	events := make(chan llm.ChatEvent, 1)
	events <- llm.ChatEvent{Text: content}
	close(events)
	return events, nil
}

func callResearchRunModel(ctx context.Context, client llm.Client, request llm.ChatRequest, streaming bool) error {
	if !streaming {
		_, err := client.Complete(ctx, request)
		return err
	}
	events, err := client.Stream(ctx, request)
	if err != nil {
		return err
	}
	for event := range events {
		err = errors.Join(err, event.Err)
	}
	return err
}

func assertResearchRunRequest(t *testing.T, request llm.ChatRequest, model domain.ModelProfileSnapshot, temperature float64, maxTokens int) {
	t.Helper()
	if request.Model != model.Model || request.BaseURL != model.BaseURL || request.MaxTokens != maxTokens || request.Temperature != temperature || !request.TemperatureSet || request.TopP != model.TopP || !request.TopPSet || !slices.Equal(request.Stop, model.StopSequences) {
		t.Errorf("snapshot request mismatch: model=%t endpoint=%t max_tokens=%d temperature=%g set=%t top_p=%g set=%t stops=%v", request.Model == model.Model, request.BaseURL == model.BaseURL, request.MaxTokens, request.Temperature, request.TemperatureSet, request.TopP, request.TopPSet, request.Stop)
	}
	if request.BeforeFallbackAttempt == nil {
		t.Error("research request has no fallback reservation callback")
	}
}

func assertResearchRunTerminal(t *testing.T, f *researchRunFixture, status domain.TaskStatus, code string) {
	t.Helper()
	run, err := f.store.GetRun(context.Background(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := researchRunSession(t, f.store, run.ID)
	if run.Status != status || run.ErrorCode != code || run.CompletedAt == nil || session.Status != string(status) {
		t.Fatalf("run status=%s code=%q completed=%t session status=%s, want %s/%q", run.Status, run.ErrorCode, run.CompletedAt != nil, session.Status, status, code)
	}
	if code != "" && session.StopReason != code {
		t.Errorf("session stop reason=%q, want %q", session.StopReason, code)
	}
}

func TestResearchRunSubmittedSnapshotAndDeadlineSurviveRestart(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	cfg := config.Config{
		MaxSteps: 6, MaxSearchQueries: 3, MaxFetchBytes: 128 << 20, MaxSourceBytes: 16 << 20,
		PDFPolicy: pdfextract.DefaultPolicy(), PDFDownloadTimeoutSeconds: 120,
		FetchTempDir: t.TempDir(), FetchTempTaskBytes: 128 << 20, FetchTempGlobalBytes: 256 << 20,
		LLMModel: "submitted-model", LLMBaseURL: "http://127.0.0.1:8001/v1",
		LLMContextLength: 32768, LLMMaxOutputTokens: 4096, LLMTemperature: 0.7, LLMTopP: 0.9,
	}
	service := app.New(f.store)
	service.SetResearchRunDefaults("standard", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
	conversation, err := service.CreateConversation(ctx, domain.Conversation{
		Title: "Greeting", Mode: "research",
		ModelProfile:     map[string]any{"temperature": 0, "top_p": 0.55, "max_output_tokens": 1536},
		ResearchSettings: map[string]any{"profile": "long_laptop", "time_budget_seconds": 14400},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.turn, err = service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: conversation.ID, Content: "Привет"}, "submitted-profile")
	if err != nil {
		t.Fatal(err)
	}
	saved := researchRunSession(t, f.store, f.turn.Run.ID)
	if saved.Profile != "long_laptop" || saved.Budget.MaxSteps != 6 || saved.Budget.MaxSearchQueries != 3 || saved.Budget.WallTimeSeconds != 14400 || saved.Policy.Budget != saved.Budget || saved.ModelSnapshot.Model != cfg.LLMModel || saved.ModelSnapshot.BaseURL != cfg.LLMBaseURL || saved.ModelSnapshot.Temperature != 0 || saved.ModelSnapshot.TopP != 0.55 || saved.ModelSnapshot.MaxOutputTokens != 1536 || len(saved.ModelSnapshot.StopSequences) != 0 {
		t.Fatal("submitted settings were not stored as the effective bounded profile")
	}
	if f.turn.Run.DeadlineAt == nil || !f.turn.Run.DeadlineAt.Equal(saved.DeadlineAt) || !saved.DeadlineAt.Equal(saved.CreatedAt.Add(4*time.Hour)) {
		t.Fatal("profile deadline was not persisted on both run and session")
	}
	cfg.LLMModel, cfg.LLMBaseURL = "replacement-model", "http://127.0.0.1:9000/v1"
	cfg.LLMTemperature, cfg.LLMTopP, cfg.LLMMaxOutputTokens = 1.4, 0.95, 64
	service.SetResearchRunDefaults("quick", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
	f.reopen(t)
	f.claim(t)
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) { return "Привет!", nil }}
	worker := New(f.store, llm.NewGuardedClient(model), tools.NewRegistry(), Options{Temperature: 1.8, TopP: 0.99, MaxOutputTokens: 64, MaxSteps: 1, MaxSearchQueries: 1})
	if worker.options.TaskTimeout != 2*time.Hour || worker.options.MaxFetchBytes != 128<<20 {
		t.Fatalf("worker defaults drifted: timeout=%s fetch bytes=%d", worker.options.TaskTimeout, worker.options.MaxFetchBytes)
	}
	deadline, persisted := worker.runDeadline(ctx, f.task.ID)
	if !persisted || !deadline.Equal(saved.DeadlineAt) {
		t.Fatal("global two-hour default replaced the longer persisted deadline")
	}
	worker.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	calls := model.calls()
	if len(calls) != 1 || !calls[0].streaming || len(calls[0].request.JSONSchema) != 0 || !calls[0].deadline.Equal(saved.DeadlineAt) {
		t.Fatalf("research greeting did not use exactly one deadline-bound plain stream: calls=%d", len(calls))
	}
	assertResearchRunRequest(t, calls[0].request, saved.ModelSnapshot, 0, 1536)
	after := researchRunSession(t, f.store, f.task.ID)
	if after.Counters.LLMCalls != 1 || after.Counters.HTTPAttempts != 1 || !reflect.DeepEqual(after.ModelSnapshot, saved.ModelSnapshot) || after.Policy != saved.Policy || after.Budget != saved.Budget {
		t.Fatal("execution changed the immutable profile or miscounted the greeting")
	}
	terminalClient := &sessionModelClient{inner: model, repo: f.store, runID: f.task.ID, model: after.ModelSnapshot}
	for _, streaming := range []bool{false, true} {
		if err := callResearchRunModel(ctx, terminalClient, llm.ChatRequest{}, streaming); !errors.Is(err, sqlite.ErrConflict) {
			t.Errorf("terminal request error=%v, want conflict", err)
		}
	}
	if err := calls[0].request.BeforeFallbackAttempt(); err == nil {
		t.Error("completed run accepted a stale fallback callback")
	}
	if len(model.calls()) != 1 || researchRunSession(t, f.store, f.task.ID).Counters != after.Counters {
		t.Fatal("completed research run made or charged another model attempt")
	}
}

func TestResearchRunModelReservationsAndFallbackSurviveRestart(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			saved := f.submit(t, "Count words in the supplied text: one two three", func(session *domain.ResearchSession) {
				session.Budget.MaxLLMCalls = 2
				session.ModelSnapshot.StopSequences = []string{"<|end|>"}
			})
			failure := errors.New("model attempt failed")
			var attempts, priorCallbacks atomic.Int32
			model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, _ bool) (string, error) {
				before := researchRunSession(t, f.store, saved.TaskID).Counters
				if before.LLMCalls != index+1 || before.HTTPAttempts != index*2+1 {
					t.Error("logical call and initial HTTP attempt were not durable before inference")
				}
				attempts.Add(1)
				if err := request.BeforeFallbackAttempt(); err != nil {
					return "", err
				}
				attempts.Add(1)
				if index == 0 {
					return "", failure
				}
				return "{}", nil
			}}
			client := &sessionModelClient{inner: model, repo: f.store, runID: saved.TaskID, model: saved.ModelSnapshot}
			request := llm.ChatRequest{
				Model: "live-model", BaseURL: "http://127.0.0.1:9000/v1", MaxTokens: 4096, Temperature: 1.7, TopP: 0.99, Stop: []string{"live-stop"},
				JSONSchema: json.RawMessage(`{"type":"object"}`),
				BeforeFallbackAttempt: func() error {
					priorCallbacks.Add(1)
					return nil
				},
			}
			if err := callResearchRunModel(ctx, client, request, streaming); !errors.Is(err, failure) {
				t.Fatalf("failed inference error=%v", err)
			}
			if counters := researchRunSession(t, f.store, saved.TaskID).Counters; counters.LLMCalls != 1 || counters.HTTPAttempts != 2 {
				t.Fatalf("failed call with fallback counters=%+v", counters)
			}
			assertResearchRunRequest(t, model.calls()[0].request, saved.ModelSnapshot, 0, 2048)
			f.reopen(t)
			restored := researchRunSession(t, f.store, saved.TaskID)
			if restored.Counters.LLMCalls != 1 || restored.Counters.HTTPAttempts != 2 || !reflect.DeepEqual(restored.ModelSnapshot, saved.ModelSnapshot) {
				t.Fatal("reopening SQLite reset consumed reservations or the model snapshot")
			}
			client = &sessionModelClient{inner: model, repo: f.store, runID: restored.TaskID, model: restored.ModelSnapshot}
			request.MaxTokens = 256
			if err := callResearchRunModel(ctx, client, request, streaming); err != nil {
				t.Fatal(err)
			}
			assertResearchRunRequest(t, model.calls()[1].request, restored.ModelSnapshot, 0, 256)
			if err := callResearchRunModel(ctx, client, request, streaming); !errors.Is(err, sqlite.ErrResearchBudgetExceeded) {
				t.Fatalf("exhausted call error=%v, want research budget exceeded", err)
			}
			if err := model.calls()[1].request.BeforeFallbackAttempt(); !errors.Is(err, sqlite.ErrResearchBudgetExceeded) {
				t.Fatalf("exhausted HTTP callback error=%v", err)
			}
			if len(model.calls()) != 2 || attempts.Load() != 4 || priorCallbacks.Load() != 2 {
				t.Fatalf("logical calls=%d HTTP attempts=%d chained callbacks=%d", len(model.calls()), attempts.Load(), priorCallbacks.Load())
			}
			f.reopen(t)
			if counters := researchRunSession(t, f.store, saved.TaskID).Counters; counters.LLMCalls != 2 || counters.HTTPAttempts != 4 {
				t.Fatalf("durable exhausted counters=%+v", counters)
			}
		})
	}
}

type researchRunSearch struct {
	calls  atomic.Int32
	fail   error
	before func(int)
}

func (s *researchRunSearch) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	n := int(s.calls.Add(1))
	if s.before != nil {
		s.before(n)
	}
	if s.fail != nil {
		return nil, s.fail
	}
	return researchSearchStub{}.Search(ctx, options)
}

type researchRunFetcher struct {
	calls  atomic.Int32
	bytes  atomic.Int64
	fail   error
	before func(int)
}

func (f *researchRunFetcher) ValidateURL(ctx context.Context, rawURL string) error {
	return researchFetcherStub{}.ValidateURL(ctx, rawURL)
}

func (f *researchRunFetcher) Fetch(ctx context.Context, rawURL string, options research.FetchOptions) (research.FetchedDocument, error) {
	n := int(f.calls.Add(1))
	if f.before != nil {
		f.before(n)
	}
	if f.fail != nil {
		return research.FetchedDocument{}, f.fail
	}
	document, err := researchFetcherStub{}.Fetch(ctx, rawURL, options)
	f.bytes.Add(int64(len(document.Body)))
	return document, err
}

func researchRunRegistry(t *testing.T, store *sqlite.Store, search *researchRunSearch, fetcher *researchRunFetcher) *tools.Registry {
	t.Helper()
	artifactStore, err := artifacts.New(t.TempDir(), 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	fetchPolicy := research.DefaultFetchPolicy()
	fetchPolicy.TempRoot = t.TempDir()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: search, Fetcher: fetcher, Extractor: researchExtractorStub{}, Artifacts: artifactStore, Repository: store, MaxFetchBytes: 128 << 20, FetchPolicy: fetchPolicy}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestResearchRunAllModelPathsAndToolReservations(t *testing.T) {
	for _, test := range []struct {
		name          string
		temperature   float64
		maxCalls      int
		criticInvalid bool
		wantStatus    domain.TaskStatus
		wantCode      string
	}{
		{"zero snapshot", 0, 24, false, domain.StatusCompleted, ""},
		{"critic explicit zero", 0.65, 24, false, domain.StatusCompleted, ""},
		{"critic repair budget exhausted", 0, 8, true, domain.StatusFailed, "research_budget_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			saved := f.submit(t, "Research protocol architecture and limitations.", func(session *domain.ResearchSession) {
				session.ModelSnapshot.Temperature = test.temperature
				session.ModelSnapshot.MaxOutputTokens = 4096
				session.Budget.MaxLLMCalls = test.maxCalls
			})
			search := &researchRunSearch{before: func(n int) {
				if researchRunSession(t, f.store, saved.TaskID).Counters.SearchQueries != n {
					t.Error("search started before its durable reservation")
				}
			}}
			fetcher := &researchRunFetcher{before: func(n int) {
				if researchRunSession(t, f.store, saved.TaskID).Counters.Fetches != n {
					t.Error("fetch started before its durable reservation")
				}
			}}
			base := integrationResearchModel()
			model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, streaming bool) (string, error) {
				if counters := researchRunSession(t, f.store, saved.TaskID).Counters; counters.LLMCalls != index+1 || counters.HTTPAttempts != index+1 {
					t.Error("worker model path bypassed or duplicated its durable reservation")
				}
				if index < 6 {
					if streaming {
						return "", errors.New("structured planner, actor, or verifier unexpectedly streamed")
					}
					return base.respond(request, index)
				}
				if index == 6 {
					if !streaming {
						return "", errors.New("finalizer did not stream")
					}
					var input struct {
						ToolResults map[string]struct {
							EvidenceID string `json:"evidence_id"`
							Excerpt    string `json:"excerpt"`
						} `json:"tool_results"`
					}
					if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
						return "", err
					}
					paragraphs := []string{}
					for _, result := range input.ToolResults {
						paragraphs = append(paragraphs, result.Excerpt+" ["+result.EvidenceID+"]")
					}
					sort.Strings(paragraphs)
					encoded, err := json.Marshal(map[string]string{"answer": strings.Join(paragraphs, "\n\n")})
					return string(encoded), err
				}
				if index == 7 {
					if test.criticInvalid {
						return "{", nil
					}
					var input struct {
						Claims []citationClaim `json:"claims_to_check"`
					}
					if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
						return "", err
					}
					for i := range input.Claims {
						input.Claims[i].Kind, input.Claims[i].Quote = "supported", input.Claims[i].Claim
					}
					encoded, err := json.Marshal(citationReview{Claims: input.Claims})
					return string(encoded), err
				}
				return "", errors.New("unexpected extra research model call")
			}}
			worker := New(f.store, llm.NewGuardedClient(model), researchRunRegistry(t, f.store, search, fetcher), Options{MaxSteps: 1, MaxSearchQueries: 1, MaxFetchBytes: 1, MaxOutputTokens: 64, Temperature: 1.7, TopP: 0.99, TaskTimeout: time.Minute})
			worker.runTask(ctx, f.task)
			calls := model.calls()
			if len(calls) != 8 {
				t.Fatalf("model paths=%d, want planner + four actors + verifier + finalizer + critic", len(calls))
			}
			for index, call := range calls {
				temperature, tokens := saved.ModelSnapshot.Temperature, 4096
				if index == 7 {
					if !strings.Contains(call.request.Messages[0].Content, "citation support critic") {
						t.Error("expected citation critic call was not reached")
					}
					temperature, tokens = 0, 2048
				}
				assertResearchRunRequest(t, call.request, saved.ModelSnapshot, temperature, tokens)
				if call.streaming != (index == 6) || len(call.request.JSONSchema) == 0 {
					t.Errorf("model path %d streaming=%t schema=%t", index, call.streaming, len(call.request.JSONSchema) > 0)
				}
			}
			after := researchRunSession(t, f.store, saved.TaskID)
			if search.calls.Load() != 2 || fetcher.calls.Load() != 2 || after.Counters.SearchQueries != 2 || after.Counters.Fetches != 2 || after.Counters.Evidence != 2 || after.Counters.FetchedBytes != fetcher.bytes.Load() || after.Counters.LLMCalls != 8 || after.Counters.HTTPAttempts != 8 {
				t.Fatalf("provider calls=%d/%d counters=%+v actual bytes=%d", search.calls.Load(), fetcher.calls.Load(), after.Counters, fetcher.bytes.Load())
			}
			assertResearchRunTerminal(t, f, test.wantStatus, test.wantCode)
		})
	}
}

func TestResearchRunFailedToolsConsumeBudgetsBeforeExecution(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	saved := f.submit(t, "Research protocol architecture and limitations.", func(session *domain.ResearchSession) {
		session.Budget.MaxSearchQueries, session.Budget.MaxFetches = 1, 1
	})
	search := &researchRunSearch{fail: research.ErrSearchUnavailable}
	fetcher := &researchRunFetcher{fail: research.ErrFetchRequestFailed}
	worker := New(f.store, &researchRunModel{}, researchRunRegistry(t, f.store, search, fetcher), Options{})
	ctx, runner, err := worker.researchExecutor(tools.WithTaskID(ctx, saved.TaskID), saved.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	runner.tools.SeedTaskURLs(saved.TaskID, "https://docs.example.com/protocol")
	for _, name := range []string{"search", "fetch_url"} {
		arguments := json.RawMessage(`{"query":"protocol architecture"}`)
		if name == "fetch_url" {
			arguments = json.RawMessage(`{"url":"https://docs.example.com/protocol"}`)
		}
		if _, err := runner.executeObservedTool(ctx, saved.TaskID, nil, "researcher", name, arguments, ""); err == nil {
			t.Fatalf("failed %s unexpectedly succeeded", name)
		}
		if _, err := runner.executeObservedTool(ctx, saved.TaskID, nil, "researcher", name, arguments, ""); !errors.Is(err, sqlite.ErrResearchBudgetExceeded) {
			t.Fatalf("second %s error=%v, want durable budget denial", name, err)
		}
	}
	if search.calls.Load() != 1 || fetcher.calls.Load() != 1 {
		t.Fatalf("budget denial reached tools: searches=%d fetches=%d", search.calls.Load(), fetcher.calls.Load())
	}
	before := researchRunSession(t, f.store, saved.TaskID).Counters
	if before.SearchQueries != 1 || before.Fetches != 1 || before.LLMCalls != 0 || before.HTTPAttempts != 0 {
		t.Fatalf("failed tool reservations=%+v", before)
	}
	f.reopen(t)
	worker = New(f.store, &researchRunModel{}, researchRunRegistry(t, f.store, search, fetcher), Options{})
	ctx, runner, err = worker.researchExecutor(tools.WithTaskID(context.Background(), saved.TaskID), saved.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"search", "fetch_url"} {
		if _, err := runner.executeObservedTool(ctx, saved.TaskID, nil, "researcher", name, json.RawMessage(`{}`), ""); !errors.Is(err, sqlite.ErrResearchBudgetExceeded) {
			t.Fatalf("reopened %s reservation error=%v", name, err)
		}
	}
	if search.calls.Load() != 1 || fetcher.calls.Load() != 1 || researchRunSession(t, f.store, saved.TaskID).Counters != before {
		t.Fatal("reopening restored a consumed tool budget or charged denied work")
	}
}

func TestResearchRunPlannerRepairBudgetFailureIsNotLLMUnavailable(t *testing.T) {
	f := newResearchRunStore(t)
	saved := f.submit(t, "Count words in the supplied text: one two three", func(session *domain.ResearchSession) {
		session.Budget.MaxLLMCalls = 1
	})
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) { return "{", nil }}
	worker := New(f.store, model, tools.NewRegistry(), Options{TaskTimeout: time.Minute})
	worker.runTask(context.Background(), f.task)
	if len(model.calls()) != 1 {
		t.Fatalf("planner repair exceeded one-call budget: calls=%d", len(model.calls()))
	}
	if counters := researchRunSession(t, f.store, saved.TaskID).Counters; counters.LLMCalls != 1 || counters.HTTPAttempts != 1 {
		t.Fatalf("planner repair budget counters=%+v", counters)
	}
	assertResearchRunTerminal(t, f, domain.StatusFailed, "research_budget_exceeded")
}

func TestResearchRunExecutorsShareExistingInferenceGuard(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	first := f.submit(t, "First supplied text", func(session *domain.ResearchSession) { session.Budget.MaxLLMCalls = 2 })
	second := f.submit(t, "Second supplied text", func(session *domain.ResearchSession) { session.Budget.MaxLLMCalls = 2 })
	var active atomic.Int32
	var overlap atomic.Bool
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	model := &researchRunModel{
		respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) {
			if active.Add(1) != 1 {
				overlap.Store(true)
			}
			defer active.Add(-1)
			return "{}", nil
		},
		stream: func(ctx context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
			if active.Add(1) != 1 {
				overlap.Store(true)
			}
			events := make(chan llm.ChatEvent)
			go func() {
				defer close(events)
				defer active.Add(-1)
				select {
				case <-ctx.Done():
				case <-release:
				}
			}()
			return events, nil
		},
	}
	guard := llm.NewGuardedClient(model)
	worker := New(f.store, guard, tools.NewRegistry(), Options{})
	_, firstRunner, err := worker.researchExecutor(ctx, first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, secondRunner, err := worker.researchExecutor(ctx, second.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	events, err := firstRunner.client.Stream(streamCtx, llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.TryComplete(ctx, llm.ChatRequest{}); !errors.Is(err, llm.ErrInferenceBusy) {
		t.Fatalf("original guard did not retain the research stream slot: %v", err)
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	done := make(chan error, 1)
	go func() { _, err := secondRunner.client.Complete(waitCtx, llm.ChatRequest{}); done <- err }()
	chatTestPoll(t, "second executor reservation", func() bool {
		return researchRunSession(t, f.store, second.TaskID).Counters.LLMCalls == 1
	})
	cancelWait()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting executor error=%v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting executor did not leave the shared guard")
	}
	if len(model.calls()) != 1 || overlap.Load() {
		t.Fatal("per-run executors created independent inference slots")
	}
	releaseOnce.Do(func() { close(release) })
	for range events {
	}
	if _, err := secondRunner.client.Complete(ctx, llm.ChatRequest{}); err != nil {
		t.Fatalf("shared slot was not released: %v", err)
	}
	if len(model.calls()) != 2 || overlap.Load() || active.Load() != 0 {
		t.Fatal("inference calls overlapped or the shared slot leaked")
	}
	if counters := researchRunSession(t, f.store, second.TaskID).Counters; counters.LLMCalls != 2 || counters.HTTPAttempts != 2 {
		t.Fatalf("cancelled queued inference was not charged durably: %+v", counters)
	}
}

func TestResearchRunLegacyTaskKeepsRequestDefaults(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	task, err := f.store.CreateTask(ctx, domain.Task{ID: "legacy-research-defaults", Title: "Count words", Description: "Count words in the supplied text: one two three"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := f.store.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("legacy claim=%t error=%v", ok, err)
	}
	responses := []string{
		`{"schema_version":1,"goal":"count supplied words","subtasks":[{"id":"step_1","kind":"analysis","description":"Inspect supplied text","dependencies":[],"expected_output":"word count","success_criteria":["count returned"]}],"max_steps":1,"final_deliverable":"short answer"}`,
		`{"status":"continue","summary":"Inspect the supplied text","tool_call":{"name":"text.inspect","arguments":{"text":"one two three"}}}`,
		`{"ready_to_answer":true,"summary":"The count is available.","missing":[],"criterion_assessments":{"c0":{"met":true,"evidence_ids":[],"rationale":"The tool recorded the count."}}}`,
		`{"answer":"The supplied text contains three words."}`,
	}
	model := &researchRunModel{respond: func(_ context.Context, _ llm.ChatRequest, index int, _ bool) (string, error) {
		if index >= len(responses) {
			return "", errors.New("unexpected legacy model request")
		}
		return responses[index], nil
	}}
	worker := New(f.store, model, tools.NewRegistry(), Options{Temperature: 0.7, TopP: 0.8, MaxOutputTokens: 512, TaskTimeout: time.Minute})
	worker.runTask(ctx, claimed)
	current, err := f.store.GetTask(ctx, task.ID)
	if err != nil || current.Status != domain.StatusCompleted {
		t.Fatalf("legacy task status=%s error=%v", current.Status, err)
	}
	if len(model.calls()) != 4 {
		t.Fatalf("legacy model calls=%d, want 4", len(model.calls()))
	}
	for index, call := range model.calls() {
		request := call.request
		if request.Model != "" || request.BaseURL != "" || len(request.Stop) != 0 || request.BeforeFallbackAttempt != nil || request.TemperatureSet || request.TopPSet || request.Temperature != 0.7 || request.TopP != 0.8 || request.MaxTokens != 512 || call.streaming != (index == 3) {
			t.Errorf("legacy request %d acquired research overrides or lost configured defaults", index)
		}
	}
	if _, err := f.store.GetResearchSession(ctx, task.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("legacy task unexpectedly acquired a research session: %v", err)
	}
}
