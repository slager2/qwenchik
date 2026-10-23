package openai

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
)

func newChatTelemetry(at time.Time, request llm.ChatRequest, status domain.InferenceStatus) llm.ChatTelemetry {
	unavailableInt := func() domain.Metric[int64] {
		return domain.NewUnavailableMetric[int64](domain.MetricUnitTokens, at)
	}
	unavailableFloat := func(unit string) domain.Metric[float64] {
		return domain.NewUnavailableMetric[float64](unit, at)
	}

	telemetry := llm.ChatTelemetry{
		InferenceTelemetry: domain.InferenceTelemetry{
			Status:                status,
			Source:                "llama.cpp",
			UpdatedAt:             at,
			RequestedTokens:       unavailableInt(),
			PromptTokens:          unavailableInt(),
			OutputTokens:          unavailableInt(),
			TotalTokens:           unavailableInt(),
			CachedPromptTokens:    unavailableInt(),
			PromptTokensPerSecond: unavailableFloat(domain.MetricUnitTokensPerSecond),
			OutputTokensPerSecond: unavailableFloat(domain.MetricUnitTokensPerSecond),
			TTFTMS:                unavailableFloat(domain.MetricUnitMilliseconds),
			PromptMS:              unavailableFloat(domain.MetricUnitMilliseconds),
			GenerationMS:          unavailableFloat(domain.MetricUnitMilliseconds),
			ContextTokens:         unavailableInt(),
			ContextWindow:         unavailableInt(),
		},
	}
	if request.MaxTokens > 0 {
		telemetry.RequestedTokens = domain.NewExactMetric(int64(request.MaxTokens), domain.MetricUnitTokens, at)
	}
	if request.ContextWindow > 0 {
		telemetry.ContextWindow = domain.NewExactMetric(int64(request.ContextWindow), domain.MetricUnitTokens, at)
	}
	return telemetry
}

// applyProviderTelemetry tolerates missing, malformed, and unsupported metrics.
// Only valid nonnegative scalar values become exact metrics; all other values
// remain unavailable (or retain a live estimate already collected).
func applyProviderTelemetry(telemetry *llm.ChatTelemetry, usage, timings json.RawMessage, at time.Time) {
	usageFields := decodeObject(usage)
	if value, ok := intMetric(usageFields["prompt_tokens"]); ok {
		telemetry.PromptTokens = domain.NewExactMetric(value, domain.MetricUnitTokens, at)
	}
	if value, ok := intMetric(usageFields["completion_tokens"]); ok {
		telemetry.OutputTokens = domain.NewExactMetric(value, domain.MetricUnitTokens, at)
	}
	if value, ok := intMetric(usageFields["total_tokens"]); ok {
		telemetry.TotalTokens = domain.NewExactMetric(value, domain.MetricUnitTokens, at)
	}
	if promptDetails := decodeObject(usageFields["prompt_tokens_details"]); promptDetails != nil {
		if value, ok := intMetric(promptDetails["cached_tokens"]); ok {
			telemetry.CachedPromptTokens = domain.NewExactMetric(value, domain.MetricUnitTokens, at)
		}
	}

	timingFields := decodeObject(timings)
	if value, ok := intMetric(timingFields["prompt_n"]); ok && telemetry.PromptTokens.Source == domain.MetricSourceUnavailable {
		telemetry.PromptTokens = domain.NewExactMetric(value, domain.MetricUnitTokens, at)
	}
	if value, ok := intMetric(timingFields["predicted_n"]); ok && telemetry.OutputTokens.Source == domain.MetricSourceUnavailable {
		telemetry.OutputTokens = domain.NewExactMetric(value, domain.MetricUnitTokens, at)
	}
	if value, ok := floatMetric(timingFields["prompt_ms"]); ok {
		telemetry.PromptMS = domain.NewExactMetric(value, domain.MetricUnitMilliseconds, at)
	}
	if value, ok := floatMetric(timingFields["prompt_per_second"]); ok {
		telemetry.PromptTokensPerSecond = domain.NewExactMetric(value, domain.MetricUnitTokensPerSecond, at)
	} else if value, valueOK := metricIntValue(telemetry.PromptTokens); valueOK {
		if milliseconds, durationOK := metricFloatValue(telemetry.PromptMS); durationOK && milliseconds > 0 {
			telemetry.PromptTokensPerSecond = domain.NewExactMetric(float64(value)*1000/milliseconds, domain.MetricUnitTokensPerSecond, at)
		}
	}
	if value, ok := floatMetric(timingFields["predicted_ms"]); ok {
		telemetry.GenerationMS = domain.NewExactMetric(value, domain.MetricUnitMilliseconds, at)
	}
	if value, ok := floatMetric(timingFields["predicted_per_second"]); ok {
		telemetry.OutputTokensPerSecond = domain.NewExactMetric(value, domain.MetricUnitTokensPerSecond, at)
	} else if value, valueOK := metricIntValue(telemetry.OutputTokens); valueOK {
		if milliseconds, durationOK := metricFloatValue(telemetry.GenerationMS); durationOK && milliseconds > 0 {
			telemetry.OutputTokensPerSecond = domain.NewExactMetric(float64(value)*1000/milliseconds, domain.MetricUnitTokensPerSecond, at)
		}
	}
	if value, ok := intMetric(timingFields["cache_n"]); ok {
		telemetry.CachedPromptTokens = domain.NewExactMetric(value, domain.MetricUnitTokens, at)
	}
	reconcileTokenTotals(telemetry, at)
	telemetry.UpdatedAt = at
}

