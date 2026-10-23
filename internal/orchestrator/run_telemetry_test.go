package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"github.com/slager2/qwenchik/internal/tools"
)

type runTelemetryTestMonitor struct {
	snapshot resources.Snapshot
}

func (monitor *runTelemetryTestMonitor) Check(context.Context) (resources.Snapshot, *resources.Violation, error) {
	return monitor.LastSnapshot(), nil, nil
}

func (monitor *runTelemetryTestMonitor) LastSnapshot() resources.Snapshot {
	snapshot := monitor.snapshot
	if snapshot.System != nil {
		system := cloneSystemTelemetry(*snapshot.System)
		snapshot.System = &system
	}
	if snapshot.Capabilities != nil {
		capabilities := *snapshot.Capabilities
		snapshot.Capabilities = &capabilities
	}
	if snapshot.StaleAfterMS != nil {
		staleAfter := *snapshot.StaleAfterMS
		snapshot.StaleAfterMS = &staleAfter
	}
	return snapshot
}

func telemetryTestInference(at time.Time, status domain.InferenceStatus, output int64) domain.InferenceTelemetry {
	return domain.InferenceTelemetry{
		Status:                status,
		Source:                "test-transport",
		UpdatedAt:             at,
		RequestedTokens:       domain.NewExactMetric(int64(16), domain.MetricUnitTokens, at),
		PromptTokens:          domain.NewExactMetric(int64(4), domain.MetricUnitTokens, at),
		OutputTokens:          domain.NewExactMetric(output, domain.MetricUnitTokens, at),
		TotalTokens:           domain.NewExactMetric(int64(4)+output, domain.MetricUnitTokens, at),
		CachedPromptTokens:    domain.NewUnavailableMetric[int64](domain.MetricUnitTokens, at),
		PromptTokensPerSecond: domain.NewExactMetric(10.0, domain.MetricUnitTokensPerSecond, at),
		OutputTokensPerSecond: domain.NewExactMetric(20.0, domain.MetricUnitTokensPerSecond, at),
		TTFTMS:                domain.NewExactMetric(12.0, domain.MetricUnitMilliseconds, at),
		PromptMS:              domain.NewExactMetric(20.0, domain.MetricUnitMilliseconds, at),
		GenerationMS:          domain.NewExactMetric(40.0, domain.MetricUnitMilliseconds, at),
		ContextTokens:         domain.NewExactMetric(int64(4), domain.MetricUnitTokens, at),
		ContextWindow:         domain.NewExactMetric(int64(128), domain.MetricUnitTokens, at),
	}
}

func telemetryTestSystem(at time.Time) domain.SystemTelemetry {
	return domain.SystemTelemetry{
		Metadata:       domain.SystemMetadata{OS: "linux", UptimeSeconds: domain.NewExactMetric(100.0, domain.MetricUnitSeconds, at)},
		CPUUsage:       domain.NewExactMetric(12.0, domain.MetricUnitPercent, at),
		CPUTemperature: domain.NewExactMetric(45.0, domain.MetricUnitCelsius, at),
		RAMUsed:        domain.NewExactMetric(int64(100), domain.MetricUnitBytes, at),
		RAMFree:        domain.NewExactMetric(int64(900), domain.MetricUnitBytes, at),
		RAMTotal:       domain.NewExactMetric(int64(1000), domain.MetricUnitBytes, at),
		GPUs: []domain.GPUTelemetry{{
			Name:        domain.NewExactMetric("test-gpu", domain.MetricUnitName, at),
			Utilization: domain.NewExactMetric(25.0, domain.MetricUnitPercent, at),
			Temperature: domain.NewExactMetric(55.0, domain.MetricUnitCelsius, at),
			VRAMUsed:    domain.NewExactMetric(int64(10), domain.MetricUnitBytes, at),
			VRAMFree:    domain.NewExactMetric(int64(90), domain.MetricUnitBytes, at),
			VRAMTotal:   domain.NewExactMetric(int64(100), domain.MetricUnitBytes, at),
			PowerWatts:  domain.NewExactMetric(30.0, domain.MetricUnitWatts, at),
		}},
		AgentRSS:         domain.NewExactMetric(int64(20), domain.MetricUnitBytes, at),
		LlamaServerRSS:   domain.NewExactMetric(int64(30), domain.MetricUnitBytes, at),
		PowerSource:      domain.NewExactMetric("unknown", domain.MetricUnitStatus, at),
		PowerWatts:       domain.NewUnavailableMetric[float64](domain.MetricUnitWatts, at),
		BatteryPercent:   domain.NewUnavailableMetric[float64](domain.MetricUnitPercent, at),
		BatteryCharging:  domain.NewUnavailableMetric[bool](domain.MetricUnitBoolean, at),
		LlamaHealth:      domain.NewExactMetric(domain.LlamaHealthHealthy, domain.MetricUnitStatus, at),
		LlamaSlotsActive: domain.NewExactMetric(int64(1), domain.MetricUnitSlots, at),
		LlamaSlotsTotal:  domain.NewExactMetric(int64(2), domain.MetricUnitSlots, at),
	}
}

