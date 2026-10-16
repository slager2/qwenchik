package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/app"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type testWorker struct{}

func (testWorker) Wake()            {}
func (testWorker) Interrupt(string) {}

type testArtifactReader struct{}

func (testArtifactReader) Read(context.Context, string, string) ([]byte, error) {
	return []byte("stored bytes"), nil
}

func newTestServer(t *testing.T) (*Server, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/api.db")
	if err != nil {
		t.Fatal(err)
	}
	server := New(app.New(store), testWorker{}, store.Ping, "http://127.0.0.1:8000/v1", "EMPTY", "", nil)
	return server, store
}

func TestCreateTaskAndRejectUnknownFields(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"title":"Example","description":"Do a local analysis"}`))
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", response.Code, response.Body.String())
	}
	var task domain.Task
	if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusInbox || task.ID == "" {
		t.Fatalf("unexpected created task: %#v", task)
	}

	bad := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"title":"x","description":"y","status":"completed"}`))
	bad.Host = "127.0.0.1:8080"
	badResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", badResponse.Code)
	}
}

func TestEmbeddedUIAndSecurityHeaders(t *testing.T) {
	if !FrontendAvailable() {
		t.Fatal("expected embedded frontend assets to be present")
	}
	server, store := newTestServer(t)
	defer store.Close()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Qwenchik — локальный агент") {
		t.Fatalf("embedded UI response status %d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing security headers: %#v", response.Header())
	}
	unknownAPI := httptest.NewRequest(http.MethodGet, "/api/not-registered", nil)
	unknownAPI.Host = "127.0.0.1:8080"
	unknownResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(unknownResponse, unknownAPI)
	if unknownResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown API route status = %d, want 404", unknownResponse.Code)
	}
	rebinding := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rebinding.Host = "rebound.attacker.example:8080"
	rebindingResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(rebindingResponse, rebinding)
	if rebindingResponse.Code != http.StatusForbidden {
		t.Fatalf("non-loopback Host status = %d, want 403", rebindingResponse.Code)
	}
	providersRequest := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	providersRequest.Host = "127.0.0.1:8080"
	providersResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(providersResponse, providersRequest)
	if providersResponse.Code != http.StatusOK || !strings.Contains(providersResponse.Body.String(), `"status":"disabled"`) {
		t.Fatalf("provider status=%d body=%s", providersResponse.Code, providersResponse.Body.String())
	}
}