func reconcileTokenTotals(telemetry *llm.ChatTelemetry, at time.Time) {
	prompt, promptKnown := metricIntValue(telemetry.PromptTokens)
	output, outputKnown := metricIntValue(telemetry.OutputTokens)
	total, totalKnown := metricIntValue(telemetry.TotalTokens)
	if totalKnown && promptKnown && outputKnown {
		if prompt > math.MaxInt64-output || total != prompt+output {
			telemetry.TotalTokens = domain.NewUnavailableMetricWithError[int64](domain.MetricUnitTokens, at, domain.MetricErrorCollectionFailed)
			totalKnown = false
		}
	}
	if totalKnown && promptKnown && !outputKnown && total >= prompt {
		output = total - prompt
		telemetry.OutputTokens = domain.NewExactMetric(output, domain.MetricUnitTokens, at)
		outputKnown = true
	}
	if totalKnown && outputKnown && !promptKnown && total >= output {
		prompt = total - output
		telemetry.PromptTokens = domain.NewExactMetric(prompt, domain.MetricUnitTokens, at)
		promptKnown = true
	}
	if !totalKnown && promptKnown && outputKnown && prompt <= math.MaxInt64-output {
		total = prompt + output
		source := domain.MetricSourceExact
		if telemetry.PromptTokens.Source == domain.MetricSourceEstimated || telemetry.OutputTokens.Source == domain.MetricSourceEstimated {
			source = domain.MetricSourceEstimated
		}
		telemetry.TotalTokens = domain.Metric[int64]{Value: &total, Source: source, Unit: domain.MetricUnitTokens, UpdatedAt: at}
		totalKnown = true
	}
	if totalKnown {
		source := telemetry.TotalTokens.Source
		value := total
		telemetry.ContextTokens = domain.Metric[int64]{Value: &value, Source: source, Unit: domain.MetricUnitTokens, UpdatedAt: at}
	} else {
		telemetry.ContextTokens = domain.NewUnavailableMetric[int64](domain.MetricUnitTokens, at)
	}
}

func metricIntValue(metric domain.Metric[int64]) (int64, bool) {
	if metric.Value == nil || metric.Source == domain.MetricSourceUnavailable {
		return 0, false
	}
	return *metric.Value, true
}

func metricFloatValue(metric domain.Metric[float64]) (float64, bool) {
	if metric.Value == nil || metric.Source == domain.MetricSourceUnavailable {
		return 0, false
	}
	return *metric.Value, true
}

func decodeObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 || rawIsNull(raw) {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields
}

func rawIsNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func intMetric(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || rawIsNull(raw) {
		return 0, false
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	return value, err == nil && value >= 0
}

func floatMetric(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || rawIsNull(raw) {
		return 0, false
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	return value, err == nil && value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}