func TestRunTelemetryUpdatesRevisionAndIgnoresStaleInference(t *testing.T) {
	worker := New(nil, nil, tools.NewRegistry(), Options{})
	callback := worker.recordRunTelemetry("run-order")
	firstAt := time.Now().UTC().Add(-time.Second)
	callback(telemetryTestInference(firstAt, domain.InferenceStatusGenerating, 2))
	_, firstRevision, ok := worker.RunTelemetry("run-order")
	if !ok || firstRevision == 0 {
		t.Fatalf("first run telemetry unavailable: revision=%d ok=%t", firstRevision, ok)
	}

	completedAt := firstAt.Add(time.Second)
	callback(telemetryTestInference(completedAt, domain.InferenceStatusCompleted, 7))
	completed, completedRevision, ok := worker.RunTelemetry("run-order")
	if !ok || completedRevision <= firstRevision || completed.Inference.Status != domain.InferenceStatusCompleted {
		t.Fatalf("completion update was not retained: revision=%d telemetry=%+v ok=%t", completedRevision, completed.Inference, ok)
	}

	callback(telemetryTestInference(firstAt.Add(-time.Second), domain.InferenceStatusFailed, 99))
	current, currentRevision, ok := worker.RunTelemetry("run-order")
	if !ok || currentRevision != completedRevision || current.Inference.Status != domain.InferenceStatusCompleted || *current.Inference.OutputTokens.Value != 7 {
		t.Fatalf("stale telemetry regressed run state: revision=%d telemetry=%+v ok=%t", currentRevision, current.Inference, ok)
	}
}

func TestRunTelemetryRetentionIsBoundedAndKeepsFinalSnapshotsUntilEviction(t *testing.T) {
	worker := New(nil, nil, tools.NewRegistry(), Options{})
	at := time.Now().UTC()
	for index := 0; index < maxRunTelemetryEntries; index++ {
		status := domain.InferenceStatusGenerating
		if index == 0 {
			status = domain.InferenceStatusCompleted
		}
		worker.runTelemetry.update(fmt.Sprintf("run-%03d", index), telemetryTestInference(at, status, int64(index)))
	}
	if snapshot, _, ok := worker.RunTelemetry("run-000"); !ok || snapshot.Inference.Status != domain.InferenceStatusCompleted {
		t.Fatalf("completed telemetry disappeared before eviction: %+v ok=%t", snapshot, ok)
	}
	worker.runTelemetry.update("run-new", telemetryTestInference(at, domain.InferenceStatusGenerating, 1))
	if _, _, ok := worker.RunTelemetry("run-000"); ok {
		t.Fatal("oldest run telemetry remained after bounded-map eviction")
	}
	if _, _, ok := worker.RunTelemetry("run-001"); !ok {
		t.Fatal("non-evicted run telemetry was lost")
	}
	if count := len(worker.runTelemetry.entries); count != maxRunTelemetryEntries {
		t.Fatalf("telemetry map has %d entries, want maximum %d", count, maxRunTelemetryEntries)
	}
}

