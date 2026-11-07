package orchestrator

import (
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
)

// Regression: the Stipendium Hungaricum call PDF was excerpted with the GKS
// query because that search sorted first.
func TestFetchOriginalQueryPrefersBestMatchingSearch(t *testing.T) {
	url := "https://stipendiumhungaricum.hu/uploads/2026/call-for-applications.pdf"
	state := checkpointState{ToolResults: map[string]any{
		"continue_1_search_3": []research.SearchHit{{URL: url, Title: "Call for Applications", Query: "GKS Global Korea Scholarship 2026 Kazakhstan quota"}},
		"search_1":            []research.SearchHit{{URL: url, Title: "Call for Applications", Query: "Stipendium Hungaricum 2026 call for applications deadline"}},
	}}
	if got := fetchOriginalQuery(url, state, "goal"); got != "Stipendium Hungaricum 2026 call for applications deadline" {
		t.Fatalf("query = %q", got)
	}
	if got := fetchOriginalQuery("https://other.example/x", state, "fallback goal"); got != "fallback goal" {
		t.Fatalf("fallback = %q", got)
	}
}
