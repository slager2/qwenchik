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
	"github.com/slager2/qwenchik/internal/runtime/keepawake"
	"github.com/slager2/qwenchik/internal/runtime/resources"
)

func TestRuntimeHealthProjectsTypedSnapshotsWithoutProbesOrSecrets(t *testing.T) {
	s, store := newTestServer(t)
	defer store.Close()
	ram := int64(1024)
	snapshot := resources.Snapshot{FreeRAMBytes: &ram, PowerSource: resources.PowerAC, Warnings: []string{"GPU VRAM unknown; configured guard cannot evaluate this metric"}}
	calls := 0
	s.SetRuntimeStatus(func() map[string]any {
		calls++
		return map[string]any{"resources": snapshot, "sleep": keepawake.Status{Provider: "systemd-inhibit", Active: false, FailureReason: "private-fixture", PowerSource: keepawake.PowerAC}, "api_key": "private-fixture", "lease_owner": "private-fixture", "active_warnings": []string{"private-fixture"}}
	})
	response := conversationRequest(t, s.Handler(), http.MethodGet, "/api/health", "", "", 200)
	if response.Code != 200 || calls != 1 {
		t.Fatalf("health status/callback: %d %d", response.Code, calls)
	}
	if strings.Contains(response.Body.String(), "private-fixture") || strings.Contains(response.Body.String(), "lease_owner") {
		t.Fatal("runtime details leaked")
	}
	var body struct {
		Runtime map[string]json.RawMessage `json:"runtime"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body.Runtime["resources"]), `"gpu_free_vram_mb":null`) || !strings.Contains(string(body.Runtime["resources"]), `"free_ram_bytes":1024`) {
		t.Fatalf("unknown/known metrics lost: %s", body.Runtime["resources"])
	}
	projected := SafeRuntimeStatus(map[string]any{"resources": snapshot})
	ram = 2048
	encoded, _ := json.Marshal(projected)
	if !strings.Contains(string(encoded), `"free_ram_bytes":1024`) {
		t.Fatal("projection aliases callback state")
	}
}

func TestResourceEventsAreAllowlistedAndSanitized(t *testing.T) {
	for _, kind := range []string{"run.paused", "run.resumed", "run.resource_probe", "run.blocked", "run.failed"} {
		event := domain.Event{Type: kind, Visibility: "user", Payload: json.RawMessage(`{"status":"paused","pause_reason":"resource_guard","resource_reason":"low_ram","resource_probe_attempts":2,"resume_after":"2026-10-22T00:00:00Z","raw_error":"private-fixture","lease_owner":"private-fixture","message":"private-fixture"}`)}
		if !publicEvent(event) {
			t.Fatalf("event dropped: %s", kind)
		}
		projected := safePublicEvent(event)
		if strings.Contains(string(projected.Payload), "private-fixture") || !strings.Contains(string(projected.Payload), `"resource_probe_attempts":2`) {
			t.Fatalf("projection: %s", projected.Payload)
		}
		event.Visibility = "internal"
		if publicEvent(event) {
			t.Fatal("internal resource event exposed")
		}
	}
}

func TestRunResourceFieldsAndTaskLeasePrivacy(t *testing.T) {
	ctx := context.Background()
	s, store := newTestServer(t)
	defer store.Close()
	c, err := store.CreateConversation(ctx, domain.Conversation{Title: "Run", Mode: "research"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Research"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimNext(ctx); err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if _, err := store.PauseTaskForResource(ctx, turn.Run.ID, "low_ram", time.Now().Add(time.Minute), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	response := conversationRequest(t, s.Handler(), "GET", "/api/runs/"+turn.Run.ID, "", "", 200)
	if !strings.Contains(response.Body.String(), `"pause_reason":"resource_guard"`) || !strings.Contains(response.Body.String(), `"resource_reason":"low_ram"`) {
		t.Fatalf("missing resource fields: %s", response.Body.String())
	}
	task := domain.Task{ID: "test", Lease: &domain.ExecutionLease{Owner: "private-fixture"}}
	for _, value := range []any{task, []domain.Task{task}} {
		recorder := httptest.NewRecorder()
		writeJSON(recorder, 200, value)
		if strings.Contains(recorder.Body.String(), "private-fixture") || strings.Contains(recorder.Body.String(), `"lease"`) {
			t.Fatal("lease metadata exposed")
		}
	}
	if task.Lease == nil {
		t.Fatal("projection mutated task")
	}
}
