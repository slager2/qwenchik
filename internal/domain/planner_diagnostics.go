package domain

import "fmt"

// PlannerSemanticValidationReasons contains backend-generated messages only.
// Return a fresh slice so callers cannot alter the public diagnostics catalogue.
func PlannerSemanticValidationReasons() []string {
	reasons := []string{}
	for _, detail := range []string{
		"a user-provided URL must be fetched before discovery search",
		"all discovery search steps must precede source fetch steps",
		"search results must be fetched before synthesis",
		"discovery search is not allowed for a self-contained request",
		"research synthesis is performed by the finalizer; do not add a text.inspect analysis step",
		"discovery searches must be independent facets, not retries of one another",
		"every discovered-source fetch must depend on all discovery searches",
		"search questions must contain text",
		"search questions must be distinct",
		"required research steps exceed the configured step capacity",
		"corrected plan must preserve every original search facet verbatim",
		"research plan contains inconsistent step IDs, dependencies, or bounds",
		"research plan requires at least 2 distinct search steps",
		"research plans support at most 3 independent search facets",
		"research plans support at most 4 fetch steps",
		"required search, fetch, ordering, or dependency constraints were not satisfied",
	} {
		reasons = append(reasons, "research plan is incomplete: "+detail)
	}
	for _, count := range []int{1, 2} {
		reasons = append(reasons, fmt.Sprintf("research plan is incomplete: research plan requires at least %d distinct fetch steps", count))
	}
	for _, count := range []int{2, 3} {
		reasons = append(reasons, fmt.Sprintf("research plan is incomplete: search facets cannot fit losslessly within %d searches and 1000 bytes per description", count))
	}
	return reasons
}

func IsSafePlannerValidationReason(value string) bool {
	for _, reason := range PlannerSemanticValidationReasons() {
		if value == reason {
			return true
		}
	}
	switch value {
	case "search_unavailable: research requires an enabled search provider",
		"invalid_model_output: planner output failed structural validation",
		"unsupported_step: planner selected an unavailable step kind",
		"context_budget_exceeded: planner input exceeds the configured capacity",
		"planning_failed: planner request could not be completed":
		return true
	}
	return false
}
