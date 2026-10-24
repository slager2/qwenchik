package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"

	"github.com/slager2/qwenchik/internal/domain"
)

const maxPublicHarnessEventBytes = 512 << 10

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

func (s *Server) sanitizeRunInferenceSummary(result map[string]any) {
	var summary domain.InferenceSummary
	switch value := result["inference_summary"].(type) {
	case domain.InferenceSummary:
		summary = value
	case *domain.InferenceSummary:
		if value == nil {
			delete(result, "inference_summary")
			return
		}
		summary = *value
	default:
		delete(result, "inference_summary")
		return
	}
	safe, ok := SafeInferenceSummary(summary)
	if !ok {
		delete(result, "inference_summary")
		return
	}
	result["inference_summary"] = safe
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
	s.sanitizeRunInferenceSummary(result)
	writeJSON(w, 200, result)
}

func safeHarnessEvent(raw json.RawMessage) map[string]any {
	result := map[string]any{}
	if len(raw) > maxPublicHarnessEventBytes {
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
			for _, allowed := range []string{"PASS", "PARTIAL", "REPAIR", "RESEARCH", "UNCERTAIN", "BLOCK", "evidence_readiness", "role_tool_results", "compaction", "context_compaction", "context.compacted"} {
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
	readinessPhase, _ := input["phase"].(string)
	if items, ok := input["supplementary_missing"].([]any); ok && readinessPhase == "evidence_readiness" {
		missing := []string{}
		for _, item := range items {
			if value, ok := item.(string); ok && len(missing) < 20 {
				runes := []rune(value)
				missing = append(missing, string(runes[:min(500, len(runes))]))
			}
		}
		result["supplementary_missing"] = missing
	}
	if items, ok := input["criteria"].([]any); ok && len(items) <= 120 && readinessPhase == "evidence_readiness" {
		criteria := make([]map[string]any, 0, len(items))
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			stepID, stepOK := entry["step_id"].(string)
			index, indexOK := entry["criterion_index"].(float64)
			classification, classificationOK := entry["classification"].(string)
			met, metOK := entry["met"].(bool)
			if !stepOK || !safeStepID(stepID) || !indexOK || index < 0 || index >= 10 || math.Trunc(index) != index || !classificationOK || classification != "critical" && classification != "supplementary" || !metOK {
				continue
			}
			projected := map[string]any{"step_id": stepID, "criterion_index": index, "classification": classification, "met": met}
			if evidence, ok := entry["evidence_ids"].([]any); ok {
				ids := []string{}
				for _, candidate := range evidence {
					if id, ok := candidate.(string); ok && safeStepID(id) && len(ids) < 16 {
						duplicate := false
						for _, prior := range ids {
							if prior == id {
								duplicate = true
								break
							}
						}
						if !duplicate {
							ids = append(ids, id)
						}
					}
				}
				projected["evidence_ids"] = ids
			}
			criteria = append(criteria, projected)
		}
		result["criteria"] = criteria
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
