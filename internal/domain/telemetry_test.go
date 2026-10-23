package domain

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

var telemetryTestTime = time.Date(2026, time.October, 23, 12, 30, 0, 123456789, time.UTC)

func TestMetricConstructorsAndJSONCompatibility(t *testing.T) {
	const largeExactInt64 int64 = 1<<54 + 37
	exact := NewExactMetric(largeExactInt64, MetricUnitBytes, telemetryTestTime)
	estimated := NewEstimatedMetric(12.5, MetricUnitPercent, telemetryTestTime)
	unavailable := NewUnavailableMetricWithError[int64](MetricUnitTokens, telemetryTestTime, MetricErrorUnsupported)

	for name, metric := range map[string]Metric[int64]{"exact": exact, "unavailable": unavailable} {
		if err := metric.Validate(); err != nil {
			t.Errorf("%s metric constructor returned invalid metric: %v", name, err)
		}
	}
	if err := estimated.Validate(); err != nil {
		t.Fatalf("estimated metric constructor returned invalid metric: %v", err)
	}

	encoded, err := json.Marshal(exact)
	if err != nil {
		t.Fatalf("marshal exact metric: %v", err)
	}
	var decodedExact Metric[int64]
	if err := json.Unmarshal(encoded, &decodedExact); err != nil {
		t.Fatalf("unmarshal exact metric: %v", err)
	}
	if decodedExact.Value == nil || *decodedExact.Value != largeExactInt64 {
		t.Fatalf("exact int64 value = %v, want %d", decodedExact.Value, largeExactInt64)
	}
	if decodedExact.Source != MetricSourceExact || decodedExact.Unit != MetricUnitBytes || !decodedExact.UpdatedAt.Equal(telemetryTestTime) {
		t.Errorf("exact metric provenance did not round-trip: %+v", decodedExact)
	}

	encoded, err = json.Marshal(estimated)
	if err != nil {
		t.Fatalf("marshal estimated metric: %v", err)
	}
	var decodedEstimated Metric[float64]
	if err := json.Unmarshal(encoded, &decodedEstimated); err != nil {
		t.Fatalf("unmarshal estimated metric: %v", err)
	}
	if decodedEstimated.Value == nil || *decodedEstimated.Value != 12.5 || decodedEstimated.Source != MetricSourceEstimated || decodedEstimated.Unit != MetricUnitPercent {
		t.Errorf("estimated metric did not round-trip: %+v", decodedEstimated)
	}

	encoded, err = json.Marshal(unavailable)
	if err != nil {
		t.Fatalf("marshal unavailable metric: %v", err)
	}
	var decodedUnavailable map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decodedUnavailable); err != nil {
		t.Fatalf("unmarshal unavailable metric JSON: %v", err)
	}
	if value, ok := decodedUnavailable["value"]; !ok || string(value) != "null" {
		t.Errorf("unavailable value JSON = %s (present %t), want null", value, ok)
	}
	if got := rawString(t, decodedUnavailable["source"]); got != string(MetricSourceUnavailable) {
		t.Errorf("source = %q, want %q", got, MetricSourceUnavailable)
	}
	if got := rawString(t, decodedUnavailable["unit"]); got != MetricUnitTokens {
		t.Errorf("unit = %q, want %q", got, MetricUnitTokens)
	}
	if got := rawString(t, decodedUnavailable["error"]); got != string(MetricErrorUnsupported) {
		t.Errorf("error code = %q, want %q", got, MetricErrorUnsupported)
	}

	unsafe := NewUnavailableMetricWithError[int64](MetricUnitBytes, telemetryTestTime, MetricErrorCode("raw provider error"))
	if err := unsafe.Validate(); !errors.Is(err, ErrInvalidMetric) {
		t.Errorf("unsafe error code validation = %v, want ErrInvalidMetric", err)
	}
}

