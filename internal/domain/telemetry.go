package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidTelemetry = errors.New("invalid telemetry")
var ErrInvalidMetric = errors.New("invalid telemetry metric")
var ErrInvalidInferenceStatus = errors.New("invalid inference status")

// MetricSource describes how a metric value was obtained.
type MetricSource string

const (
	MetricSourceExact       MetricSource = "exact"
	MetricSourceEstimated   MetricSource = "estimated"
	MetricSourceUnavailable MetricSource = "unavailable"
)

// MetricErrorCode is a bounded, safe code; it cannot carry raw runtime errors.
type MetricErrorCode string

const (
	MetricErrorUnsupported            MetricErrorCode = "unsupported"
	MetricErrorPermissionDenied       MetricErrorCode = "permission_denied"
	MetricErrorTemporarilyUnavailable MetricErrorCode = "temporarily_unavailable"
	MetricErrorTimeout                MetricErrorCode = "timeout"
	MetricErrorCollectionFailed       MetricErrorCode = "collection_failed"
)

func (code MetricErrorCode) Valid() bool {
	switch code {
	case MetricErrorUnsupported, MetricErrorPermissionDenied, MetricErrorTemporarilyUnavailable,
		MetricErrorTimeout, MetricErrorCollectionFailed:
		return true
	default:
		return false
	}
}

// MetricValue is the set of JSON scalar types used by public telemetry.
type MetricValue interface {
	bool | string | int64 | float64
}

// Metric carries a typed reading and its provenance. Value is always serialized;
// unavailable readings therefore appear as JSON null rather than a fabricated zero.
type Metric[T MetricValue] struct {
	Value     *T              `json:"value"`
	Source    MetricSource    `json:"source"`
	Unit      string          `json:"unit"`
	UpdatedAt time.Time       `json:"updated_at"`
	Error     MetricErrorCode `json:"error,omitempty"`
}

func NewExactMetric[T MetricValue](value T, unit string, updatedAt time.Time) Metric[T] {
	return Metric[T]{Value: &value, Source: MetricSourceExact, Unit: unit, UpdatedAt: updatedAt}
}

func NewEstimatedMetric[T MetricValue](value T, unit string, updatedAt time.Time) Metric[T] {
	return Metric[T]{Value: &value, Source: MetricSourceEstimated, Unit: unit, UpdatedAt: updatedAt}
}

func NewUnavailableMetric[T MetricValue](unit string, updatedAt time.Time) Metric[T] {
	return Metric[T]{Source: MetricSourceUnavailable, Unit: unit, UpdatedAt: updatedAt}
}

func NewUnavailableMetricWithError[T MetricValue](unit string, updatedAt time.Time, code MetricErrorCode) Metric[T] {
	return Metric[T]{Source: MetricSourceUnavailable, Unit: unit, UpdatedAt: updatedAt, Error: code}
}

func (metric Metric[T]) Validate() error {
	if strings.TrimSpace(metric.Unit) == "" {
		return fmt.Errorf("%w: unit is required", ErrInvalidMetric)
	}
	if metric.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: updated_at is required", ErrInvalidMetric)
	}
	if metric.Error != "" {
		if !metric.Error.Valid() {
			return fmt.Errorf("%w: invalid error code", ErrInvalidMetric)
		}
		if metric.Source != MetricSourceUnavailable {
			return fmt.Errorf("%w: error code requires an unavailable source", ErrInvalidMetric)
		}
	}
	switch metric.Source {
	case MetricSourceExact, MetricSourceEstimated:
		if metric.Value == nil {
			return fmt.Errorf("%w: source %q requires a value", ErrInvalidMetric, metric.Source)
		}
		switch value := any(*metric.Value).(type) {
		case float32:
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("%w: value must be finite", ErrInvalidMetric)
			}
		case float64:
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%w: value must be finite", ErrInvalidMetric)
			}
		}
	case MetricSourceUnavailable:
		if metric.Value != nil {
			return fmt.Errorf("%w: unavailable source cannot have a value", ErrInvalidMetric)
		}
	default:
		return fmt.Errorf("%w: unknown source", ErrInvalidMetric)
	}
	return nil
}

func (metric Metric[T]) MarshalJSON() ([]byte, error) {
	if err := metric.Validate(); err != nil {
		return nil, err
	}
	type encodedMetric Metric[T]
	return json.Marshal(encodedMetric(metric))
}

