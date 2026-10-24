package api

import (
	"encoding/json"

	"github.com/slager2/qwenchik/internal/domain"
)

// Preserve the existing failure envelope while projecting new planner fields
// through a finite catalogue. No raw model output or user text is diagnostic.
func safePlannerFailurePayload(raw json.RawMessage) json.RawMessage {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		return json.RawMessage(`{}`)
	}
	stage, role, reason := payload["failure_stage"], payload["failure_role"], payload["validation_error"]
	attempt, summary := payload["planner_attempt"], payload["plan_summary"]
	for _, key := range []string{"failure_stage", "failure_role", "validation_error", "planner_attempt", "plan_summary"} {
		delete(payload, key)
	}
	if stage == "planning" {
		payload["failure_stage"] = "planning"
		if role == "planner_initial" || role == "planner_correction" {
			payload["failure_role"] = role
		}
		if value, ok := reason.(string); ok && domain.IsSafePlannerValidationReason(value) {
			payload["validation_error"] = value
		}
		if value, ok := boundedNumber(attempt, 2, true); ok && value >= 1 {
			payload["planner_attempt"] = value
		}
		if counts, ok := summary.(map[string]any); ok {
			safe := map[string]any{}
			for _, key := range []string{"steps", "search", "fetch", "analysis", "code_execution", "external_action", "other"} {
				if value, ok := boundedNumber(counts[key], 12, true); ok {
					safe[key] = value
				}
			}
			payload["plan_summary"] = safe
		}
	}
	result, _ := json.Marshal(payload)
	return result
}
