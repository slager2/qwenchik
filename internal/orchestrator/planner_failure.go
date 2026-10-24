package orchestrator

import (
	"errors"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
)

// Cause is retained for errors.Is/As; diagnostics never expose its raw text.
type plannerFailureError struct {
	Cause           error
	Role            string
	Attempt         int
	ValidationError string
	PlanSummary     map[string]int
}

func (e *plannerFailureError) Error() string { return e.ValidationError }
func (e *plannerFailureError) Unwrap() error { return e.Cause }

const plannerSemanticFallback = "research plan is incomplete: required search, fetch, ordering, or dependency constraints were not satisfied"

// This finite catalogue is the public validation_error allowlist. Match entire
// messages, never prefixes: even an error wrapping a semantic sentinel may
// contain untrusted IDs/text. Numeric variants are generated from bounded
// backend counts, not copied from the error string.
var plannerSemanticReasons = func() map[string]struct{} {
	reasons := map[string]struct{}{}
	for _, reason := range domain.PlannerSemanticValidationReasons() {
		reasons[reason] = struct{}{}
	}
	reasons[plannerSemanticFallback] = struct{}{}
	return reasons
}()

func safePlannerReason(err error) string {
	switch {
	case errors.Is(err, errResearchPlanIncomplete):
		if _, known := plannerSemanticReasons[err.Error()]; known {
			return err.Error()
		}
		return plannerSemanticFallback
	case errors.Is(err, errSearchUnavailable):
		return "search_unavailable: research requires an enabled search provider"
	case errors.Is(err, errInvalidModelOutput), errors.Is(err, domain.ErrInvalidPlan), errors.Is(err, llm.ErrInvalidModelOutput):
		return "invalid_model_output: planner output failed structural validation"
	case errors.Is(err, errUnsupportedStep):
		return "unsupported_step: planner selected an unavailable step kind"
	case errors.Is(err, errInputBudgetExceeded):
		return "context_budget_exceeded: planner input exceeds the configured capacity"
	default:
		return "planning_failed: planner request could not be completed"
	}
}

func publicPlannerError(reason string) string {
	switch reason {
	case "research plan is incomplete: search results must be fetched before synthesis":
		return "План исследования не прошёл проверку: поисковые шаги не были дополнены загрузкой источников."
	case "research plan is incomplete: required research steps exceed the configured step capacity":
		return "План исследования не прошёл проверку: обязательные поисковые шаги и загрузки источников не помещаются в доступное число шагов."
	case "research plan is incomplete: search facets cannot fit losslessly within 2 searches and 1000 bytes per description",
		"research plan is incomplete: search facets cannot fit losslessly within 3 searches and 1000 bytes per description":
		return "План исследования не прошёл проверку: все направления поиска невозможно объединить в допустимое число шагов без потери исходного текста."
	default:
		return publicError("research_plan_incomplete")
	}
}

func plannerPlanSummary(plan domain.Plan) map[string]int {
	counts := map[string]int{"steps": len(plan.Subtasks), "search": 0, "fetch": 0, "analysis": 0, "code_execution": 0, "external_action": 0, "other": 0}
	for _, step := range plan.Subtasks {
		switch step.Kind {
		case "search", "fetch", "analysis", "code_execution", "external_action":
			counts[step.Kind]++
		default:
			counts["other"]++
		}
	}
	return counts
}
