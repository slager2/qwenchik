package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestPlannerFailureProjectionPreservesSafeReasonAndDropsUntrustedDiagnostics(t *testing.T) {
	reason := "research plan is incomplete: search results must be fetched before synthesis"
	payload := map[string]any{
		"message": "План исследования не прошёл проверку.", "error_code": "research_plan_incomplete",
		"failure_stage": "planning", "failure_role": "planner_correction", "planner_attempt": 2,
		"validation_error": reason, "plan_summary": map[string]any{"steps": 4, "search": 4, "fetch": 0, "api_key": "PRIVATE_SECRET"},
	}
	raw, _ := json.Marshal(payload)
	projected := safePublicEvent(domain.Event{Type: "task.failed", Payload: raw})
	var result map[string]any
	if err := json.Unmarshal(projected.Payload, &result); err != nil {
		t.Fatal(err)
	}
	if result["validation_error"] != reason || result["failure_stage"] != "planning" || result["planner_attempt"] != float64(2) || result["error_code"] != "research_plan_incomplete" || strings.Contains(string(projected.Payload), "PRIVATE_SECRET") {
		t.Fatalf("safe planning diagnostic projection: %s", projected.Payload)
	}
	payload["validation_error"] = reason + " api_key=PRIVATE_SECRET"
	payload["failure_role"] = "PRIVATE_SECRET"
	payload["planner_attempt"] = 100
	raw, _ = json.Marshal(payload)
	projected = safePublicEvent(domain.Event{Type: "task.failed", Payload: raw})
	if strings.Contains(string(projected.Payload), "PRIVATE_SECRET") || strings.Contains(string(projected.Payload), "validation_error") || strings.Contains(string(projected.Payload), "planner_attempt") {
		t.Fatalf("untrusted planning diagnostics leaked: %s", projected.Payload)
	}
}

func TestPlanningFailureDiagnosticsReplayThroughRESTAndSSE(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	ctx := context.Background()
	task, err := store.CreateTask(ctx, domain.Task{ID: "planning-diagnostics", Title: "Research", Description: "PRIVATE_CONTEXT"}, "")
	if err != nil {
		t.Fatal(err)
	}
	stage := domain.StagePlanning
	_, err = store.RecordEvent(ctx, task.ID, "task.failed", &stage, "user", map[string]any{
		"error_code": "research_plan_incomplete", "failure_stage": "planning", "failure_role": "planner_correction",
		"planner_attempt": 2, "validation_error": "research plan is incomplete: search questions must be distinct",
		"plan_summary": map[string]int{"steps": 4, "search": 4, "fetch": 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/events", "/events/stream"} {
		request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+suffix, nil)
		request.Host = "127.0.0.1"
		response := httptest.NewRecorder()
		if suffix == "/events/stream" {
			streamCtx, cancel := context.WithTimeout(request.Context(), 100*time.Millisecond)
			defer cancel()
			request = request.WithContext(streamCtx)
		}
		server.Handler().ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, `"validation_error"`) || !strings.Contains(body, `"plan_summary"`) || strings.Contains(body, "PRIVATE_CONTEXT") {
			t.Fatalf("diagnostic replay %s: %d %s", suffix, response.Code, body)
		}
	}
}
