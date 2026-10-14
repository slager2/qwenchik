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
	if _, err := service.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/events/stream", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Last-Event-ID", "1")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "id: 3\nevent: step.started") || !strings.Contains(body, "id: 4\nevent: task.cancelled") {
		t.Fatalf("missing replayed public events, status %d: %s", response.Code, body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "checkpoint.saved") {
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