const (
	MetricUnitPercent         = "percent"
	MetricUnitCelsius         = "celsius"
	MetricUnitBytes           = "bytes"
	MetricUnitWatts           = "watts"
	MetricUnitName            = "name"
	MetricUnitBoolean         = "boolean"
	MetricUnitStatus          = "status"
	MetricUnitTokens          = "tokens"
	MetricUnitTokensPerSecond = "tokens/s"
	MetricUnitMilliseconds    = "ms"
	MetricUnitSeconds         = "seconds"
	MetricUnitSlots           = "slots"
	LlamaHealthHealthy        = "healthy"
	LlamaHealthUnhealthy      = "unhealthy"
)

// TelemetryCapabilities reports which telemetry families can be collected.
// Flags are always serialized so false remains distinct from an omitted field.
type TelemetryCapabilities struct {
	CPU            bool `json:"cpu"`
	RAM            bool `json:"ram"`
	GPU            bool `json:"gpu"`
	GPUTemperature bool `json:"gpu_temperature"`
	GPUUtilization bool `json:"gpu_utilization"`
	Power          bool `json:"power"`
	Battery        bool `json:"battery"`
	ProcessRSS     bool `json:"process_rss"`
	LlamaHealth    bool `json:"llama_health"`
	LlamaTimings   bool `json:"llama_timings"`
	LlamaMetrics   bool `json:"llama_metrics"`
	LlamaSlots     bool `json:"llama_slots"`
}

// Telemetry is a platform-independent snapshot of host and inference metrics.
type Telemetry struct {
	Timestamp    time.Time             `json:"timestamp"`
	StaleAfterMS int64                 `json:"stale_after_ms"`
	Capabilities TelemetryCapabilities `json:"capabilities"`
	System       SystemTelemetry       `json:"system"`
	Inference    InferenceTelemetry    `json:"inference"`
}

type SystemTelemetry struct {
	Metadata         SystemMetadata  `json:"metadata"`
	CPUUsage         Metric[float64] `json:"cpu_usage"`
	CPUTemperature   Metric[float64] `json:"cpu_temperature"`
	RAMUsed          Metric[int64]   `json:"ram_used"`
	RAMFree          Metric[int64]   `json:"ram_free"`
	RAMTotal         Metric[int64]   `json:"ram_total"`
	GPUs             []GPUTelemetry  `json:"gpus"`
	AgentRSS         Metric[int64]   `json:"agent_rss"`
	LlamaServerRSS   Metric[int64]   `json:"llama_server_rss"`
	PowerSource      Metric[string]  `json:"power_source"`
	PowerWatts       Metric[float64] `json:"power_watts"`
	BatteryPercent   Metric[float64] `json:"battery_percent"`
	BatteryCharging  Metric[bool]    `json:"battery_charging"`
	LlamaHealth      Metric[string]  `json:"llama_health"`
	LlamaSlotsActive Metric[int64]   `json:"llama_slots_active"`
	LlamaSlotsTotal  Metric[int64]   `json:"llama_slots_total"`
}

type SystemMetadata struct {
	OS              string          `json:"os,omitempty"`
	Platform        string          `json:"platform,omitempty"`
	PlatformVersion string          `json:"platform_version,omitempty"`
	KernelVersion   string          `json:"kernel_version,omitempty"`
	KernelArch      string          `json:"kernel_arch,omitempty"`
	UptimeSeconds   Metric[float64] `json:"uptime_seconds"`
}

type GPUTelemetry struct {
	Name        Metric[string]  `json:"name"`
	Utilization Metric[float64] `json:"utilization"`
	Temperature Metric[float64] `json:"temperature"`
	VRAMUsed    Metric[int64]   `json:"vram_used"`
	VRAMFree    Metric[int64]   `json:"vram_free"`
	VRAMTotal   Metric[int64]   `json:"vram_total"`
	PowerWatts  Metric[float64] `json:"power_watts"`
}

type InferenceStatus string

const (
	InferenceStatusQueued     InferenceStatus = "queued"
	InferenceStatusLoading    InferenceStatus = "loading"
	InferenceStatusPrefilling InferenceStatus = "prefilling"
	InferenceStatusGenerating InferenceStatus = "generating"
	InferenceStatusCompleted  InferenceStatus = "completed"
	InferenceStatusFailed     InferenceStatus = "failed"
	InferenceStatusCancelled  InferenceStatus = "cancelled"
)

