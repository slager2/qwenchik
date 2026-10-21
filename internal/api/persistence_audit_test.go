package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestRunCancelAfterCompletionAndReplayAfterDeletion(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	worker := &conversationWorker{}
	server.worker = worker
	h := server.Handler()
	response := conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Delete replay","mode":"chat"}`, "create-audit", 201)
	var c domain.Conversation
	json.Unmarshal(response.Body.Bytes(), &c)
	path := "/api/conversations/" + c.ID
	response = conversationRequest(t, h, "POST", path+"/messages", `{"content":"Question"}`, "send-audit", 202)
	var turn domain.SubmitResponse
	json.Unmarshal(response.Body.Bytes(), &turn)
	if _, ok, e := store.ClaimNext(ctx); e != nil || !ok {
		t.Fatal(e)
	}
	for _, status := range []domain.TaskStatus{domain.StatusRunning, domain.StatusVerifying} {
		if _, e := store.TransitionTask(ctx, turn.Run.ID, status, nil, "", "user", nil); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := store.CompleteTask(ctx, turn.Run.ID, "Completed answer", nil); e != nil {
		t.Fatal(e)
	}
	response = conversationRequest(t, h, "POST", "/api/runs/"+turn.Run.ID+"/cancel", "", "", 202)
	if !strings.Contains(response.Body.String(), `"status":"completed"`) || len(worker.interrupted) != 0 {
		t.Fatalf("completed run was interrupted: %s %#v", response.Body.String(), worker.interrupted)
	}
	conversationRequest(t, h, "DELETE", path, "", "", 204)
	conversationRequest(t, h, "POST", path+"/messages", `{"content":"Question"}`, "send-audit", 404)
	conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Delete replay","mode":"chat"}`, "create-audit", 404)
	response = conversationRequest(t, h, "GET", "/api/conversations", "", "", 200)
	if strings.Contains(response.Body.String(), c.ID) {
		t.Fatal("deleted conversation resurrected")
	}
}

func TestHealthReportsHeartbeatFreshnessWithoutInference(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	var probes atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { probes.Add(1); w.WriteHeader(503) }))
	defer model.Close()
	server.llmURL = model.URL
	server.worker = busyTestWorker{}
	for _, test := range []struct {
		name string
		at   time.Time
		err  error
		want string
	}{{"fresh", time.Now().Add(-time.Second), nil, "fresh"}, {"stale", time.Now().Add(-time.Minute), nil, "stale"}, {"absent", time.Time{}, nil, "unknown"}, {"error", time.Time{}, errors.New("private-fixture"), "unknown"}} {
		t.Run(test.name, func(t *testing.T) {
			server.SetWorkerHeartbeat(func(context.Context) (time.Time, error) { return test.at, test.err })
			response := conversationRequest(t, server.Handler(), "GET", "/api/health", "", "", 200)
			var payload struct {
				Worker  string `json:"worker"`
				Details struct {
					Status string `json:"status"`
					Busy   bool   `json:"busy"`
				} `json:"worker_details"`
				Version int `json:"api_version"`
			}
			if e := json.Unmarshal(response.Body.Bytes(), &payload); e != nil {
				t.Fatal(e)
			}
			if payload.Details.Status != test.want || !payload.Details.Busy || payload.Worker != "attached" || payload.Version != 1 || strings.Contains(response.Body.String(), "private-fixture") {
				t.Fatal(response.Body.String())
			}
		})
	}
	if probes.Load() != 0 {
		t.Fatal("health performed a model probe")
	}
}

func TestAPIRejectsChangingActiveExecutionPermissions(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	h := server.Handler()
	response := conversationRequest(t, h, "POST", "/api/conversations", `{"mode":"research"}`, "", 201)
	var c domain.Conversation
	json.Unmarshal(response.Body.Bytes(), &c)
	path := "/api/conversations/" + c.ID
	response = conversationRequest(t, h, "POST", path+"/messages", `{"content":"Research"}`, "", 202)
	var turn domain.SubmitResponse
	json.Unmarshal(response.Body.Bytes(), &turn)
	conversationRequest(t, h, "PATCH", path, `{"research_settings":{"web_enabled":false}}`, "", 409)
	conversationRequest(t, h, "POST", "/api/runs/"+turn.Run.ID+"/cancel", "", "", 202)
	conversationRequest(t, h, "PATCH", path, `{"research_settings":{"web_enabled":false}}`, "", 200)
	response = conversationRequest(t, h, "POST", path+"/messages", `{"content":"Research","mode":"research"}`, "", 409)
	if !strings.Contains(response.Body.String(), "web_disabled") {
		t.Fatal(response.Body.String())
	}
	conversationRequest(t, h, "POST", path+"/messages", `{"content":"Offline","mode":"chat"}`, "", 202)
}

func TestConversationMutationRejectsPublicBrowserOrigins(t *testing.T) {
	s, store := newTestServer(t)
	defer store.Close()
	h := s.Handler()
	for _, test := range []struct {
		origin, site string
		status       int
	}{{"https://attacker.example", "cross-site", 403}, {"null", "cross-site", 403}, {"", "cross-site", 403}, {"http://localhost:5173", "same-origin", 201}, {"http://127.0.0.1:8080", "same-origin", 201}, {"", "", 201}} {
		r := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"Origin check"}`))
		r.Host = "127.0.0.1:8080"
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Sec-Fetch-Site", test.site)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		if response.Code != test.status {
			t.Fatalf("origin=%q site=%q status=%d body=%s", test.origin, test.site, response.Code, response.Body.String())
		}
	}
}