func TestTelemetryJSONKeepsCapabilityFlagsAndExplicitUnits(t *testing.T) {
	telemetry := validTelemetry()
	telemetry.Capabilities = TelemetryCapabilities{
		CPU: true, RAM: false, GPU: true, GPUTemperature: false,
		GPUUtilization: true, LlamaTimings: false, LlamaMetrics: true, LlamaSlots: false,
		Power: true, Battery: false, ProcessRSS: true, LlamaHealth: true,
	}
	encoded, err := json.Marshal(telemetry)
	if err != nil {
		t.Fatalf("marshal telemetry: %v", err)
	}
	var got struct {
		StaleAfterMS int64           `json:"stale_after_ms"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal telemetry: %v", err)
	}
	if got.StaleAfterMS != 2500 {
		t.Errorf("stale_after_ms = %d, want 2500", got.StaleAfterMS)
	}
	want := map[string]bool{
		"cpu": true, "ram": false, "gpu": true, "gpu_temperature": false,
		"gpu_utilization": true, "llama_timings": false, "llama_metrics": true, "llama_slots": false,
		"power": true, "battery": false, "process_rss": true, "llama_health": true,
	}
	for name, expected := range want {
		if actual, ok := got.Capabilities[name]; !ok || actual != expected {
			t.Errorf("capability %q = %t (present %t), want %t", name, actual, ok, expected)
		}
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal telemetry fields: %v", err)
	}
	var inference map[string]json.RawMessage
	if err := json.Unmarshal(payload["inference"], &inference); err != nil {
		t.Fatalf("unmarshal inference fields: %v", err)
	}
	for _, field := range []string{
		"output_tokens", "total_tokens", "cached_prompt_tokens", "prompt_tokens_per_second", "output_tokens_per_second",
		"ttft_ms", "prompt_ms", "generation_ms", "context_tokens", "context_window",
	} {
		if _, ok := inference[field]; !ok {
			t.Errorf("inference JSON is missing requested field %q", field)
		}
	}
	var system map[string]json.RawMessage
	if err := json.Unmarshal(payload["system"], &system); err != nil {
		t.Fatalf("unmarshal system fields: %v", err)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(system["metadata"], &metadata); err != nil {
		t.Fatalf("unmarshal system metadata: %v", err)
	}
	if rawString(t, metadata["os"]) != "linux" {
		t.Errorf("system OS metadata = %s, want linux", metadata["os"])
	}
	if _, ok := metadata["uptime_seconds"]; !ok {
		t.Error("system metadata omitted uptime_seconds metric")
	}
}

func TestTelemetryValidateRequiresCompleteSnapshotAndValidMetrics(t *testing.T) {
	if err := (Telemetry{}).Validate(); !errors.Is(err, ErrInvalidTelemetry) {
		t.Errorf("zero telemetry validation = %v, want ErrInvalidTelemetry", err)
	}
	if _, err := json.Marshal(Telemetry{}); !errors.Is(err, ErrInvalidTelemetry) {
		t.Errorf("marshaling zero telemetry error = %v, want ErrInvalidTelemetry", err)
	}
	if _, err := json.Marshal(Metric[float64]{}); !errors.Is(err, ErrInvalidMetric) {
		t.Errorf("marshaling zero metric error = %v, want ErrInvalidMetric", err)
	}

	valid := validTelemetry()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid snapshot with explicit unavailable metrics rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Telemetry)
	}{
		{"nonpositive stale duration", func(value *Telemetry) { value.StaleAfterMS = 0 }},
		{"zero metric", func(value *Telemetry) { value.System.CPUUsage = Metric[float64]{} }},
		{"reported source without value", func(value *Telemetry) {
			value.System.CPUUsage = Metric[float64]{Source: MetricSourceExact, Unit: MetricUnitPercent, UpdatedAt: telemetryTestTime}
		}},
		{"unavailable source with value", func(value *Telemetry) {
			value.System.CPUUsage = Metric[float64]{Value: floatPointer(50), Source: MetricSourceUnavailable, Unit: MetricUnitPercent, UpdatedAt: telemetryTestTime}
		}},
		{"metric unit mismatch", func(value *Telemetry) {
			value.System.CPUUsage = NewExactMetric(50.0, MetricUnitBytes, telemetryTestTime)
		}},
		{"cpu usage out of range", func(value *Telemetry) {
			value.System.CPUUsage = NewExactMetric(100.1, MetricUnitPercent, telemetryTestTime)
		}},
		{"negative RAM", func(value *Telemetry) {
			value.System.RAMUsed = NewExactMetric(int64(-1), MetricUnitBytes, telemetryTestTime)
		}},
		{"negative uptime", func(value *Telemetry) {
			value.System.Metadata.UptimeSeconds = NewExactMetric(-1.0, MetricUnitSeconds, telemetryTestTime)
		}},
		{"unbounded platform version", func(value *Telemetry) {
			value.System.Metadata.PlatformVersion = strings.Repeat("x", 129)
		}},
		{"RAM used exceeds total", func(value *Telemetry) {
			value.System.RAMUsed = NewExactMetric(int64(11), MetricUnitBytes, telemetryTestTime)
			value.System.RAMTotal = NewExactMetric(int64(10), MetricUnitBytes, telemetryTestTime)
		}},
		{"GPU metric range", func(value *Telemetry) {
			value.System.GPUs[0].Utilization = NewExactMetric(101.0, MetricUnitPercent, telemetryTestTime)
		}},
		{"GPU name source timestamp", func(value *Telemetry) {
			value.System.GPUs[0].Name = NewExactMetric("GPU", MetricUnitName, telemetryTestTime.Add(time.Second))
		}},
		{"metric after snapshot", func(value *Telemetry) {
			value.System.PowerWatts = NewExactMetric(20.0, MetricUnitWatts, telemetryTestTime.Add(time.Second))
		}},
		{"invalid battery range", func(value *Telemetry) {
			value.System.BatteryPercent = NewExactMetric(101.0, MetricUnitPercent, telemetryTestTime)
		}},
		{"invalid llama health", func(value *Telemetry) {
			value.System.LlamaHealth = NewExactMetric("starting", MetricUnitStatus, telemetryTestTime)
		}},
		{"invalid power source", func(value *Telemetry) {
			value.System.PowerSource = NewExactMetric("mains-ish", MetricUnitStatus, telemetryTestTime)
		}},
		{"negative llama slot count", func(value *Telemetry) {
			value.System.LlamaSlotsActive = NewExactMetric(int64(-1), MetricUnitSlots, telemetryTestTime)
		}},
		{"active llama slots exceed total", func(value *Telemetry) {
			value.System.LlamaSlotsActive = NewExactMetric(int64(2), MetricUnitSlots, telemetryTestTime)
			value.System.LlamaSlotsTotal = NewExactMetric(int64(1), MetricUnitSlots, telemetryTestTime)
		}},
		{"invalid inference status", func(value *Telemetry) {
			value.Inference.Status = "running"
		}},
		{"inference update after snapshot", func(value *Telemetry) {
			value.Inference.UpdatedAt = telemetryTestTime.Add(time.Second)
		}},
		{"context exceeds window", func(value *Telemetry) {
			value.Inference.ContextTokens = NewExactMetric(int64(9), MetricUnitTokens, telemetryTestTime)
			value.Inference.ContextWindow = NewExactMetric(int64(8), MetricUnitTokens, telemetryTestTime)
		}},
		{"total tokens mismatch", func(value *Telemetry) {
			value.Inference.PromptTokens = NewExactMetric(int64(3), MetricUnitTokens, telemetryTestTime)
			value.Inference.OutputTokens = NewExactMetric(int64(2), MetricUnitTokens, telemetryTestTime)
			value.Inference.TotalTokens = NewExactMetric(int64(6), MetricUnitTokens, telemetryTestTime)
		}},
		{"nonfinite rate", func(value *Telemetry) {
			value.Inference.OutputTokensPerSecond = NewExactMetric(math.Inf(1), MetricUnitTokensPerSecond, telemetryTestTime)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			telemetry := validTelemetry()
			test.mutate(&telemetry)
			if err := telemetry.Validate(); !errors.Is(err, ErrInvalidTelemetry) {
				t.Errorf("Validate() error = %v, want ErrInvalidTelemetry", err)
			}
		})
	}
}

func TestTelemetryValidateAcceptsInferenceStatuses(t *testing.T) {
	statuses := []InferenceStatus{
		InferenceStatusQueued,
		InferenceStatusLoading,
		InferenceStatusPrefilling,
		InferenceStatusGenerating,
		InferenceStatusCompleted,
		InferenceStatusFailed,
		InferenceStatusCancelled,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			telemetry := validTelemetry()
			telemetry.Inference.Status = status
			if err := telemetry.Validate(); err != nil {
				t.Fatalf("valid status %q rejected: %v", status, err)
			}
			encoded, err := json.Marshal(telemetry.Inference)
			if err != nil {
				t.Fatalf("marshal inference: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("unmarshal inference: %v", err)
			}
			if gotStatus := rawString(t, got["status"]); gotStatus != string(status) {
				t.Errorf("marshaled status = %q, want %q", gotStatus, status)
			}
		})
	}
	if err := (InferenceTelemetry{Status: "running", UpdatedAt: telemetryTestTime}).Validate(); !errors.Is(err, ErrInvalidInferenceStatus) {
		t.Errorf("invalid inference status error = %v, want ErrInvalidInferenceStatus", err)
	}
}

func validTelemetry() Telemetry {
	timestamp := telemetryTestTime
	unavailableFloat := func(unit string) Metric[float64] {
		return NewUnavailableMetric[float64](unit, timestamp)
	}
	unavailableInt := func(unit string) Metric[int64] {
		return NewUnavailableMetric[int64](unit, timestamp)
	}
	unavailableString := func(unit string) Metric[string] {
		return NewUnavailableMetric[string](unit, timestamp)
	}
	unavailableBool := func(unit string) Metric[bool] {
		return NewUnavailableMetric[bool](unit, timestamp)
	}
	return Telemetry{
		Timestamp:    timestamp,
		StaleAfterMS: 2500,
		Capabilities: TelemetryCapabilities{
			CPU: true, RAM: true, GPU: true, GPUTemperature: true, GPUUtilization: true,
			LlamaTimings: true, LlamaMetrics: true, LlamaSlots: true,
		},
		System: SystemTelemetry{
			Metadata:       SystemMetadata{OS: "linux", UptimeSeconds: unavailableFloat(MetricUnitSeconds)},
			CPUUsage:       unavailableFloat(MetricUnitPercent),
			CPUTemperature: unavailableFloat(MetricUnitCelsius),
			RAMUsed:        unavailableInt(MetricUnitBytes),
			RAMFree:        unavailableInt(MetricUnitBytes),
			RAMTotal:       unavailableInt(MetricUnitBytes),
			GPUs: []GPUTelemetry{{
				Name:        unavailableString(MetricUnitName),
				Utilization: unavailableFloat(MetricUnitPercent),
				Temperature: unavailableFloat(MetricUnitCelsius),
				VRAMUsed:    unavailableInt(MetricUnitBytes),
				VRAMFree:    unavailableInt(MetricUnitBytes),
				VRAMTotal:   unavailableInt(MetricUnitBytes),
				PowerWatts:  unavailableFloat(MetricUnitWatts),
			}},
			AgentRSS:         unavailableInt(MetricUnitBytes),
			LlamaServerRSS:   unavailableInt(MetricUnitBytes),
			PowerSource:      unavailableString(MetricUnitStatus),
			PowerWatts:       unavailableFloat(MetricUnitWatts),
			BatteryPercent:   unavailableFloat(MetricUnitPercent),
			BatteryCharging:  unavailableBool(MetricUnitBoolean),
			LlamaHealth:      unavailableString(MetricUnitStatus),
			LlamaSlotsActive: unavailableInt(MetricUnitSlots),
			LlamaSlotsTotal:  unavailableInt(MetricUnitSlots),
		},
		Inference: InferenceTelemetry{
			Status:                InferenceStatusQueued,
			UpdatedAt:             timestamp,
			RequestedTokens:       unavailableInt(MetricUnitTokens),
			PromptTokens:          unavailableInt(MetricUnitTokens),
			OutputTokens:          unavailableInt(MetricUnitTokens),
			TotalTokens:           unavailableInt(MetricUnitTokens),
			CachedPromptTokens:    unavailableInt(MetricUnitTokens),
			PromptTokensPerSecond: unavailableFloat(MetricUnitTokensPerSecond),
			OutputTokensPerSecond: unavailableFloat(MetricUnitTokensPerSecond),
			TTFTMS:                unavailableFloat(MetricUnitMilliseconds),
			PromptMS:              unavailableFloat(MetricUnitMilliseconds),
			GenerationMS:          unavailableFloat(MetricUnitMilliseconds),
			ContextTokens:         unavailableInt(MetricUnitTokens),
			ContextWindow:         unavailableInt(MetricUnitTokens),
		},
	}
}

func floatPointer(value float64) *float64 { return &value }

func rawString(t *testing.T, value json.RawMessage) string {
	t.Helper()
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	return result
}
