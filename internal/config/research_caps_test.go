package config

import (
	"github.com/slager2/qwenchik/internal/domain"
	"testing"
)

func TestRoundProfilesHaveReachableFiniteBudgets(t *testing.T) {
	isolatedResearchEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name                                                   string
		rounds, steps, queries, fetches, llm, sections, claims, minRounds int
	}{
		{"quick", 1, 12, 4, 6, 24, 4, 8, 0}, {"standard", 3, 20, 8, 16, 48, 6, 24, 0}, {"deep_laptop", 5, 32, 16, 24, 64, 8, 32, 2}, {"long_laptop", 8, 64, 32, 48, 96, 8, 64, 8}, {"overnight_laptop", 10, 96, 48, 64, 128, 8, 64, 10},
	} {
		p, err := cfg.ResearchProfile(want.name)
		if err != nil {
			t.Fatal(err)
		}
		b := p.Budget
		if p.Version != 2 || b.MaxRounds != want.rounds || b.MaxTotalSteps != want.steps || b.MaxSearchQueries != want.queries || b.MaxFetches != want.fetches || b.MaxLLMCalls != want.llm || b.MaxReportSections != want.sections || b.MaxCitationClaims != want.claims || b.MinRounds != want.minRounds {
			t.Fatalf("profile %s=%+v", want.name, b)
		}
		// A normal initial 2-search/2-fetch plan can actually reach every nominal round.
		if 4+4*(b.MaxRounds-1) > b.MaxTotalSteps || 2*b.MaxRounds > b.MaxSearchQueries || 2*b.MaxRounds > b.MaxFetches || 2*b.MaxRounds > b.MaxEvidence || 5*b.MaxRounds+1 > b.MaxLLMCalls {
			t.Fatalf("unreachable profile: %+v", b)
		}
	}
}

func TestResearchCapsAreIndependentAndInvalidValuesFail(t *testing.T) {
	isolatedResearchEnv(t)
	t.Setenv("MAX_ROUND_STEPS", "4")
	t.Setenv("MAX_INITIAL_PLAN_STEPS", "6")
	t.Setenv("MAX_TOTAL_STEPS", "40")
	t.Setenv("MAX_TOTAL_ROUNDS", "5")
	t.Setenv("MAX_SEARCH_QUERIES", "20")
	t.Setenv("MAX_FETCHES", "18")
	t.Setenv("MAX_LLM_CALLS", "70")
	t.Setenv("MAX_EVIDENCE", "18")
	t.Setenv("MAX_QUESTIONS", "30")
	t.Setenv("MAX_CITATION_CLAIMS", "20")
	t.Setenv("MAX_REPORT_SECTIONS", "5")
	t.Setenv("MAX_REPORT_PARAGRAPHS", "15")
	t.Setenv("MAX_REPORT_BYTES", "65536")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.ResearchProfile(domain.ResearchProfileOvernight)
	if err != nil {
		t.Fatal(err)
	}
	b := p.Budget
	if b.MaxInitialPlanSteps != 6 || b.MaxRoundSteps != 4 || b.MaxTotalSteps != 40 || b.MaxRounds != 5 || b.MaxSearchQueries != 20 || b.MaxFetches != 18 || b.MaxLLMCalls != 70 || b.MaxEvidence != 18 || b.MaxQuestionsCreated != 30 || b.MaxCitationClaims != 20 || b.MaxReportSections != 5 || b.MaxReportParagraphs != 15 || b.MaxReportBytes != 65536 {
		t.Fatalf("caps not applied independently: %+v", b)
	}
	for _, key := range []string{"MAX_INITIAL_PLAN_STEPS", "MAX_ROUND_STEPS", "MAX_TOTAL_ROUNDS", "MAX_TOTAL_STEPS", "MAX_LLM_CALLS", "MAX_REPORT_BYTES"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "0")
			if _, err := Load(); err == nil {
				t.Fatalf("accepted invalid %s", key)
			}
		})
	}
}
