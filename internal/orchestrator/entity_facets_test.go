package orchestrator

import (
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestGoalEntities(t *testing.T) {
	for goal, want := range map[string]string{
		"Сравни стипендии Stipendium Hungaricum, GKS и Türkiye Burslari: дедлайны 2026 и требования, дай PDF": "Stipendium Hungaricum|GKS|Türkiye Burslari",
		"Compare Seoul National University and the University of Tokyo admission requirements":                "Seoul National University|University of Tokyo",
		"Расскажи про программу «Глобальное образование» и DAAD":                                              "Глобальное образование|DAAD",
		"как выучить биоинформатику с нуля":                                                                   "",
	} {
		if got := strings.Join(goalEntities(goal), "|"); got != want {
			t.Fatalf("%q: got %q want %q", goal, got, want)
		}
	}
}

func TestEnsureEntitySearchFacetsAddsOneFacetPerUncoveredEntity(t *testing.T) {
	goal := "Сравни стипендии Stipendium Hungaricum, GKS и Türkiye Burslari: дедлайны и требования"
	plan := domain.Plan{Subtasks: []domain.PlanStep{
		{ID: "search_1", Kind: "search", Description: "Stipendium Hungaricum", Query: "Stipendium Hungaricum application deadline", SuccessCriteria: []string{"x"}},
		{ID: "fetch_1", Kind: "fetch", Dependencies: []string{"search_1"}, SuccessCriteria: []string{"x"}},
	}}
	got := ensureEntitySearchFacets(plan, goal, 8, 20, searchParamsNow)
	queries := []string{}
	for _, step := range got.Subtasks {
		if step.Kind == "search" {
			queries = append(queries, step.Query)
		}
	}
	// The user-language slot stays free; GKS and Türkiye Burslari get facets.
	if strings.Join(queries, "|") != "Stipendium Hungaricum application deadline|GKS scholarship 2026 deadline requirements|Türkiye Burslari 2026 deadline requirements" {
		t.Fatalf("queries = %q", queries)
	}
	fetch := got.Subtasks[len(got.Subtasks)-1]
	if fetch.Kind != "fetch" || len(fetch.Dependencies) != 3 || got.MaxSteps != len(got.Subtasks) {
		t.Fatalf("fetch = %#v", fetch)
	}
	if limited := ensureEntitySearchFacets(plan, goal, 2, 20, searchParamsNow); len(limited.Subtasks) != 2 {
		t.Fatalf("limit ignored: %d steps", len(limited.Subtasks))
	}
}
