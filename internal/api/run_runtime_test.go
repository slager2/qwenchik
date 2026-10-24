package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestSafeHarnessEventProjectsVerificationClassifications(t *testing.T) {
	raw := json.RawMessage(`{"verdict":"PARTIAL","phase":"evidence_readiness","summary":"Core requirements supported.","missing":[],"supplementary_missing":["A supplementary detail is unavailable."],"criteria":[{"step_id":"fetch_1","criterion_index":0,"classification":"critical","met":true,"evidence_ids":["evidence-1"],"rationale":"private"},{"step_id":"fetch_1","criterion_index":1,"classification":"unknown","met":false,"evidence_ids":["bad id"]}],"secret":"private"}`)
	projected := safeHarnessEvent(raw)
	if projected["verdict"] != "PARTIAL" || projected["summary"] != nil || projected["secret"] != nil {
		t.Fatalf("verification envelope projection: %#v", projected)
	}
	if missing, ok := projected["supplementary_missing"].([]string); !ok || len(missing) != 1 || missing[0] != "A supplementary detail is unavailable." {
		t.Fatalf("supplementary missing projection: %#v", projected["supplementary_missing"])
	}
	criteria, ok := projected["criteria"].([]map[string]any)
	if !ok || len(criteria) != 1 || criteria[0]["classification"] != "critical" || criteria[0]["met"] != true {
		t.Fatalf("criterion projection: %#v", projected["criteria"])
	}
	ids, ok := criteria[0]["evidence_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "evidence-1" {
		t.Fatalf("criterion evidence projection: %#v", criteria[0]["evidence_ids"])
	}
}

func TestSafeHarnessEventRetainsMaximumVerificationCatalog(t *testing.T) {
	criteria := make([]map[string]any, 0, 120)
	evidenceIDs := make([]string, 0, 8)
	for index := 0; index < 8; index++ {
		evidenceIDs = append(evidenceIDs, fmt.Sprintf("evidence-%024d", index))
	}
	for index := 0; index < 120; index++ {
		criteria = append(criteria, map[string]any{
			"step_id": fmt.Sprintf("step_%d", index/10), "criterion_index": index % 10,
			"classification": "critical", "met": true, "evidence_ids": evidenceIDs,
		})
	}
	raw, err := json.Marshal(map[string]any{"verdict": "PASS", "phase": "evidence_readiness", "missing": []string{}, "criteria": criteria})
	if err != nil || len(raw) <= 16384 {
		t.Fatalf("large verification fixture size=%d err=%v", len(raw), err)
	}
	projected := safeHarnessEvent(raw)
	result, ok := projected["criteria"].([]map[string]any)
	if !ok || len(result) != 120 || projected["verdict"] != "PASS" {
		t.Fatalf("maximum verification catalog was dropped: criteria=%d projection=%#v", len(result), projected)
	}
}

func TestRuntimeSnapshotResyncAndPublicHarnessProjection(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "runtime-snapshot", Title: "PRIVATE_PROMPT", Description: "PRIVATE_PROMPT"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext(ctx); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := store.SaveContextSnapshot(ctx, task.ID, domain.ContextSnapshot{Goal: "PRIVATE_PROMPT", WorkingSet: map[string]string{"note": "PRIVATE_PROMPT"}, Contradictions: []string{"Unresolved observation conflict"}}, "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, task.ID, "context.compacted", nil, "user", map[string]any{"reduced_bytes": 100, "original_tool_result_bytes": 300, "retained_tool_result_bytes": 200, "raw_prompt": "PRIVATE_PROMPT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusFailed, nil, "task.failed", "user", map[string]any{"error_code": "completion_blocked"}); err != nil {
		t.Fatal(err)
	}
	h := server.Handler()
	runtime := conversationRequest(t, h, "GET", "/api/runs/"+task.ID+"/runtime", "", "", 200).Body.String()
	if strings.Contains(runtime, "PRIVATE_PROMPT") || !strings.Contains(runtime, "Unresolved observation conflict") || !strings.Contains(runtime, `"terminal_outcome":"blocked"`) {
		t.Fatalf("unsafe snapshot: %s", runtime)
	}
	stream := conversationRequest(t, h, "GET", "/api/tasks/"+task.ID+"/events/stream?after=999999", "", "", 200).Body.String()
	if !strings.Contains(stream, "event: snapshot") || !strings.Contains(stream, "event: resync") || strings.Contains(stream, "PRIVATE_PROMPT") {
		t.Fatalf("bad resync: %s", stream)
	}
	replay := conversationRequest(t, h, "GET", "/api/tasks/"+task.ID+"/events", "", "", 200).Body.String()
	if !strings.Contains(replay, "context.compacted") || strings.Contains(replay, "raw_prompt") {
		t.Fatalf("unsafe projection: %s", replay)
	}
}