func TestProvidersReportsTavilyConfiguredWithoutKey(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	server.SetTavilyConfigured(true)
	request := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("provider status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"type":"searxng"`) || !strings.Contains(response.Body.String(), `"mode":"tavily"`) || !strings.Contains(response.Body.String(), `"status":"configured"`) {
		t.Fatalf("Tavily configuration was not reported: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "api_key") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("provider response exposed credential details: %s", response.Body.String())
	}
}

func TestSSEReplaysOnlyUserVisibleEvents(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	service := app.New(store)
	task, err := service.CreateTask(context.Background(), "Timeline", "Inspect events", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(context.Background(), task.ID, "checkpoint.saved", nil, "internal", map[string]any{"secret": "not-for-ui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(context.Background(), task.ID, "step.started", nil, "user", map[string]any{"step_id": "step_1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(context.Background(), task.ID, "message.delta", nil, "user", map[string]any{
		"stream_id": "stream-public", "message_id": "message-public", "chunk_index": 0, "delta": "draft text",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(context.Background(), task.ID, "message.delta", nil, "audit", map[string]any{"delta": "not-for-ui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(context.Background(), task.ID, "step.skipped", nil, "user", map[string]any{"step_id": "search_retry_1", "reason": "initial search returned results"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/events/stream", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Last-Event-ID", "1")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "id: 3\nevent: step.started") || !strings.Contains(body, "id: 4\nevent: message.delta") || !strings.Contains(body, `"stream_id":"stream-public"`) || !strings.Contains(body, "id: 6\nevent: step.skipped") || !strings.Contains(body, "id: 7\nevent: task.cancelled") {
		t.Fatalf("missing replayed public events, status %d: %s", response.Code, body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "checkpoint.saved") || strings.Contains(body, "not-for-ui") {
		t.Fatalf("internal event leaked to SSE: %s", body)
	}
}

func TestEvidenceAndArtifactMetadataEndpoints(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	server.SetArtifactReader(testArtifactReader{})
	ctx := context.Background()
	task, err := app.New(store).CreateTask(ctx, "Research", "Compare sources", "")
	if err != nil {
		t.Fatal(err)
	}
	artifactBytes := []byte("stored bytes")
	artifactHash := sha256.Sum256(artifactBytes)
	artifact, err := store.SaveArtifact(ctx, domain.Artifact{ID: "raw-1", TaskID: task.ID, Name: "source.html", MIMEType: "text/html", SHA256: hex.EncodeToString(artifactHash[:]), SizeBytes: int64(len(artifactBytes))}, task.ID+"/raw-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveEvidence(ctx, domain.Evidence{
		TaskID: task.ID, Provider: "direct_http", OriginalURL: "https://example.com/", CanonicalURL: "https://example.com/",
		Title: "Example", ContentSHA256: "source-hash", RawArtifactID: artifact.ID, Excerpt: "Source excerpt",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"artifacts", "evidence"} {
		request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/"+path, nil)
		request.Host = "127.0.0.1:8080"
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "source") {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/artifacts/"+artifact.ID, nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "stored bytes" {
		t.Fatalf("artifact download status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestApprovalEndpointsPersistDecisionAndQueueApprovedTask(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	ctx := context.Background()
	task, err := app.New(store).CreateTask(ctx, "Approval", "Confirm external request", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	stage := domain.StageSynthesis
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusRunning, &stage, "", "", nil); err != nil {
		t.Fatal(err)
	}
	approval, err := store.CreateApproval(ctx, domain.Approval{
		TaskID: task.ID, ToolName: "external.submit", Arguments: json.RawMessage(`{"target":"example"}`),
		Target: "example", RiskExplanation: "External submission", ExpiresAt: time.Now().Add(time.Minute),
	}, map[string]any{"pending_approval": true, "current_step": "step_1"})
	if err != nil {
		t.Fatal(err)
	}
	pendingRequest := httptest.NewRequest(http.MethodGet, "/api/approvals", nil)
	pendingRequest.Host = "127.0.0.1:8080"
	pendingResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(pendingResponse, pendingRequest)
	if pendingResponse.Code != http.StatusOK || !strings.Contains(pendingResponse.Body.String(), approval.ID) {
		t.Fatalf("pending approvals response status=%d body=%s", pendingResponse.Code, pendingResponse.Body.String())
	}
	approveRequest := httptest.NewRequest(http.MethodPost, "/api/approvals/"+approval.ID+"/approve", nil)
	approveRequest.Host = "127.0.0.1:8080"
	approveRequest.Header.Set("Idempotency-Key", "approve-api-1")
	approveResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(approveResponse, approveRequest)
	if approveResponse.Code != http.StatusAccepted || !strings.Contains(approveResponse.Body.String(), "approved") {
		t.Fatalf("approve response status=%d body=%s", approveResponse.Code, approveResponse.Body.String())
	}
	queued, err := store.GetTask(ctx, task.ID)
	if err != nil || queued.Status != domain.StatusQueued {
		t.Fatalf("task status after approval=%q err=%v", queued.Status, err)
	}
	approveRequest = httptest.NewRequest(http.MethodPost, "/api/approvals/"+approval.ID+"/approve", nil)
	approveRequest.Host = "127.0.0.1:8080"
	approveRequest.Header.Set("Idempotency-Key", "approve-api-1")
	approveResponse = httptest.NewRecorder()
	server.Handler().ServeHTTP(approveResponse, approveRequest)
	if approveResponse.Code != http.StatusAccepted {
		t.Fatalf("idempotent approval retry status=%d body=%s", approveResponse.Code, approveResponse.Body.String())
	}
}

func TestMemoryAPIRequiresExplicitAcceptanceForRetrieval(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/memories", strings.NewReader(`{"kind":"preference","text":"Prefer concise Russian answers.","scope":"global"}`))
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("memory create status=%d body=%s", response.Code, response.Body.String())
	}
	var memory domain.Memory
	if err := json.Unmarshal(response.Body.Bytes(), &memory); err != nil {
		t.Fatal(err)
	}
	if memory.Status != domain.MemoryCandidate || memory.ID == "" {
		t.Fatalf("memory creation bypassed review: %#v", memory)
	}

	search := httptest.NewRequest(http.MethodGet, "/api/memories/search?q=concise+Russian", nil)
	search.Host = "127.0.0.1:8080"
	searchResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(searchResponse, search)
	var matches []domain.MemoryMatch
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &matches); err != nil {
		t.Fatal(err)
	}
	if searchResponse.Code != http.StatusOK || len(matches) != 0 {
		t.Fatalf("candidate search status=%d matches=%#v", searchResponse.Code, matches)
	}

	accept := httptest.NewRequest(http.MethodPost, "/api/memories/"+memory.ID+"/accept", nil)
	accept.Host = "127.0.0.1:8080"
	acceptResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(acceptResponse, accept)
	if acceptResponse.Code != http.StatusOK {
		t.Fatalf("memory accept status=%d body=%s", acceptResponse.Code, acceptResponse.Body.String())
	}

	searchResponse = httptest.NewRecorder()
	server.Handler().ServeHTTP(searchResponse, search)
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &matches); err != nil {
		t.Fatal(err)
	}
	if searchResponse.Code != http.StatusOK || len(matches) != 1 || matches[0].Memory.Status != domain.MemoryAccepted {
		t.Fatalf("accepted memory search status=%d matches=%#v", searchResponse.Code, matches)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/api/memories", strings.NewReader(`{"kind":"fact","text":"candidate","status":"accepted"}`))
	invalid.Host = "127.0.0.1:8080"
	invalidResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("attempt to set memory status on create returned %d, want 400", invalidResponse.Code)
	}
}

func TestFailedTaskAcceptsClarificationAndCanBeRetried(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	ctx := context.Background()
	task, err := app.New(store).CreateTask(ctx, "Retry research", "Research a distributed protocol", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusFailed, nil, "task.failed", "user", map[string]any{"error_code": "search_unavailable"}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/messages", strings.NewReader(`{"content":"Use the configured local search provider and retry."}`))
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("failed-task clarification status=%d body=%s", response.Code, response.Body.String())
	}
	queued, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != domain.StatusQueued {
		t.Fatalf("task status after clarification = %q, want queued", queued.Status)
	}
	messages, err := store.Messages(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "Use the configured local search provider and retry." {
		t.Fatalf("clarification was not persisted: %#v", messages)
	}
}

func TestDoctorAggregatesWarningsAndRequiresStrictStructuredModelSmoke(t *testing.T) {
	for _, test := range []struct {
		name              string
		modelReply        string
		wantStatus        string
		wantLLMStatus     string
		wantSearchStatus  string
		wantSearchMessage string
		tavilyConfigured  bool
		searxngConfigured bool
	}{
		{
			name:       "search disabled",
			modelReply: `{"ok":true}`, wantStatus: "warning", wantLLMStatus: "ok",
			wantSearchStatus:  "warning",
			wantSearchMessage: "Web search is disabled. Tasks requiring web discovery will fail with search_unavailable.",
		},
		{
			name:       "invalid structured output with search disabled",
			modelReply: `{"ok":false}`, wantStatus: "error", wantLLMStatus: "error",
			wantSearchStatus:  "warning",
			wantSearchMessage: "Web search is disabled. Tasks requiring web discovery will fail with search_unavailable.",
		},
		{
			name:       "Tavily configured",
			modelReply: `{"ok":true}`, wantStatus: "warning", wantLLMStatus: "ok",
			wantSearchStatus:  "warning",
			wantSearchMessage: "Tavily is configured; external API health is checked during research",
			tavilyConfigured:  true,
		},
		{
			name:       "healthy SearXNG configured",
			modelReply: `{"ok":true}`, wantStatus: "ok", wantLLMStatus: "ok",
			wantSearchStatus:  "ok",
			wantSearchMessage: "SearXNG search endpoint returned valid JSON",
			searxngConfigured: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/models" {
					_, _ = w.Write([]byte(`{"data":[{"id":"doctor-test"}]}`))
					return
				}
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": test.modelReply}}}})
				_, _ = w.Write(response)
			}))
			defer llmServer.Close()
			searchURL := ""
			if test.searxngConfigured {
				searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":[]}`))
				}))
				defer searchServer.Close()
				searchURL = searchServer.URL
			}
			store, err := sqlite.Open(context.Background(), t.TempDir()+"/doctor.db")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server := New(app.New(store), testWorker{}, store.Ping, llmServer.URL+"/v1", "EMPTY", searchURL, nil)
			server.SetLLMModel("doctor-test")
			server.SetTavilyConfigured(test.tavilyConfigured)
			server.SetSandboxStatus(true, "rootless sandbox is available")
			request := httptest.NewRequest(http.MethodGet, "/api/doctor", nil)
			request.Host = "127.0.0.1:8080"
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			var body struct {
				Status string                       `json:"status"`
				Checks map[string]map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantHTTPStatus := http.StatusOK
			if test.wantStatus == "error" {
				wantHTTPStatus = http.StatusServiceUnavailable
			}
			if response.Code != wantHTTPStatus || body.Status != test.wantStatus || body.Checks["llm"]["status"] != test.wantLLMStatus {
				t.Fatalf("doctor status=%q llm=%#v body=%s", body.Status, body.Checks["llm"], response.Body.String())
			}
			if body.Checks["search"]["status"] != test.wantSearchStatus || body.Checks["search"]["message"] != test.wantSearchMessage {
				t.Fatalf("doctor search check=%#v body=%s", body.Checks["search"], response.Body.String())
			}
		})
	}
}

func TestDoctorDoesNotReportMalformedSearXNGResponseAsHealthy(t *testing.T) {
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer searchServer.Close()
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/doctor-search.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := New(app.New(store), testWorker{}, store.Ping, "http://127.0.0.1:1/v1", "", searchServer.URL, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/doctor", nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var body struct {
		Checks map[string]map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Checks["search"]["status"] != "warning" || !strings.Contains(body.Checks["search"]["message"], "unavailable") {
		t.Fatalf("doctor treated malformed SearXNG JSON as healthy: %#v", body.Checks["search"])
	}
}
