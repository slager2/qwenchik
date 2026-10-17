package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type integrationTransport func(*http.Request) (*http.Response, error)

func (f integrationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestHTTPResearchIntegration(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		failure     string
		wantQueries int
	}{
		{name: "healthy", wantQueries: 2},
		{name: "empty_then_broader_results", wantQueries: 3},
		{name: "searx_503", failure: "search_unavailable", wantQueries: 1},
		{name: "searx_down", failure: "search_unavailable", wantQueries: 1},
		{name: "tavily_unauthorized", failure: "provider_misconfigured"},
		{name: "ddg_blocked", failure: "provider_blocked"},
		{name: "all_fail", failure: "search_unavailable", wantQueries: 1},
		{name: "allowlist_filtered", failure: "search_filtered_by_policy", wantQueries: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var mu sync.Mutex
			var queries []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("safesearch") != "1" || r.URL.Query().Get("q") == "" {
					t.Errorf("unexpected search request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				queries = append(queries, r.URL.Query().Get("q"))
				count := len(queries)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if scenario.name == "searx_503" || scenario.name == "all_fail" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if scenario.name == "empty_then_broader_results" && count == 1 {
					_, _ = io.WriteString(w, `{"results":[]}`)
					return
				}
				_, _ = io.WriteString(w, `{"results":[{"url":"https://docs.example.com/protocol","title":"Protocol specification","content":"Protocol architecture and behavior.","engine":"fixture","score":1},{"url":"https://reference.example.net/protocol","title":"Independent protocol reference","content":"Protocol limitations and compatibility.","engine":"fixture","score":0.9}]}`)
			}))
			t.Cleanup(server.Close)
			client := server.Client()
			var downCalls atomic.Int32
			if scenario.name == "searx_down" {
				client = &http.Client{Transport: integrationTransport(func(*http.Request) (*http.Response, error) {
					downCalls.Add(1)
					return nil, errors.New("fixture connection refused")
				})}
			}
			searx, err := research.NewSearXNG(server.URL, client)
			if err != nil {
				t.Fatal(err)
			}
			var tavilyCalls atomic.Int32
			tavily, err := research.NewTavily("fixture-secret-key", &http.Client{Transport: integrationTransport(func(r *http.Request) (*http.Response, error) {
				tavilyCalls.Add(1)
				if r.Method != http.MethodPost || r.URL.String() != "https://api.tavily.com/search" || r.Header.Get("Authorization") != "Bearer fixture-secret-key" {
					t.Errorf("unexpected Tavily request: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"fixture-secret-key"}`)), Request: r}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			blocked := research.ProviderOutcome{Provider: "duckduckgo", State: "blocked", Code: "provider_blocked", HTTPStatus: http.StatusForbidden}
			ddg := &diagnosticSearchStub{report: research.SearchReport{Outcomes: []research.ProviderOutcome{blocked}}, err: &research.SearchFailure{Code: "search_unavailable", Outcomes: []research.ProviderOutcome{blocked}}}
			var provider research.SearchProvider = searx
			switch scenario.name {
			case "tavily_unauthorized":
				provider = tavily
			case "ddg_blocked":
				provider = ddg
			case "all_fail":
				provider = research.NewCompositeSearch(searx, tavily, ddg)
			}
			store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "research.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			artifactStore, err := artifacts.New(t.TempDir(), 1_000_000)
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			runtime := tools.ResearchTools{SearchProvider: provider, Fetcher: researchFetcherStub{}, Extractor: researchExtractorStub{}, Artifacts: artifactStore, Repository: store, MaxFetchBytes: 100_000}
			if scenario.name == "allowlist_filtered" {
				runtime.AllowedDomains = []string{"allowed.example.org"}
			}
			if err := tools.RegisterResearchTools(registry, runtime); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(ctx, domain.Task{ID: "http-research", Title: "Protocol review", Description: "Research protocol architecture and limitations."}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := store.ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim: ok=%t err=%v", ok, err)
			}
			model := integrationResearchModel()
			worker := New(store, model, registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxSearchQueries: 4})
			worker.runTask(ctx, claimed)
			current, err := store.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := store.EventsAfter(ctx, task.ID, 0, 300)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			gotQueries := append([]string(nil), queries...)
			mu.Unlock()
			queryCount := len(gotQueries) + int(downCalls.Load())
			if queryCount != scenario.wantQueries {
				t.Fatalf("HTTP searches=%d want=%d: %v", queryCount, scenario.wantQueries, gotQueries)
			}
			providerEvents, retries, failures, fetches := 0, 0, 0, 0
			stages := map[domain.Stage]bool{}
			codes := map[string]bool{}
			for _, event := range events {
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(event.Payload), "fixture-secret-key") {
					t.Fatalf("credential leaked: %s", event.Payload)
				}
				if event.Stage != nil {
					stages[*event.Stage] = true
				}
				switch event.Type {
				case "search.provider":
					providerEvents++
					codes[fmt.Sprint(payload["code"])] = true
					if event.Stage == nil || *event.Stage != domain.StageSearch || event.Visibility != "user" {
						t.Fatalf("provider event envelope: %#v", event)
					}
					if payload["code"] == "search_ok" && (payload["state"] != "healthy" || payload["http_status"] != float64(200) || payload["result_count"] != float64(2) || payload["parser_version"] != "searxng-json-v1") {
						t.Fatalf("healthy provider diagnostics: %s", event.Payload)
					}
				case "tool.started":
					if strings.HasPrefix(fmt.Sprint(payload["step_id"]), "conditional-search-retry:") {
						retries++
					}
				case "tool.completed":
					if payload["tool"] == "fetch_url" {
						fetches++
					}
				case "task.failed":
					failures++
					if payload["error_code"] != scenario.failure || len(payload["provider_outcomes"].([]any)) == 0 {
						t.Fatalf("failure diagnostics: %s", event.Payload)
					}
				}
			}
			wantProviderEvents := max(1, scenario.wantQueries)
			if scenario.name == "all_fail" {
				wantProviderEvents = 3
			}
			if providerEvents != wantProviderEvents {
				t.Fatalf("provider events=%d want=%d", providerEvents, wantProviderEvents)
			}
			if scenario.name == "tavily_unauthorized" || scenario.name == "all_fail" {
				if tavilyCalls.Load() != 1 || !codes["provider_misconfigured"] {
					t.Fatalf("Tavily calls=%d codes=%v", tavilyCalls.Load(), codes)
				}
			}
			if scenario.name == "all_fail" && (!codes["provider_blocked"] || !codes["search_unavailable"] || ddg.calls != 1) {
				t.Fatalf("composite outcomes=%v DDG calls=%d", codes, ddg.calls)
			}
			evidence, err := store.Evidence(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.failure != "" {
				if current.Status != domain.StatusFailed || failures != 1 || retries != 0 || fetches != 0 || len(evidence) != 0 || model.requests != 2 || stages[domain.StageVerification] || stages[domain.StageFinalization] {
					t.Fatalf("failed execution: status=%s failures=%d retries=%d fetches=%d evidence=%d model=%d stages=%v", current.Status, failures, retries, fetches, len(evidence), model.requests, stages)
				}
				return
			}
			wantRetries := 0
			if scenario.name == "empty_then_broader_results" {
				wantRetries = 1
				if !codes["search_no_results"] || len(strings.Fields(gotQueries[1])) >= len(strings.Fields(gotQueries[0])) {
					t.Fatalf("empty search did not broaden: queries=%v codes=%v", gotQueries, codes)
				}
			}
			if current.Status != domain.StatusCompleted || failures != 0 || retries != wantRetries || fetches != 2 || len(evidence) != 2 || model.requests != 7 || !codes["search_ok"] {
				t.Fatalf("completion: status=%s failures=%d retries=%d fetches=%d evidence=%d model=%d codes=%v", current.Status, failures, retries, fetches, len(evidence), model.requests, codes)
			}
			messages, err := store.Messages(ctx, task.ID)
			if err != nil || len(messages) == 0 {
				t.Fatalf("messages=%v err=%v", messages, err)
			}
			answer := messages[len(messages)-1].Content
			for _, item := range evidence {
				if item.Taint != research.TaintExternal || item.OriginalQuery == "" || !strings.Contains(answer, "["+item.ID+"]") {
					t.Fatalf("uncited or incomplete evidence: %#v answer=%q", item, answer)
				}
				for _, id := range []string{item.RawArtifactID, item.TextArtifactID} {
					body, err := artifactStore.Read(ctx, task.ID, id)
					if err != nil || len(body) == 0 {
						t.Fatalf("artifact %s: bytes=%d err=%v", id, len(body), err)
					}
				}
			}
			for _, stage := range []domain.Stage{domain.StageSearch, domain.StageFetch, domain.StageVerification, domain.StageFinalization} {
				if !stages[stage] {
					t.Errorf("missing execution stage %s", stage)
				}
			}
		})
	}
}

func integrationResearchModel() *scriptedLLM {
	return &scriptedLLM{respond: func(request llm.ChatRequest, index int) (string, error) {
		switch index {
		case 0:
			return `{"schema_version":1,"goal":"Research protocol architecture and limitations","subtasks":[{"id":"search_1","kind":"search","description":"Find protocol architecture specifications","dependencies":[],"expected_output":"architecture hits","success_criteria":["results recorded"]},{"id":"search_2","kind":"search","description":"Find independent limitations and compatibility references","dependencies":[],"expected_output":"limitations hits","success_criteria":["results recorded"]},{"id":"fetch_1","kind":"fetch","description":"Inspect the specification","dependencies":["search_1","search_2"],"expected_output":"source evidence","success_criteria":["evidence stored"]},{"id":"fetch_2","kind":"fetch","description":"Inspect an independent reference","dependencies":["search_1","search_2"],"expected_output":"independent evidence","success_criteria":["evidence stored"]}],"max_steps":4,"final_deliverable":"Cited protocol review"}`, nil
		case 1:
			return `{"status":"continue","summary":"Search architecture","tool_call":{"name":"search","arguments":{"query":"protocol architecture rare specification version","language":null,"recency":null,"max_results":3}}}`, nil
		case 2:
			return `{"status":"continue","summary":"Search limitations","tool_call":{"name":"search","arguments":{"query":"protocol limitations compatibility","language":null,"recency":null,"max_results":3}}}`, nil
		case 3, 4:
			url := "https://docs.example.com/protocol"
			if index == 4 {
				url = "https://reference.example.net/protocol"
			}
			return fmt.Sprintf(`{"status":"continue","summary":"Inspect source","tool_call":{"name":"fetch_url","arguments":{"url":%q,"original_query":"protocol architecture and limitations"}}}`, url), nil
		case 5, 6:
			var input struct {
				ToolResults map[string]any `json:"tool_results"`
			}
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
				return "", err
			}
			assessments := make([]map[string]any, 0, 4)
			answer := "Protocol review:"
			for _, step := range []string{"search_1", "search_2", "fetch_1", "fetch_2"} {
				ids := []string{}
				if strings.HasPrefix(step, "fetch_") {
					result, _ := input.ToolResults[step].(map[string]any)
					id, _ := result["evidence_id"].(string)
					if id == "" {
						return "", fmt.Errorf("missing evidence for %s", step)
					}
					ids = append(ids, id)
					answer += " Source describes protocol behavior [" + id + "]."
				}
				assessments = append(assessments, map[string]any{"step_id": step, "criterion_index": 0, "met": true, "evidence_ids": ids, "rationale": "Recorded result supports the criterion."})
			}
			if index == 6 {
				return answer, nil
			}
			encoded, err := json.Marshal(map[string]any{"ready_to_answer": true, "summary": "Two distinct sources inspected.", "missing": []string{}, "criterion_assessments": fixtureVerdicts(assessments)})
			return string(encoded), err
		default:
			return "", errors.New("unexpected model request")
		}
	}}
}