func (status InferenceStatus) Valid() bool {
	switch status {
	case InferenceStatusQueued, InferenceStatusLoading, InferenceStatusPrefilling,
		InferenceStatusGenerating, InferenceStatusCompleted, InferenceStatusFailed,
		InferenceStatusCancelled:
		return true
	default:
		return false
	}
}

type InferenceTelemetry struct {
	Status                InferenceStatus `json:"status"`
	Source                string          `json:"source,omitempty"`
	UpdatedAt             time.Time       `json:"updated_at"`
	RequestedTokens       Metric[int64]   `json:"requested_tokens"`
	PromptTokens          Metric[int64]   `json:"prompt_tokens"`
	OutputTokens          Metric[int64]   `json:"output_tokens"`
	TotalTokens           Metric[int64]   `json:"total_tokens"`
	CachedPromptTokens    Metric[int64]   `json:"cached_prompt_tokens"`
	PromptTokensPerSecond Metric[float64] `json:"prompt_tokens_per_second"`
	OutputTokensPerSecond Metric[float64] `json:"output_tokens_per_second"`
	TTFTMS                Metric[float64] `json:"ttft_ms"`
	PromptMS              Metric[float64] `json:"prompt_ms"`
	GenerationMS          Metric[float64] `json:"generation_ms"`
	ContextTokens         Metric[int64]   `json:"context_tokens"`
	ContextWindow         Metric[int64]   `json:"context_window"`
}

type InvocationStatus string

const (
	InvocationStatusCompleted InvocationStatus = "completed"
	InvocationStatusFailed    InvocationStatus = "failed"
	InvocationStatusUnknown   InvocationStatus = "unknown"
)

const InferenceSummaryMaxBytes = 8 << 10

// InferenceSummary is the bounded final inference snapshot associated with a
// completed invocation. It intentionally contains no invocation result data.
type InferenceSummary struct {
	InvocationID     string             `json:"invocation_id"`
	InvocationStatus InvocationStatus   `json:"invocation_status"`
	SnapshotAt       time.Time          `json:"snapshot_at"`
	Inference        InferenceTelemetry `json:"inference"`
}

func (status InvocationStatus) Valid() bool {
	switch status {
	case InvocationStatusCompleted, InvocationStatusFailed, InvocationStatusUnknown:
		return true
	default:
		return false
	}
}

// Sanitized returns a copy whose inference source is restricted to the one
// explicitly supported provider identifier.
func (summary InferenceSummary) Sanitized() InferenceSummary {
	if summary.Inference.Source != "llama.cpp" {
		summary.Inference.Source = ""
	}
	return summary
}

func (summary InferenceSummary) Validate() error {
	if summary.InvocationID == "" || len(summary.InvocationID) > 128 ||
		strings.TrimSpace(summary.InvocationID) != summary.InvocationID ||
		!utf8.ValidString(summary.InvocationID) || strings.ContainsAny(summary.InvocationID, " \t\x00\r\n") {
		return fmt.Errorf("%w: invalid invocation_id", ErrInvalidInferenceSummary)
	}
	if !summary.InvocationStatus.Valid() {
		return fmt.Errorf("%w: invalid invocation_status", ErrInvalidInferenceSummary)
	}
	if summary.SnapshotAt.IsZero() {
		return fmt.Errorf("%w: snapshot_at is required", ErrInvalidInferenceSummary)
	}
	if summary.Inference.Source != "" && summary.Inference.Source != "llama.cpp" {
		return fmt.Errorf("%w: invalid inference source", ErrInvalidInferenceSummary)
	}
	if err := summary.Inference.validate(summary.SnapshotAt); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInferenceSummary, err)
	}
	if err := validateMetric(summary.Inference.CachedPromptTokens, MetricUnitTokens, summary.SnapshotAt, validateNonnegative); err != nil {
		return fmt.Errorf("%w: cached_prompt_tokens: %v", ErrInvalidInferenceSummary, err)
	}
	encoded, err := json.Marshal(inferenceSummaryJSON(summary))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInferenceSummary, err)
	}
	if len(encoded) > InferenceSummaryMaxBytes {
		return fmt.Errorf("%w: payload exceeds %d bytes", ErrInvalidInferenceSummary, InferenceSummaryMaxBytes)
	}
	return nil
}