func TestRunTelemetryDeepCopiesInferenceAndResourceMetrics(t *testing.T) {
	at := time.Now().UTC().Add(-time.Second)
	system := telemetryTestSystem(at)
	capabilities := domain.TelemetryCapabilities{CPU: true, RAM: true, GPU: true}
	staleAfter := int64(45_000)
	monitor := &runTelemetryTestMonitor{snapshot: resources.Snapshot{
		SnapshotAt:   at,
		System:       func() *domain.SystemTelemetry { copy := cloneSystemTelemetry(system); return &copy }(),
		Capabilities: func() *domain.TelemetryCapabilities { copy := capabilities; return &copy }(),
		StaleAfterMS: func() *int64 { copy := staleAfter; return &copy }(),
	}}
	worker := New(nil, nil, tools.NewRegistry(), Options{ResourceMonitor: monitor})
	inference := telemetryTestInference(at, domain.InferenceStatusCompleted, 7)
	outputValue := *inference.OutputTokens.Value
	worker.recordRunTelemetry("run-copy")(inference)

	*inference.OutputTokens.Value = 77
	*system.CPUUsage.Value = 98
	*system.GPUs[0].Name.Value = "mutated-gpu"
	capabilities.CPU = false
	staleAfter = 1

	first, _, ok := worker.RunTelemetry("run-copy")
	if !ok {
		t.Fatal("run telemetry unavailable")
	}
	if *first.Inference.OutputTokens.Value != outputValue || *first.System.CPUUsage.Value != 12 || *first.System.GPUs[0].Name.Value != "test-gpu" || !first.Capabilities.CPU || first.StaleAfterMS != 45_000 {
		t.Fatalf("snapshot aliases its inputs: %+v", first)
	}

	*first.Inference.OutputTokens.Value = 100
	*first.System.CPUUsage.Value = 100
	*first.System.GPUs[0].Name.Value = "mutated-return"
	second, _, ok := worker.RunTelemetry("run-copy")
	if !ok || *second.Inference.OutputTokens.Value != outputValue || *second.System.CPUUsage.Value != 12 || *second.System.GPUs[0].Name.Value != "test-gpu" {
		t.Fatalf("returned snapshot aliases the stored telemetry: %+v ok=%t", second, ok)
	}
}

func TestRunTelemetryComposesValidatedResourceSnapshotAndKeepsLegacyStatusSmall(t *testing.T) {
	at := time.Now().UTC().Add(-time.Second)
	system := telemetryTestSystem(at)
	capabilities := domain.TelemetryCapabilities{CPU: true, RAM: true, GPU: true, LlamaHealth: true}
	staleAfter := int64(60_000)
	monitor := &runTelemetryTestMonitor{snapshot: resources.Snapshot{
		SnapshotAt: at, System: &system, Capabilities: &capabilities, StaleAfterMS: &staleAfter,
	}}
	worker := New(nil, nil, tools.NewRegistry(), Options{ResourceMonitor: monitor})
	worker.recordRunTelemetry("run-valid")(telemetryTestInference(at, domain.InferenceStatusGenerating, 3))

	snapshot, revision, ok := worker.RunTelemetry("run-valid")
	if !ok || revision == 0 {
		t.Fatalf("composed snapshot missing: revision=%d ok=%t", revision, ok)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("composed snapshot is invalid: %v", err)
	}
	if snapshot.System.Metadata.OS != "linux" || !snapshot.Capabilities.GPU || snapshot.StaleAfterMS != staleAfter || snapshot.Timestamp.Before(system.CPUUsage.UpdatedAt) || snapshot.Timestamp.Before(snapshot.Inference.UpdatedAt) {
		t.Fatalf("resource fields or timestamp were not composed: %+v", snapshot)
	}
	legacy := worker.RuntimeStatus()["resources"].(resources.Snapshot)
	if legacy.System != nil || legacy.Capabilities != nil || legacy.StaleAfterMS != nil {
		t.Fatalf("legacy resources payload contains typed telemetry fields: %+v", legacy)
	}
}

func TestRunTelemetryRevisionTracksNewResourceSnapshot(t *testing.T) {
	at := time.Now().UTC().Add(-time.Second)
	system := telemetryTestSystem(at)
	monitor := &runTelemetryTestMonitor{snapshot: resources.Snapshot{SnapshotAt: at, System: &system}}
	worker := New(nil, nil, tools.NewRegistry(), Options{ResourceMonitor: monitor})
	worker.recordRunTelemetry("run-resource-revision")(telemetryTestInference(at, domain.InferenceStatusGenerating, 3))

	first, firstRevision, ok := worker.RunTelemetry("run-resource-revision")
	if !ok {
		t.Fatal("initial resource-composed telemetry unavailable")
	}

	updatedAt := at.Add(time.Second)
	updatedSystem := telemetryTestSystem(updatedAt)
	*updatedSystem.CPUUsage.Value = 37
	monitor.snapshot = resources.Snapshot{SnapshotAt: updatedAt, System: &updatedSystem}
	second, secondRevision, ok := worker.RunTelemetry("run-resource-revision")
	if !ok || secondRevision <= firstRevision || *second.System.CPUUsage.Value != 37 || !second.Timestamp.After(first.Timestamp) {
		t.Fatalf("resource snapshot change did not advance the revision: first=%d second=%d telemetry=%+v ok=%t", firstRevision, secondRevision, second, ok)
	}
}

