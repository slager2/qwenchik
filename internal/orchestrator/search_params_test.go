package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
)

var searchParamsNow = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

func TestResearchSearchArgumentsSetLanguageRecencyAndPreferredDomains(t *testing.T) {
	state := checkpointState{ToolResults: map[string]any{
		"search_1": []research.SearchHit{
			{Provider: "agentsearcher", Query: "Stipendium Hungaricum scholarship", URL: "https://blog.example.com/sh"},
			{Provider: "agentsearcher", Query: "Stipendium Hungaricum scholarship", URL: "https://stipendiumhungaricum.hu/apply/"},
			{Provider: "agentsearcher", Query: "Stipendium Hungaricum scholarship", URL: "https://www.studyinkorea.go.kr/en/main.do"},
		},
		// Checkpointed hits are decoded JSON.
		"search_2": []any{map[string]any{"provider": "agentsearcher", "query": "GKS", "url": "https://www.mit.edu/admissions"}},
	}}
	first := researchSearchArguments(domain.PlanStep{ID: "search_1", Kind: "search"}, "стипендия Венгрия 2026 дедлайн", state, searchParamsNow)
	if first["language"] != "ru" || first["recency"] != "year" || first["preferred_domains"] != nil || first["max_results"] != researchSearchResults {
		t.Fatalf("initial facet arguments = %#v", first)
	}
	followUp := researchSearchArguments(domain.PlanStep{ID: "continue_1_search", Kind: "search"}, "Stipendium Hungaricum eligibility", state, searchParamsNow)
	domains, _ := followUp["preferred_domains"].([]string)
	if followUp["language"] != "en" || followUp["recency"] != nil || strings.Join(domains, ",") != "studyinkorea.go.kr,mit.edu,stipendiumhungaricum.hu" {
		t.Fatalf("follow-up arguments = %#v", followUp)
	}
	if _, err := json.Marshal(followUp); err != nil {
		t.Fatal(err)
	}
}

func TestDateSensitiveText(t *testing.T) {
	for text, want := range map[string]bool{
		"GKS 2026 application":           true,
		"сроки подачи документов":        true,
		"Stipendium Hungaricum deadline": true,
		"history of the programme 2019":  false,
		"обзор университетов Кореи":      false,
		"кандидаты и требования":         false,
		"приём заявок открыт":            true,
	} {
		if got := dateSensitiveText(text, searchParamsNow); got != want {
			t.Fatalf("%q: %v", text, got)
		}
	}
}
