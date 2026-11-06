package orchestrator

import (
	"context"
	"fmt"

	"github.com/slager2/qwenchik/internal/domain"
)

// researchFetchTarget returns the number of distinct sources a research task
// should fetch before fetch steps are skipped: the session's saved breadth
// target (domain.ResearchFetchTargetForProfile), never below the request's
// hard minimum. Legacy and pre-target sessions use the minimum.
func researchFetchTarget(ctx context.Context, text string) int {
	required := minimumResearchFetches(text)
	if required == 0 {
		return 0
	}
	session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession)
	if !ok || session.Policy.Version != 2 || session.Policy.FetchTarget <= required {
		return required
	}
	target := min(session.Policy.FetchTarget, domain.MaxResearchFetchTarget)
	if session.Budget.MaxFetches > 0 {
		target = min(target, session.Budget.MaxFetches)
	}
	if session.Budget.MaxEvidence > 0 {
		target = min(target, session.Budget.MaxEvidence)
	}
	return max(required, target)
}

// expandResearchFetchBreadth appends deterministic fetch steps, each depending
// on every search, until the plan holds target fetches or the step capacity
// is reached. Extra steps are skipped at run time when no unused validated URL
// remains or the target is met, so they never fail a run on their own.
func expandResearchFetchBreadth(plan domain.Plan, target, capacity int) domain.Plan {
	searchIDs := []string{}
	used := map[string]bool{}
	fetches := 0
	for _, step := range plan.Subtasks {
		used[step.ID] = true
		switch step.Kind {
		case "search":
			searchIDs = append(searchIDs, step.ID)
		case "fetch":
			fetches++
		}
	}
	if len(searchIDs) == 0 || fetches == 0 || fetches >= target {
		return plan
	}
	// Insert after the last fetch so non-research steps stay at the end.
	insertAt := 0
	for index, step := range plan.Subtasks {
		if step.Kind == "fetch" {
			insertAt = index + 1
		}
	}
	extra := []domain.PlanStep{}
	for n := 1; fetches+len(extra) < target && len(plan.Subtasks)+len(extra) < capacity; n++ {
		id := fmt.Sprintf("fetch_%d", n)
		if used[id] {
			continue
		}
		used[id] = true
		extra = append(extra, domain.PlanStep{
			ID: id, Kind: "fetch",
			Description:     fmt.Sprintf("Inspect distinct approved source %d for the exact user request", fetches+len(extra)+1),
			ExpectedOutput:  "Source text with stored evidence ID",
			Dependencies:    append([]string{}, searchIDs...),
			SuccessCriteria: []string{"An approved source was inspected and its text was stored as task-owned evidence."},
		})
	}
	if len(extra) == 0 {
		return plan
	}
	subtasks := make([]domain.PlanStep, 0, len(plan.Subtasks)+len(extra))
	subtasks = append(subtasks, plan.Subtasks[:insertAt]...)
	subtasks = append(subtasks, extra...)
	subtasks = append(subtasks, plan.Subtasks[insertAt:]...)
	plan.Subtasks = subtasks
	plan.MaxSteps = len(subtasks)
	return plan
}

// researchPlanCapacity is the most steps an initial plan may hold: the worker
// step limit, the session's initial-plan capacity, and half of its total
// step budget so continuation rounds keep room.
func researchPlanCapacity(ctx context.Context, maxSteps int) int {
	capacity := maxSteps
	if session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); ok {
		if session.Budget.MaxInitialPlanSteps > 0 {
			capacity = min(capacity, session.Budget.MaxInitialPlanSteps)
		}
		if session.Budget.MaxTotalSteps > 0 {
			capacity = min(capacity, max(1, session.Budget.MaxTotalSteps/2))
		}
	}
	return capacity
}
