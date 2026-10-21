package api

import (
	"encoding/json"
	"math"
	"time"
)

func (s *Server) SetRuntimeStatus(status func() map[string]any) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.runtimeStatus = status
}

func (s *Server) runtimeHealth() map[string]any {
	s.healthMu.RLock()
	status := s.runtimeStatus
	s.healthMu.RUnlock()
	if status == nil {
		return map[string]any{"status": "unknown"}
	}
	return SafeRuntimeStatus(status())
}

func runtimeObject(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 16384 {
		return nil
	}
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func safeRuntimeFields(input map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"enabled", "available", "active", "keep_awake", "llama_healthy", "healthy", "cancel_requested"} {
		if value, ok := input[key].(bool); ok {
			result[key] = value
		}
	}
	for key, maximum := range map[string]float64{
		"free_ram_bytes": 1 << 50, "rss_bytes": 1 << 50, "heap_alloc_bytes": 1 << 50,
		"gpu_free_vram_mb": 1 << 30, "gpu_temp_c": 200, "llama_health_latency": float64(time.Hour),
		"resource_probe_attempts": 1000, "max_resume_attempts": 1000, "retry_count": 1000,
	} {
		if value, ok := input[key].(float64); ok && value >= 0 && value <= maximum && !math.IsNaN(value) && !math.IsInf(value, 0) {
			result[key] = value
		} else if value, exists := input[key]; exists && value == nil {
			result[key] = nil
		}
	}
	for key, allowed := range map[string][]string{
		"status":           {"unknown", "ok", "paused", "queued", "running", "planning", "verifying", "failed", "completed", "cancelled", "blocked", "needs_review", "partial", "budget_exhausted"},
		"power_source":     {"unknown", "AC", "battery"},
		"provider":         {"systemd-inhibit", "unavailable", "unknown", "none"},
		"sleep_policy":     {"disabled", "keep_awake", "opt_in", "requested", "enabled", "best_effort", "unknown"},
		"pause_reason":     {"resource_guard", "recovery_required", "manual", ""},
		"terminal_outcome": {"complete", "completed", "partial", "blocked", "needs_review", "budget_exhausted", "cancelled", "failed", ""},
		"resource_reason":  {"low_ram", "low_vram", "high_rss", "gpu_temperature", "llama_unhealthy", "llama_timeout", "model_timeout", "model_oom", "model_resource", "telemetry_unavailable", ""},
		"error_code":       {"resource_guard_exhausted", "deadline_exceeded", "failed_recovery_required", "fetch_outcome_unknown", "generation_interrupted", ""},
	} {
		if value, ok := input[key].(string); ok {
			for _, candidate := range allowed {
				if value == candidate {
					result[key] = value
					break
				}
			}
		}
	}
	for _, key := range []string{"snapshot_at", "resume_after", "deadline_at"} {
		if value, ok := input[key].(string); ok && len(value) <= 64 {
			if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
				result[key] = at.UTC()
			}
		}
	}
	for _, key := range []string{"failure_reason", "llama_health_reason"} {
		if value, ok := input[key].(string); ok && value != "" {
			result[key] = "runtime provider reported unavailable"
		}
	}
	for _, key := range []string{"warnings", "active_warnings"} {
		if values, ok := input[key].([]any); ok {
			warnings := []string{}
			for _, value := range values {
				if len(warnings) == 16 {
					break
				}
				if text, ok := value.(string); ok {
					for _, metric := range []string{"RAM", "process RSS", "GPU VRAM", "GPU temperature", "llama health"} {
						if text == metric+" unknown; configured guard cannot evaluate this metric" {
							warnings = append(warnings, text)
						}
					}
				}
			}
			if len(values) > 0 && len(warnings) == 0 {
				warnings = append(warnings, "runtime warning reported")
			}
			result[key] = warnings
		}
	}
	return result
}

func SafeRuntimeStatus(input map[string]any) map[string]any {
	result := safeRuntimeFields(runtimeObject(input))
	for _, key := range []string{"resources", "last_snapshot", "snapshot", "sleep", "keep_awake", "sleep_policy"} {
		if object := runtimeObject(input[key]); object != nil {
			result[key] = safeRuntimeFields(object)
		}
	}
	return result
}

func resourceEventType(kind string) bool {
	switch kind {
	case "run.paused", "run.resumed", "run.resource_probe", "run.blocked", "run.failed":
		return true
	}
	return false
}

func safeResourceEvent(raw json.RawMessage) map[string]any {
	if len(raw) > 16384 {
		return map[string]any{}
	}
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	return safeRuntimeFields(input)
}