func (summary InferenceSummary) MarshalJSON() ([]byte, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(inferenceSummaryJSON(summary))
}

type inferenceSummaryJSON InferenceSummary

var ErrInvalidInferenceSummary = errors.New("invalid inference summary")

func (inference InferenceTelemetry) Validate() error {
	if !inference.Status.Valid() {
		return fmt.Errorf("%w: unknown status", ErrInvalidInferenceStatus)
	}
	if inference.UpdatedAt.IsZero() {
		return errors.New("inference updated_at is required")
	}
	if len(inference.Source) > 64 || strings.ContainsAny(inference.Source, "\x00\r\n") {
		return errors.New("inference source exceeds safe bounds")
	}
	return nil
}

func (telemetry Telemetry) Validate() error {
	if telemetry.Timestamp.IsZero() {
		return invalidTelemetry("timestamp is required")
	}
	if telemetry.StaleAfterMS <= 0 {
		return invalidTelemetry("stale_after_ms must be positive")
	}

	system := telemetry.System
	for _, item := range []struct {
		name  string
		value string
		limit int
	}{
		{"os", system.Metadata.OS, 32},
		{"platform", system.Metadata.Platform, 128},
		{"platform_version", system.Metadata.PlatformVersion, 128},
		{"kernel_version", system.Metadata.KernelVersion, 128},
		{"kernel_arch", system.Metadata.KernelArch, 32},
	} {
		if len(item.value) > item.limit || strings.ContainsRune(item.value, '\x00') {
			return invalidTelemetry("system metadata " + item.name + " exceeds bounds")
		}
	}
	metrics := []struct {
		name  string
		check func() error
	}{
		{"system.uptime_seconds", func() error {
			return validateMetric(system.Metadata.UptimeSeconds, MetricUnitSeconds, telemetry.Timestamp, validateNonnegativeFloat)
		}},
		{"system.cpu_usage", func() error {
			return validateMetric(system.CPUUsage, MetricUnitPercent, telemetry.Timestamp, validatePercent)
		}},
		{"system.cpu_temperature", func() error {
			return validateMetric(system.CPUTemperature, MetricUnitCelsius, telemetry.Timestamp, validateTemperature)
		}},
		{"system.ram_used", func() error {
			return validateMetric(system.RAMUsed, MetricUnitBytes, telemetry.Timestamp, validateNonnegative)
		}},
		{"system.ram_free", func() error {
			return validateMetric(system.RAMFree, MetricUnitBytes, telemetry.Timestamp, validateNonnegative)
		}},
		{"system.ram_total", func() error {
			return validateMetric(system.RAMTotal, MetricUnitBytes, telemetry.Timestamp, validatePositive)
		}},
		{"system.agent_rss", func() error {
			return validateMetric(system.AgentRSS, MetricUnitBytes, telemetry.Timestamp, validateNonnegative)
		}},
		{"system.llama_server_rss", func() error {
			return validateMetric(system.LlamaServerRSS, MetricUnitBytes, telemetry.Timestamp, validateNonnegative)
		}},
		{"system.power_source", func() error {
			return validateMetric(system.PowerSource, MetricUnitStatus, telemetry.Timestamp, validatePowerSource)
		}},
		{"system.power_watts", func() error {
			return validateMetric(system.PowerWatts, MetricUnitWatts, telemetry.Timestamp, validateNonnegativeFloat)
		}},
		{"system.battery_percent", func() error {
			return validateMetric(system.BatteryPercent, MetricUnitPercent, telemetry.Timestamp, validatePercent)
		}},
		{"system.battery_charging", func() error {
			return validateMetric(system.BatteryCharging, MetricUnitBoolean, telemetry.Timestamp, noValidation[bool])
		}},
		{"system.llama_health", func() error {
			return validateMetric(system.LlamaHealth, MetricUnitStatus, telemetry.Timestamp, validateLlamaHealth)
		}},
		{"system.llama_slots_active", func() error {
			return validateMetric(system.LlamaSlotsActive, MetricUnitSlots, telemetry.Timestamp, validateNonnegative)
		}},
		{"system.llama_slots_total", func() error {
			return validateMetric(system.LlamaSlotsTotal, MetricUnitSlots, telemetry.Timestamp, validateNonnegative)
		}},
		{"inference.cached_prompt_tokens", func() error {
			return validateMetric(telemetry.Inference.CachedPromptTokens, MetricUnitTokens, telemetry.Timestamp, validateNonnegative)
		}},
	}
	for _, metric := range metrics {
		if err := metric.check(); err != nil {
			return invalidTelemetryField(metric.name, err)
		}
	}
	if err := validateMemoryTotals("system.ram", system.RAMUsed, system.RAMFree, system.RAMTotal); err != nil {
		return err
	}
	if active, activeOK := metricValue(system.LlamaSlotsActive); activeOK {
		if total, totalOK := metricValue(system.LlamaSlotsTotal); totalOK && active > total {
			return invalidTelemetry("system.llama_slots_active exceeds system.llama_slots_total")
		}
	}

	for index, gpu := range system.GPUs {
		prefix := fmt.Sprintf("system.gpus[%d]", index)
		gpuMetrics := []struct {
			name  string
			check func() error
		}{
			{"name", func() error {
				return validateMetric(gpu.Name, MetricUnitName, telemetry.Timestamp, validateName)
			}},
			{"utilization", func() error {
				return validateMetric(gpu.Utilization, MetricUnitPercent, telemetry.Timestamp, validatePercent)
			}},
			{"temperature", func() error {
				return validateMetric(gpu.Temperature, MetricUnitCelsius, telemetry.Timestamp, validateTemperature)
			}},
			{"vram_used", func() error {
				return validateMetric(gpu.VRAMUsed, MetricUnitBytes, telemetry.Timestamp, validateNonnegative)
			}},
			{"vram_free", func() error {
				return validateMetric(gpu.VRAMFree, MetricUnitBytes, telemetry.Timestamp, validateNonnegative)
			}},
			{"vram_total", func() error {
				return validateMetric(gpu.VRAMTotal, MetricUnitBytes, telemetry.Timestamp, validatePositive)
			}},
			{"power_watts", func() error {
				return validateMetric(gpu.PowerWatts, MetricUnitWatts, telemetry.Timestamp, validateNonnegativeFloat)
			}},
		}
		for _, metric := range gpuMetrics {
			if err := metric.check(); err != nil {
				return invalidTelemetryField(prefix+"."+metric.name, err)
			}
		}
		if err := validateMemoryTotals(prefix+".vram", gpu.VRAMUsed, gpu.VRAMFree, gpu.VRAMTotal); err != nil {
			return err
		}
	}

	if err := telemetry.Inference.validate(telemetry.Timestamp); err != nil {
		return invalidTelemetryField("inference", err)
	}
	return nil
}

