package config

import (
	"fmt"
	"github.com/slager2/qwenchik/internal/domain"
)

func defaultResearchCaps(initial, queries int) domain.ResearchBudget {
	return domain.ResearchBudget{MaxInitialPlanSteps: initial, MaxRoundSteps: 12, MaxTotalSteps: 96, MaxRounds: 10, MaxSearchQueries: queries, MaxFetches: 36, MaxLLMCalls: 128, MaxEvidence: 32, MaxQuestionsCreated: 80, MaxQuestionsAccepted: 40, MaxCitationSources: 8, MaxCitationClaims: 32, MaxReportSections: 8, MaxReportParagraphs: 32, MaxReportBytes: 512 << 10}
}
func validateResearchCaps(b domain.ResearchBudget) error {
	for _, v := range []struct {
		name            string
		value, min, max int
	}{
		{"initial_plan_steps", b.MaxInitialPlanSteps, 1, 12}, {"round_steps", b.MaxRoundSteps, 4, 12}, {"total_rounds", b.MaxRounds, 1, 10}, {"total_steps", b.MaxTotalSteps, 1, 128},
		{"fetches", b.MaxFetches, 1, 128}, {"llm_calls", b.MaxLLMCalls, 1, 512}, {"evidence", b.MaxEvidence, 1, 32}, {"questions", b.MaxQuestionsCreated, 1, 80}, {"questions_accepted", b.MaxQuestionsAccepted, 1, 80},
		{"citation_sources", b.MaxCitationSources, 1, 8}, {"citation_claims", b.MaxCitationClaims, 1, 32}, {"report_sections", b.MaxReportSections, 1, 8}, {"report_paragraphs", b.MaxReportParagraphs, 1, 32}, {"report_bytes", b.MaxReportBytes, 4096, 1 << 20},
	} {
		if v.value < v.min || v.value > v.max {
			return fmt.Errorf("research %s must be %d..%d", v.name, v.min, v.max)
		}
	}
	return nil
}
