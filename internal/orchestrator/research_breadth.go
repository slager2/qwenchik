package orchestrator

import (
	"context"
	"fmt"
	"unicode/utf8"

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

// researchSearchTarget returns how many distinct search facets a research
// task's initial plan runs: the session's saved search breadth target
// (domain.ResearchSearchTargetForProfile), at most half of the search query
// budget so continuation rounds keep queries, never below the request's hard
// minimum. Legacy and pre-target sessions use the minimum.
func researchSearchTarget(ctx context.Context, text string) int {
	required := minimumResearchSearches(text)
	if required == 0 {
		return 0
	}
	session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession)
	if !ok || session.Policy.Version != 2 || session.Policy.SearchTarget <= required {
		return required
	}
	target := min(session.Policy.SearchTarget, maxBackendSearchFacets)
	if session.Budget.MaxSearchQueries > 0 {
		target = min(target, session.Budget.MaxSearchQueries/2)
	}
	return max(required, target)
}

// researchAngleSuffixes vary backend-added search facets so their queries
// stay distinct; Cyrillic goals get Russian suffixes.
var researchAngleSuffixes = map[string][]string{
	"en": {"overview", "guide", "comparison", "examples", "best practices"},
	"ru": {"обзор", "руководство", "сравнение", "примеры", "рекомендации"},
}

// widenResearchSearchFacets inserts backend search facets after the last
// search until the plan holds target searches or the step capacity is
// reached. Each added facet searches concise keywords of the user's own
// request (in the user's language) with a distinct angle, and every
// discovered-source fetch is made to depend on it.
func widenResearchSearchFacets(plan domain.Plan, goal string, target, capacity int) domain.Plan {
	searches, lastSearch := 0, -1
	used := map[string]bool{}
	queries := []string{}
	for index, step := range plan.Subtasks {
		used[step.ID] = true
		if step.Kind == "search" {
			searches++
			lastSearch = index
			queries = append(queries, step.Query)
		}
	}
	keywords := goalKeywordQuery(goal, 8)
	if searches == 0 || searches >= target || keywords == "" {
		return plan
	}
	suffixes := researchAngleSuffixes[reportLanguage(goal)]
	added := []domain.PlanStep{}
	for n, angle := 1, 0; searches+len(added) < target && len(plan.Subtasks)+len(added) < capacity && angle < len(suffixes); n++ {
		id := fmt.Sprintf("search_%d", n)
		if used[id] {
			continue
		}
		query := conciseSearchQuery(keywords + " " + suffixes[angle])
		angle++
		if query == "" || duplicateResearchQuery(query, queries) {
			continue
		}
		used[id] = true
		queries = append(queries, query)
		facet := fmt.Sprintf("Independent discovery angle %d for the exact user request: %s", searches+len(added)+1, query)
		description := deterministicSearchFacetPrefix + facet + ". User request: " + goal
		if utf8.RuneCountInString(description) > 1000 {
			description = facet
		}
		added = append(added, domain.PlanStep{
			ID: id, Kind: "search", Description: description, Query: query,
			Dependencies: []string{}, ExpectedOutput: "Independent discovery leads",
			SuccessCriteria: []string{"The discovery query was executed and its results were recorded; search snippets are leads, not verified facts."},
		})
	}
	if len(added) == 0 {
		return plan
	}
	subtasks := make([]domain.PlanStep, 0, len(plan.Subtasks)+len(added))
	subtasks = append(subtasks, plan.Subtasks[:lastSearch+1]...)
	subtasks = append(subtasks, added...)
	subtasks = append(subtasks, plan.Subtasks[lastSearch+1:]...)
	userURLFetch := hasUserURL(goal)
	for index := range subtasks {
		step := &subtasks[index]
		if step.Kind != "fetch" || index == 0 && userURLFetch {
			continue
		}
		step.Dependencies = append([]string{}, step.Dependencies...)
		for _, search := range added {
			step.Dependencies = append(step.Dependencies, search.ID)
		}
	}
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
