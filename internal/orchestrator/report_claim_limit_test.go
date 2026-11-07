package orchestrator

import (
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestLongReportsGiveEverySectionEightClaims(t *testing.T) {
	for profile, want := range map[string]int{domain.ResearchProfileStandard: 6, domain.ResearchProfileDeepLaptop: 6, domain.ResearchProfileLongLaptop: 8, domain.ResearchProfileOvernight: 8} {
		b, _ := domain.RoundResearchBudgetForProfile(profile)
		sections := b.MaxReportSections
		if profile == domain.ResearchProfileStandard {
			sections = 4 // standard reports are capped at four sections
		}
		if got := reportClaimLimit(b.MaxCitationClaims, min(b.MaxReportParagraphs, b.MaxCitationClaims), sections); got != want {
			t.Fatalf("%s: %d claims per section with %d sections, want %d", profile, got, sections, want)
		}
	}
	if got := reportClaimLimit(64, 64, 2); got != 8 {
		t.Fatalf("per-section cap exceeded: %d", got)
	}
}
