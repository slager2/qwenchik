package orchestrator

import (
	"fmt"

	"github.com/slager2/qwenchik/internal/domain"
)

func executionStage(kind string) domain.Stage {
	switch kind {
	case "search":
		return domain.StageSearch
	case "fetch", "fetch_url":
		return domain.StageFetch
	case "code_execution", "run_command_sandbox":
		return domain.StageCodeExecution
	default:
		return domain.StageSynthesis
	}
}

func normalizeResearchPlan(plan domain.Plan, title, goal string) domain.Plan {
	if !requiresWebResearch(title + "\n" + goal) {
		return plan
	}
	shape := plan
	shape.MaxSteps = max(shape.MaxSteps, len(shape.Subtasks))
	if domain.ValidatePlan(shape, 12) != nil {
		return plan
	}
	used := make(map[string]bool, len(plan.Subtasks))
	searches, fetches := 0, 0
	for _, step := range plan.Subtasks {
		used[step.ID] = true
		if step.Kind == "search" {
			searches++
		}
		if step.Kind == "fetch" {
			fetches++
		}
	}
	if searches == 0 && fetches == 0 {
		return plan
	}
	steps := append([]domain.PlanStep(nil), plan.Subtasks...)
	if searches > maxResearchSearchFacets || fetches > maxResearchFetches {
		for _, step := range steps {
			if step.Kind != "search" && step.Kind != "fetch" {
				return plan
			}
		}
		bounded := make([]domain.PlanStep, 0, maxResearchSearchFacets+maxResearchFetches)
		searches, fetches = 0, 0
		for _, step := range steps {
			if step.Kind == "search" {
				if searches == maxResearchSearchFacets {
					continue
				}
				searches++
			} else {
				if fetches == maxResearchFetches {
					continue
				}
				fetches++
			}
			bounded = append(bounded, step)
		}
		steps = bounded
	}
	newID := func(kind string) string {
		for index := 1; ; index++ {
			id := fmt.Sprintf("%s_%d", kind, index)
			if !used[id] {
				used[id] = true
				return id
			}
		}
	}
	for searches < minimumResearchSearches(title+"\n"+goal) {
		steps = append(steps, domain.PlanStep{ID: newID("search"), Kind: "search", Description: "Find independent corroborating sources for the user request: " + truncateRunes(goal, 240), ExpectedOutput: "Independent discovery leads", SuccessCriteria: []string{"Discovery results are recorded"}})
		searches++
	}
	for fetches < minimumResearchFetches(title+"\n"+goal) {
		steps = append(steps, domain.PlanStep{ID: newID("fetch"), Kind: "fetch", Description: fmt.Sprintf("Inspect distinct source site %d from the approved discovery results for the user's request", fetches+1), ExpectedOutput: "Source text with stored evidence ID", SuccessCriteria: []string{"Source evidence is stored"}})
		fetches++
	}
	ordered := make([]domain.PlanStep, 0, len(steps))
	userFetchID := ""
	if hasUserURL(goal) {
		for _, step := range steps {
			if step.Kind == "fetch" {
				userFetchID = step.ID
				ordered = append(ordered, step)
				break
			}
		}
	}
	for _, kind := range []string{"search", "fetch"} {
		for _, step := range steps {
			if step.Kind == kind && step.ID != userFetchID {
				ordered = append(ordered, step)
			}
		}
	}
	for _, step := range steps {
		if step.Kind != "search" && step.Kind != "fetch" {
			ordered = append(ordered, step)
		}
	}
	if len(ordered) > 12 {
		return plan
	}
	steps = ordered
	reserved := make(map[string]bool, len(steps))
	for _, step := range steps {
		if step.Kind != "search" && step.Kind != "fetch" {
			reserved[step.ID] = true
		}
	}
	idMap := make(map[string]string, len(steps))
	kindCounts := make(map[string]int)
	for index, step := range steps {
		if step.Kind != "search" && step.Kind != "fetch" {
			continue
		}
		for {
			kindCounts[step.Kind]++
			id := fmt.Sprintf("%s_%d", step.Kind, kindCounts[step.Kind])
			if !reserved[id] {
				reserved[id] = true
				idMap[step.ID] = id
				steps[index].ID = id
				break
			}
		}
	}
	for index, step := range steps {
		dependencies := append([]string(nil), step.Dependencies...)
		for position, dependency := range dependencies {
			if id, exists := idMap[dependency]; exists {
				dependencies[position] = id
			}
		}
		steps[index].Dependencies = dependencies
	}
	if userFetchID != "" {
		userFetchID = idMap[userFetchID]
	}
	searchIDs := make([]string, 0, maxResearchSearchFacets)
	for _, step := range steps {
		if step.Kind == "search" {
			searchIDs = append(searchIDs, step.ID)
		}
	}
	searchIndex, fetchIndex := 0, 0
	angles := []string{"overview and direct information about the requested entities", "independent corroboration of the same entities and facts", "current limitations or changes relevant to the same request"}
	for index, step := range steps {
		switch step.Kind {
		case "search":
			steps[index].Description = truncateBytesUTF8("Find sources for the exact user request. Evidence angle: "+angles[min(searchIndex, len(angles)-1)]+". Do not introduce a different subject or category. Request: "+truncateRunes(goal, 400), 1000)
			searchIndex++
			steps[index].SuccessCriteria = []string{"The discovery query was executed and its results were recorded; search snippets are leads, not verified facts."}
			steps[index].Dependencies = []string{}
			if userFetchID != "" && (noDiscoveryPattern.MatchString(goal) || !requiresWebResearch(goal)) {
				steps[index].Dependencies = []string{userFetchID}
			}
		case "fetch":
			fetchIndex++
			steps[index].Description = truncateBytesUTF8(fmt.Sprintf("Inspect distinct approved source %d that directly addresses the user's exact request: %s", fetchIndex, truncateRunes(goal, 400)), 1000)
			steps[index].SuccessCriteria = []string{"An approved source was inspected and its text was stored as task-owned evidence."}
		}
		if step.Kind != "fetch" || (index == 0 && hasUserURL(goal)) {
			if step.Kind == "fetch" {
				steps[index].Dependencies = []string{}
			}
			continue
		}
		steps[index].Dependencies = append([]string(nil), searchIDs...)
	}
	plan.Subtasks = steps
	plan.MaxSteps = max(plan.MaxSteps, len(steps))
	return plan
}