func (telemetry Telemetry) MarshalJSON() ([]byte, error) {
	if err := telemetry.Validate(); err != nil {
		return nil, err
	}
	type encodedTelemetry Telemetry
	return json.Marshal(encodedTelemetry(telemetry))
}

func (inference InferenceTelemetry) validate(snapshotAt time.Time) error {
	if err := inference.Validate(); err != nil {
		return err
	}
	if inference.UpdatedAt.After(snapshotAt) {
		return errors.New("updated_at is after snapshot timestamp")
	}

	metrics := []struct {
		name  string
		check func() error
	}{
		{"requested_tokens", func() error {
			return validateMetric(inference.RequestedTokens, MetricUnitTokens, snapshotAt, validateNonnegative)
		}},
		{"prompt_tokens", func() error {
			return validateMetric(inference.PromptTokens, MetricUnitTokens, snapshotAt, validateNonnegative)
		}},
		{"output_tokens", func() error {
			return validateMetric(inference.OutputTokens, MetricUnitTokens, snapshotAt, validateNonnegative)
		}},
		{"total_tokens", func() error {
			return validateMetric(inference.TotalTokens, MetricUnitTokens, snapshotAt, validateNonnegative)
		}},
		{"prompt_tokens_per_second", func() error {
			return validateMetric(inference.PromptTokensPerSecond, MetricUnitTokensPerSecond, snapshotAt, validateNonnegativeFloat)
		}},
		{"output_tokens_per_second", func() error {
			return validateMetric(inference.OutputTokensPerSecond, MetricUnitTokensPerSecond, snapshotAt, validateNonnegativeFloat)
		}},
		{"ttft_ms", func() error {
			return validateMetric(inference.TTFTMS, MetricUnitMilliseconds, snapshotAt, validateNonnegativeFloat)
		}},
		{"prompt_ms", func() error {
			return validateMetric(inference.PromptMS, MetricUnitMilliseconds, snapshotAt, validateNonnegativeFloat)
		}},
		{"generation_ms", func() error {
			return validateMetric(inference.GenerationMS, MetricUnitMilliseconds, snapshotAt, validateNonnegativeFloat)
		}},
		{"context_tokens", func() error {
			return validateMetric(inference.ContextTokens, MetricUnitTokens, snapshotAt, validateNonnegative)
		}},
		{"context_window", func() error {
			return validateMetric(inference.ContextWindow, MetricUnitTokens, snapshotAt, validatePositive)
		}},
	}
	for _, metric := range metrics {
		if err := metric.check(); err != nil {
			return fmt.Errorf("%s: %w", metric.name, err)
		}
	}
	if used, usedOK := metricValue(inference.ContextTokens); usedOK {
		if window, windowOK := metricValue(inference.ContextWindow); windowOK && used > window {
			return errors.New("context_tokens exceeds context_window")
		}
	}
	prompt, promptOK := metricValue(inference.PromptTokens)
	output, outputOK := metricValue(inference.OutputTokens)
	total, totalOK := metricValue(inference.TotalTokens)
	if promptOK && outputOK && totalOK {
		if prompt > math.MaxInt64-output || total != prompt+output {
			return errors.New("total_tokens does not equal prompt plus output tokens")
		}
	}
	return nil
}

