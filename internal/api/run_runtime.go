package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"

	"github.com/slager2/qwenchik/internal/domain"
)

type runRuntimeService interface {
	RunRuntime(context.Context, string) (map[string]any, error)
}

func (s *Server) SetRunTelemetry(snapshot func(runID string) (domain.Telemetry, uint64, bool)) {
	s.telemetryMu.Lock()
	defer s.telemetryMu.Unlock()
	s.runTelemetry = snapshot
}

func (s *Server) runTelemetrySnapshot(runID string) (domain.Telemetry, uint64, bool) {
	s.telemetryMu.RLock()
	snapshot := s.runTelemetry
	s.telemetryMu.RUnlock()
	if snapshot == nil {
		return domain.Telemetry{}, 0, false
	}
	telemetry, revision, available := snapshot(runID)
	if !available {
		return domain.Telemetry{}, 0, false
	}
	telemetry, ok := SafeTelemetry(telemetry)
	if !ok {
		return domain.Telemetry{}, 0, false
	}
	return telemetry, revision, true
}

func (s *Server) addRunTelemetry(result map[string]any, runID string) (domain.Telemetry, uint64, bool) {
	telemetry, revision, available := s.runTelemetrySnapshot(runID)
	if available {
		result["telemetry"] = telemetry
		result["telemetry_revision"] = revision
	}
	return telemetry, revision, available
}

func (s *Server) runRuntime(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(runRuntimeService)
	if !ok {
		writeError(w, 503, "runtime_unavailable", "Состояние runtime недоступно.", true)
		return
	}
	result, err := service.RunRuntime(r.Context(), r.PathValue("runID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	result["runtime"] = s.runtimeHealth()
	s.addRunTelemetry(result, r.PathValue("runID"))
	writeJSON(w, 200, result)
}

func safeHarnessEvent(raw json.RawMessage) map[string]any {
	result := map[string]any{}
	if len(raw) > 16384 {
		return result
	}
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return result
	}
	for _, key := range []string{"version", "attempt", "max_continuations", "number", "original_tool_result_bytes", "retained_tool_result_bytes", "reduced_bytes", "previous_bytes", "snapshot_bytes", "accepted_evidence", "completed_steps", "done_criteria", "contradictions", "open_questions"} {
		if n, ok := input[key].(float64); ok && n >= 0 && n <= 1<<40 && math.Trunc(n) == n {
			result[key] = n
		}
	}
	for _, key := range []string{"bundle_hash", "hash", "slot", "progress_fingerprint", "failure_hash"} {
		if value, ok := input[key].(string); ok && validFingerprint(value) {
			result[key] = value
		}
	}
	for _, key := range []string{"verdict", "phase", "reason"} {
		if value, ok := input[key].(string); ok {
			for _, allowed := range []string{"PASS", "REPAIR", "RESEARCH", "UNCERTAIN", "BLOCK", "evidence_readiness", "role_tool_results", "compaction", "context_compaction", "context.compacted"} {
				if value == allowed {
					result[key] = value
				}
			}
		}
	}
	if items, ok := input["new_steps"].([]any); ok {
		steps := []string{}
		for _, item := range items {
			if value, ok := item.(string); ok && safeStepID(value) && len(steps) < 4 {
				steps = append(steps, value)
			}
		}
		result["new_steps"] = steps
	}
	if items, ok := input["missing"].([]any); ok {
		missing := []string{}
		for _, item := range items {
			if value, ok := item.(string); ok && len(missing) < 20 {
				runes := []rune(value)
				missing = append(missing, string(runes[:min(500, len(runes))]))
			}
		}
		result["missing"] = missing
	}
	if status, ok := input["status"].(string); ok {
		for _, allowed := range []string{"candidate", "evaluating", "ready", "approved", "rejected"} {
			if status == allowed {
				result["status"] = status
			}
		}
	}
	if evaluation, ok := input["evaluation"].(map[string]any); ok {
		safe := map[string]any{}
		for _, key := range []string{"case_count", "baseline_passed", "candidate_passed", "baseline_ms", "candidate_ms"} {
			if n, ok := evaluation[key].(float64); ok && n >= 0 && n <= 300000 && math.Trunc(n) == n {
				safe[key] = n
			}
		}
		if passed, ok := evaluation["passed"].(bool); ok {
			safe["passed"] = passed
		}
		result["evaluation"] = safe
	}
	return result
}
