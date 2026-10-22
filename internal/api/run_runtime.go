package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
)

type runRuntimeService interface {
	RunRuntime(context.Context, string) (map[string]any, error)
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
