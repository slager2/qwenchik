package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func freshnessEvidence(id, rawURL, title, excerpt string) domain.Evidence {
	return domain.Evidence{ID: id, CanonicalURL: rawURL, Title: title, Excerpt: excerpt, HTTPStatus: 200}
}

func TestFreshnessCoverageGapsFlagStaleDatesAndMissingOfficialSources(t *testing.T) {
	goal := "Сравни Stipendium Hungaricum и GKS: дедлайны 2026 и требования"
	evidence := []domain.Evidence{
		freshnessEvidence("e1", "https://stipendiumhungaricum.hu/apply/", "Application", "The deadline for the 2023/2024 call was 15 January 2024."),
		freshnessEvidence("e2", "https://blog.example.com/gks", "GKS guide", "GKS 2024 deadlines summary."),
	}
	gaps, queries := freshnessCoverageGaps(goal, evidence, nil, searchParamsNow)
	if len(gaps) != 2 || !strings.Contains(gaps[0], "no year after 2024") || !strings.Contains(gaps[1], "GKS") {
		t.Fatalf("gaps = %q", gaps)
	}
	if strings.Join(queries, "|") != "Stipendium Hungaricum 2026 official|GKS 2026 official" {
		t.Fatalf("queries = %q", queries)
	}
	evidence = append(evidence,
		freshnessEvidence("e3", "https://www.studyinkorea.go.kr/en/gks.do", "GKS 2026 notice", "GKS 2026 graduate program: applications until 1 March 2026."),
	)
	if gaps, queries := freshnessCoverageGaps(goal, evidence, nil, searchParamsNow); len(gaps) != 0 || len(queries) != 0 {
		t.Fatalf("fresh official evidence still flagged: %q %q", gaps, queries)
	}
	// Only cited evidence counts for freshness.
	if gaps, _ := freshnessCoverageGaps(goal, evidence, map[string]bool{"e1": true}, searchParamsNow); len(gaps) != 1 {
		t.Fatalf("cited-only freshness gaps = %q", gaps)
	}
	if gaps, _ := freshnessCoverageGaps("Explain how the GKS scholarship works", evidence[:2], nil, searchParamsNow); len(gaps) != 0 {
		t.Fatalf("not date-sensitive request flagged: %q", gaps)
	}
}

func TestFreshnessRuleAppliesOnlyToExploringProfilesWithRoundsLeft(t *testing.T) {
	session := func(profile string) context.Context {
		s := domain.ResearchSession{Profile: profile}
		s.Policy.Profile, s.Policy.Version, s.Policy.AdaptiveEnabled = profile, 2, true
		s.Budget, _ = domain.RoundResearchBudgetForProfile(profile)
		return context.WithValue(context.Background(), researchExecutionKey{}, s)
	}
	for _, profile := range []string{domain.ResearchProfileQuick, domain.ResearchProfileStandard} {
		if _, ok := freshnessRuleApplies(session(profile), checkpointState{}); ok {
			t.Fatalf("%s: rule applies", profile)
		}
	}
	deep := session(domain.ResearchProfileDeepLaptop)
	if hold, ok := freshnessRuleApplies(deep, checkpointState{}); !ok || hold {
		t.Fatalf("deep round 1: hold=%v ok=%v (exploring rounds only steer follow-ups)", hold, ok)
	}
	state := checkpointState{}
	state.Completion.Continuations = 1
	if hold, ok := freshnessRuleApplies(deep, state); !ok || !hold {
		t.Fatalf("deep round 2: hold=%v ok=%v", hold, ok)
	}
	state.Completion.Continuations = maxFreshnessContinuations
	if _, ok := freshnessRuleApplies(session(domain.ResearchProfileOvernight), state); ok {
		t.Fatal("rule exceeded its continuation bound")
	}
}