func TestPublicReplayTraversesInternalOnlyPages(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "internal-pages", Title: "Task", Description: "Goal"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 501; index++ {
		if _, err := store.RecordEvent(ctx, task.ID, "trace.private", nil, "internal", map[string]any{"index": index}); err != nil {
			t.Fatal(err)
		}
	}
	last, err := store.RecordEvent(ctx, task.ID, "continuation.requested", nil, "user", map[string]any{"attempt": 1, "max_continuations": 1})
	if err != nil {
		t.Fatal(err)
	}
	body := conversationRequest(t, server.Handler(), "GET", fmt.Sprintf("/api/tasks/%s/events?after=1", task.ID), "", "", 200).Body.Bytes()
	var events []domain.Event
	if json.Unmarshal(body, &events) != nil || len(events) != 1 || events[0].Seq != last.Seq {
		t.Fatalf("lost event after internal-only page: %s", body)
	}
}

func TestRunRuntimeIncludesSanitizedTelemetryWithoutDatabaseWrites(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "telemetry-runtime", Title: "PRIVATE_PROMPT", Description: "PRIVATE_PROMPT"}, "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := server.service.Events(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	telemetry := apiTelemetryFixture(time.Now().UTC())
	telemetry.System.Metadata.PlatformVersion = "https://PRIVATE_URL/private/path"
	telemetry.System.CPUUsage = domain.NewExactMetric(101.0, domain.MetricUnitPercent, telemetry.Timestamp)
	telemetry.System.CPUTemperature = domain.Metric[float64]{Value: telemetryFloatPointer(42), Source: domain.MetricSourceExact, Unit: "/private/provider-error", UpdatedAt: telemetry.Timestamp}
	telemetry.System.GPUs = make([]domain.GPUTelemetry, maxPublicTelemetryGPUs+2)
	for index := range telemetry.System.GPUs {
		telemetry.System.GPUs[index] = domain.GPUTelemetry{
			Name:        domain.NewExactMetric("GPU "+fmt.Sprint(index), domain.MetricUnitName, telemetry.Timestamp),
			Utilization: domain.NewUnavailableMetric[float64](domain.MetricUnitPercent, telemetry.Timestamp),
			Temperature: domain.NewUnavailableMetric[float64](domain.MetricUnitCelsius, telemetry.Timestamp),
			VRAMUsed:    domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, telemetry.Timestamp),
			VRAMFree:    domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, telemetry.Timestamp),
			VRAMTotal:   domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, telemetry.Timestamp),
			PowerWatts:  domain.NewUnavailableMetric[float64](domain.MetricUnitWatts, telemetry.Timestamp),
		}
	}
	telemetry.System.GPUs[0].Name = domain.NewExactMetric("https://PRIVATE_URL/private/path", domain.MetricUnitName, telemetry.Timestamp)
	telemetry.Inference.Source = "raw provider error PRIVATE_PROMPT /private/path"
	telemetry.Inference.RequestedTokens = domain.Metric[int64]{Source: domain.MetricSourceUnavailable, Unit: domain.MetricUnitTokens, UpdatedAt: telemetry.Timestamp, Error: "PRIVATE_PROVIDER_ERROR"}
	telemetryRevision := uint64(7)
	callbackCalls := 0
	server.SetRunTelemetry(func(runID string) (domain.Telemetry, uint64, bool) {
		callbackCalls++
		if runID != task.ID {
			t.Errorf("telemetry callback run ID = %q, want %q", runID, task.ID)
		}
		return telemetry, telemetryRevision, true
	})

	response := conversationRequest(t, server.Handler(), http.MethodGet, "/api/runs/"+task.ID+"/runtime", "", "", http.StatusOK)
	if callbackCalls != 1 {
		t.Fatalf("telemetry callback calls = %d, want 1", callbackCalls)
	}
	var body struct {
		TaskID            string           `json:"task_id"`
		Status            string           `json:"status"`
		Cursor            int64            `json:"cursor"`
		Telemetry         domain.Telemetry `json:"telemetry"`
		TelemetryRevision uint64           `json:"telemetry_revision"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode telemetry response: %v: %s", err, response.Body.String())
	}
	if body.TaskID != task.ID || body.Status != string(domain.StatusInbox) || body.Cursor <= 0 {
		t.Fatalf("legacy runtime fields missing: %#v", body)
	}
	if body.TelemetryRevision != telemetryRevision || len(body.Telemetry.System.GPUs) != maxPublicTelemetryGPUs {
		t.Fatalf("telemetry revision/GPU bound: revision=%d GPUs=%d", body.TelemetryRevision, len(body.Telemetry.System.GPUs))
	}
	if body.Telemetry.System.CPUUsage.Source != domain.MetricSourceUnavailable || body.Telemetry.System.CPUTemperature.Unit != domain.MetricUnitCelsius || body.Telemetry.System.GPUs[0].Name.Source != domain.MetricSourceUnavailable {
		t.Fatalf("invalid metric values were not reduced to unavailable readings: %#v", body.Telemetry.System)
	}
	if body.Telemetry.Inference.Source != "" || body.Telemetry.Inference.RequestedTokens.Error != "" {
		t.Fatalf("unsafe inference details survived projection: %#v", body.Telemetry.Inference)
	}
	if len(response.Body.Bytes()) > maxPublicTelemetryBytes+(16<<10) {
		t.Fatalf("runtime response exceeded bound: %d bytes", len(response.Body.Bytes()))
	}
	for _, marker := range []string{"PRIVATE_PROMPT", "PRIVATE_URL", "PRIVATE_PROVIDER_ERROR", "/private/path", "raw provider error"} {
		if strings.Contains(response.Body.String(), marker) {
			t.Fatalf("telemetry response leaked %q: %s", marker, response.Body.String())
		}
	}
	after, err := server.service.Events(ctx, task.ID, 0, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("telemetry transport wrote durable events: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestRunRuntimeWithoutTelemetryKeepsLegacyJSONShape(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(context.Background(), domain.Task{ID: "legacy-runtime", Title: "Task"}, "")
	if err != nil {
		t.Fatal(err)
	}
	response := conversationRequest(t, server.Handler(), http.MethodGet, "/api/runs/"+task.ID+"/runtime", "", "", http.StatusOK)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"task_id", "status", "cursor", "runtime"} {
		if _, ok := body[key]; !ok {
			t.Errorf("legacy field %q missing: %s", key, response.Body.String())
		}
	}
	for _, key := range []string{"telemetry", "telemetry_revision"} {
		if _, ok := body[key]; ok {
			t.Errorf("field %q appeared without telemetry: %s", key, response.Body.String())
		}
	}
}

func TestSSETelemetryCoalescesFramesAndDoesNotAdvanceDurableCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(context.Background(), domain.Task{ID: "telemetry-stream", Title: "Task"}, "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().UTC().Add(-time.Second)
	telemetry := apiTelemetryFixture(timestamp)
	callbackCalls := 0
	server.SetRunTelemetry(func(string) (domain.Telemetry, uint64, bool) {
		callbackCalls++
		current := telemetry
		revision := uint64(1)
		if callbackCalls >= 3 {
			revision = 2
			current.Timestamp = timestamp.Add(500 * time.Millisecond)
			current.Inference.UpdatedAt = timestamp.Add(500 * time.Millisecond)
			current.Inference.OutputTokens = domain.NewExactMetric(int64(2), domain.MetricUnitTokens, current.Inference.UpdatedAt)
		}
		if callbackCalls >= 4 {
			current.Timestamp = timestamp.Add(time.Second)
			current.System.CPUUsage.UpdatedAt = timestamp.Add(time.Second)
			cancel()
		}
		return current, revision, true
	})
	runtimeCalls := 0
	server.SetRuntimeStatus(func() map[string]any {
		runtimeCalls++
		warnings := []string{"RAM unknown; configured guard cannot evaluate this metric"}
		if runtimeCalls >= 3 {
			warnings = []string{
				"GPU VRAM unknown; configured guard cannot evaluate this metric",
				"RAM unknown; configured guard cannot evaluate this metric",
			}
		}
		return map[string]any{"active_warnings": warnings}
	})
	request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/events/stream", nil).WithContext(ctx)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "event: snapshot") || !strings.Contains(body, `"telemetry_revision":1`) {
		t.Fatalf("initial state missing from stream, status=%d: %s", response.Code, body)
	}
	if strings.Count(body, "event: inference_progress") != 1 || strings.Count(body, "event: runtime_snapshot") != 1 || strings.Count(body, "event: resource_warning") != 1 {
		t.Fatalf("telemetry/warning frames were not coalesced: %s", body)
	}
	if !strings.Contains(body, `"telemetry_revision":2`) || !strings.Contains(body, "GPU VRAM unknown") {
		t.Fatalf("updated telemetry/warnings missing: %s", body)
	}
	for _, event := range []string{"runtime_snapshot", "inference_progress", "resource_warning"} {
		for _, frame := range strings.Split(body, "\n\n") {
			if strings.Contains(frame, "event: "+event) && strings.Contains(frame, "id:") {
				t.Fatalf("ephemeral %s advanced Last-Event-ID: %s", event, frame)
			}
		}
	}
	if callbackCalls != 4 {
		t.Fatalf("telemetry sampled %d times, want one initial snapshot and three existing event polls", callbackCalls)
	}
}

func TestSSELifecycleAliasesRetainExistingNamesAndCursorSemantics(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "lifecycle-alias", Title: "Task"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusCancelled, nil, "task.cancelled", "user", nil); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind    string
		payload map[string]any
	}{
		{"run.paused", map[string]any{"pause_reason": "manual"}},
		{"run.resumed", map[string]any{}},
		{"task.status_changed", map[string]any{"from": "running", "to": "paused"}},
		{"task.status_changed", map[string]any{"from": "paused", "to": "running"}},
		{"task.completed", map[string]any{}},
		{"task.failed", map[string]any{"error_code": "verification_incomplete"}},
	} {
		if _, err := store.RecordEvent(ctx, task.ID, event.kind, nil, "user", event.payload); err != nil {
			t.Fatal(err)
		}
	}
	events, err := server.service.Events(ctx, task.ID, 0, 100)
	if err != nil || len(events) < 7 {
		t.Fatalf("read lifecycle events: count=%d err=%v", len(events), err)
	}
	lastSeq := events[len(events)-1].Seq
	stream := conversationRequest(t, server.Handler(), http.MethodGet, "/api/tasks/"+task.ID+"/events/stream", "", "", http.StatusOK).Body.String()
	for _, name := range []string{"run.paused", "run.resumed", "task_paused", "task_resumed", "task.completed", "task_completed", "task.failed", "task_failed"} {
		if !strings.Contains(stream, "event: "+name) {
			t.Errorf("missing original or alias event %q: %s", name, stream)
		}
	}
	if !strings.Contains(stream, fmt.Sprintf("id: %d\nevent: task_failed", lastSeq)) || !strings.Contains(stream, fmt.Sprintf("id: %d\nevent: task.failed", lastSeq)) {
		t.Errorf("alias did not preserve its durable sequence: %s", stream)
	}
	resume := conversationRequest(t, server.Handler(), http.MethodGet, fmt.Sprintf("/api/tasks/%s/events/stream?after=%d", task.ID, lastSeq), "", "", http.StatusOK).Body.String()
	if strings.Contains(resume, "event: task_paused") || strings.Contains(resume, "event: task_completed") || strings.Contains(resume, "event: task_failed") || strings.Contains(resume, "event: resync") {
		t.Fatalf("durable aliases replayed past the cursor or forced a false gap: %s", resume)
	}
}

func apiTelemetryFixture(at time.Time) domain.Telemetry {
	floatMetric := func(unit string) domain.Metric[float64] { return domain.NewUnavailableMetric[float64](unit, at) }
	intMetric := func(unit string) domain.Metric[int64] { return domain.NewUnavailableMetric[int64](unit, at) }
	stringMetric := func(unit string) domain.Metric[string] { return domain.NewUnavailableMetric[string](unit, at) }
	boolMetric := func(unit string) domain.Metric[bool] { return domain.NewUnavailableMetric[bool](unit, at) }
	return domain.Telemetry{
		Timestamp: at, StaleAfterMS: 1000,
		Capabilities: domain.TelemetryCapabilities{CPU: true, RAM: true, GPU: true, GPUTemperature: true, GPUUtilization: true, Power: true, Battery: true, ProcessRSS: true, LlamaHealth: true, LlamaTimings: true, LlamaMetrics: true, LlamaSlots: true},
		System: domain.SystemTelemetry{
			Metadata: domain.SystemMetadata{OS: "linux", Platform: "ubuntu", PlatformVersion: "24.04", KernelVersion: "6.8.0-40-generic", KernelArch: "x86_64", UptimeSeconds: floatMetric(domain.MetricUnitSeconds)},
			CPUUsage: floatMetric(domain.MetricUnitPercent), CPUTemperature: floatMetric(domain.MetricUnitCelsius),
			RAMUsed: intMetric(domain.MetricUnitBytes), RAMFree: intMetric(domain.MetricUnitBytes), RAMTotal: intMetric(domain.MetricUnitBytes),
			GPUs: []domain.GPUTelemetry{}, AgentRSS: intMetric(domain.MetricUnitBytes), LlamaServerRSS: intMetric(domain.MetricUnitBytes),
			PowerSource: stringMetric(domain.MetricUnitStatus), PowerWatts: floatMetric(domain.MetricUnitWatts),
			BatteryPercent: floatMetric(domain.MetricUnitPercent), BatteryCharging: boolMetric(domain.MetricUnitBoolean),
			LlamaHealth: stringMetric(domain.MetricUnitStatus), LlamaSlotsActive: intMetric(domain.MetricUnitSlots), LlamaSlotsTotal: intMetric(domain.MetricUnitSlots),
		},
		Inference: domain.InferenceTelemetry{
			Status: domain.InferenceStatusGenerating, Source: "llama.cpp", UpdatedAt: at,
			RequestedTokens: intMetric(domain.MetricUnitTokens), PromptTokens: intMetric(domain.MetricUnitTokens),
			OutputTokens: intMetric(domain.MetricUnitTokens), TotalTokens: intMetric(domain.MetricUnitTokens), CachedPromptTokens: intMetric(domain.MetricUnitTokens),
			PromptTokensPerSecond: floatMetric(domain.MetricUnitTokensPerSecond), OutputTokensPerSecond: floatMetric(domain.MetricUnitTokensPerSecond),
			TTFTMS: floatMetric(domain.MetricUnitMilliseconds), PromptMS: floatMetric(domain.MetricUnitMilliseconds), GenerationMS: floatMetric(domain.MetricUnitMilliseconds),
			ContextTokens: intMetric(domain.MetricUnitTokens), ContextWindow: intMetric(domain.MetricUnitTokens),
		},
	}
}

func telemetryFloatPointer(value float64) *float64 { return &value }