type runTelemetryInvocationModel struct {
	response llm.ChatResponse
	events   []llm.ChatEvent
}

func (model *runTelemetryInvocationModel) Complete(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return model.response, nil
}

func (model *runTelemetryInvocationModel) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	stream := make(chan llm.ChatEvent, len(model.events))
	for _, event := range model.events {
		stream <- event
	}
	close(stream)
	return stream, nil
}

func TestInvocationTelemetryCallbackReceivesResponseAndEveryEventWithoutDatabaseTicks(t *testing.T) {
	ctx := context.Background()
	fixture := newResearchRunStore(t)
	fixture.submit(t, "Research telemetry callback", nil)
	worker := New(fixture.store, nil, tools.NewRegistry(), Options{})
	at := time.Now().UTC().Add(-time.Second)
	completionTelemetry := telemetryTestInference(at, domain.InferenceStatusCompleted, 2)
	streamTelemetry := []domain.InferenceTelemetry{
		telemetryTestInference(at.Add(time.Second), domain.InferenceStatusPrefilling, 0),
		telemetryTestInference(at.Add(2*time.Second), domain.InferenceStatusGenerating, 1),
		telemetryTestInference(at.Add(3*time.Second), domain.InferenceStatusCompleted, 5),
	}
	model := &runTelemetryInvocationModel{
		response: llm.ChatResponse{Content: `{"ok":true}`, Telemetry: &llm.ChatTelemetry{InferenceTelemetry: completionTelemetry}},
		events: []llm.ChatEvent{
			{Text: "first", Telemetry: &llm.ChatTelemetry{InferenceTelemetry: streamTelemetry[0]}},
			{Text: "second", Telemetry: &llm.ChatTelemetry{InferenceTelemetry: streamTelemetry[1]}},
			{Telemetry: &llm.ChatTelemetry{InferenceTelemetry: streamTelemetry[2]}},
		},
	}
	callbackCount := 0
	callback := func(inference domain.InferenceTelemetry) {
		callbackCount++
		worker.recordRunTelemetry(fixture.task.ID)(inference)
	}
	client := newInvocationClient(fixture.store, model, fixture.task.ID, callback)
	request := invocationFixtureRequest()
	if _, err := client.Complete(ctx, request); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	if callbackCount != 4 {
		t.Fatalf("telemetry callback count=%d, want response plus all three events", callbackCount)
	}

	snapshot, revision, ok := worker.RunTelemetry(fixture.task.ID)
	if !ok || revision != 4 || snapshot.Inference.Status != domain.InferenceStatusCompleted || *snapshot.Inference.OutputTokens.Value != 5 {
		t.Fatalf("last in-memory inference telemetry was not propagated: revision=%d telemetry=%+v ok=%t", revision, snapshot.Inference, ok)
	}

	events, err := fixture.store.EventsAfter(ctx, fixture.task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	invocationStarted, invocationFinished := 0, 0
	for _, event := range events {
		if strings.Contains(strings.ToLower(event.Type), "telemetry") {
			t.Fatalf("telemetry tick was written to SQLite: %s", event.Type)
		}
		switch event.Type {
		case "invocation.started":
			invocationStarted++
		case "invocation.completed":
			invocationFinished++
			var payload struct {
				Invocation domain.Invocation `json:"invocation"`
				Result     json.RawMessage   `json:"result"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Invocation.Kind == "llm.complete" {
				var persisted llm.ChatResponse
				if err := json.Unmarshal(payload.Result, &persisted); err != nil {
					t.Fatal(err)
				}
				if persisted.Telemetry != nil {
					t.Fatal("completion telemetry was persisted with the invocation result")
				}
			}
		}
	}
	if invocationStarted != 2 || invocationFinished != 2 {
		t.Fatalf("expected only one start/finish pair per model call, got started=%d finished=%d", invocationStarted, invocationFinished)
	}
}
