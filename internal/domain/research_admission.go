package domain

import "time"

// Spread available operations over the remaining rounds without forcing every
// profile to two searches/two fetches. A caller may explicitly cap round steps
// lower; success still stops immediately rather than consuming a quota.
func NextResearchRoundShape(s ResearchSession) (steps, searches, fetches, llmReserve int) {
	if !s.Policy.AdaptiveEnabled {
		return 2, 1, 1, 3
	}
	if s.Policy.Version == 1 {
		return 4, 2, 2, 7
	}
	left := max(1, s.Budget.MaxRounds-s.CurrentRound-1)
	ceil := func(remaining int) int {
		remaining = max(0, remaining)
		q := remaining / left
		if remaining%left != 0 {
			q++
		}
		return min(s.Budget.MaxRoundSteps-2, max(2, q))
	}
	searches = ceil(s.Budget.MaxSearchQueries - s.Counters.SearchQueries)
	fetches = min(ceil(s.Budget.MaxFetches-s.Counters.Fetches), ceil(s.Budget.MaxEvidence-s.Counters.Evidence))
	for searches+fetches > s.Budget.MaxRoundSteps {
		if searches > fetches && searches > 2 {
			searches--
		} else if fetches > 2 {
			fetches--
		} else if searches > 2 {
			searches--
		} else {
			break
		}
	}
	steps = searches + fetches
	return steps, searches, fetches, steps + 3
}

// Shared by worker and transactional admission. Counters are durable reservations,
// not refunded on failure/restart. Plans reserve steps before they become runnable.
func ContinuationStopReason(s ResearchSession, steps, searches, fetches, llmCalls int, now time.Time) string {
	b, c := s.Budget, s.Counters
	if !now.Before(s.DeadlineAt) {
		return "deadline"
	}
	if s.CurrentRound+1 >= b.MaxRounds {
		return "max_rounds"
	}
	if s.Policy.Version == 2 && c.Steps+steps > b.MaxTotalSteps {
		return "total_steps"
	}
	if c.SearchQueries+searches > b.MaxSearchQueries {
		return "search_budget"
	}
	if c.Fetches+fetches > b.MaxFetches {
		return "fetch_budget"
	}
	if c.Evidence+fetches > b.MaxEvidence {
		return "evidence_budget"
	}
	if c.LLMCalls+llmCalls > b.MaxLLMCalls {
		return "llm_budget"
	}
	if c.FetchedBytes >= s.Policy.TaskFetchBudgetBytes {
		return "fetch_bytes"
	}
	return ""
}