func validateMetric[T MetricValue](metric Metric[T], unit string, snapshotAt time.Time, validateValue func(T) error) error {
	if err := metric.Validate(); err != nil {
		return err
	}
	if metric.Unit != unit {
		return errors.New("unexpected unit")
	}
	if metric.UpdatedAt.After(snapshotAt) {
		return errors.New("updated_at is after snapshot timestamp")
	}
	if metric.Value != nil {
		if err := validateValue(*metric.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateMemoryTotals(name string, used, free, total Metric[int64]) error {
	usedValue, usedOK := metricValue(used)
	freeValue, freeOK := metricValue(free)
	totalValue, totalOK := metricValue(total)
	if totalOK && usedOK && usedValue > totalValue {
		return invalidTelemetry(name + " used exceeds total")
	}
	if totalOK && freeOK && freeValue > totalValue {
		return invalidTelemetry(name + " free exceeds total")
	}
	if totalOK && usedOK && freeOK && usedValue > totalValue-freeValue {
		return invalidTelemetry(name + " used plus free exceeds total")
	}
	return nil
}

func metricValue[T MetricValue](metric Metric[T]) (T, bool) {
	if metric.Value == nil || metric.Source == MetricSourceUnavailable {
		var zero T
		return zero, false
	}
	return *metric.Value, true
}

func validatePercent(value float64) error {
	if value < 0 || value > 100 {
		return errors.New("must be between 0 and 100 percent")
	}
	return nil
}

func validateTemperature(value float64) error {
	if value < -100 || value > 200 {
		return errors.New("must be between -100 and 200 celsius")
	}
	return nil
}

func validateNonnegative(value int64) error {
	if value < 0 {
		return errors.New("must not be negative")
	}
	return nil
}

func validatePositive(value int64) error {
	if value <= 0 {
		return errors.New("must be positive")
	}
	return nil
}

func validateNonnegativeFloat(value float64) error {
	if value < 0 {
		return errors.New("must not be negative")
	}
	return nil
}

func validateName(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("must not be blank")
	}
	return nil
}

func validateLlamaHealth(value string) error {
	if value != LlamaHealthHealthy && value != LlamaHealthUnhealthy {
		return errors.New("must be healthy or unhealthy")
	}
	return nil
}

func validatePowerSource(value string) error {
	switch value {
	case "AC", "battery", "unknown":
		return nil
	default:
		return errors.New("must be AC, battery, or unknown")
	}
}

func noValidation[T any](T) error { return nil }

func invalidTelemetry(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidTelemetry, message)
}

func invalidTelemetryField(field string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrInvalidTelemetry, field, err)
}
