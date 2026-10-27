package domain

import (
	"testing"
	"time"
)

func TestNominalRoundShapesCanReachProfileRoundsAndQueryBudgets(t *testing.T) {
	for _, name := range []string{ResearchProfileStandard, ResearchProfileDeepLaptop, ResearchProfileLongLaptop, ResearchProfileOvernight} {
		t.Run(name, func(t *testing.T) {
			b, _ := RoundResearchBudgetForProfile(name)
			s := ResearchSession{Policy: ResearchPolicy{Version: 2, AdaptiveEnabled: true, TaskFetchBudgetBytes: 128 << 20}, Budget: b, DeadlineAt: time.Now().Add(time.Hour), Counters: ResearchCounters{Steps: 4, SearchQueries: 2, Fetches: 2, Evidence: 2, LLMCalls: 6}}
			for round := 1; round < b.MaxRounds; round++ {
				s.CurrentRound = round - 1
				steps, q, f, reserve := NextResearchRoundShape(s)
				if steps > b.MaxRoundSteps || q < 2 || f < 2 {
					t.Fatalf("invalid shape %d/%d/%d", steps, q, f)
				}
				if reason := ContinuationStopReason(s, steps, q, f, reserve, time.Now()); reason != "" {
					t.Fatalf("profile %s round %d nominally unreachable: %s %+v", name, round+1, reason, s.Counters)
				}
				s.Counters.Steps += steps
				s.Counters.SearchQueries += q
				s.Counters.Fetches += f
				s.Counters.Evidence += f
				s.Counters.LLMCalls += steps + 1
			}
			if s.Counters.SearchQueries != b.MaxSearchQueries || s.Counters.Steps > b.MaxTotalSteps || s.Counters.Evidence > b.MaxEvidence {
				t.Fatalf("nominal budget clamped by hidden shape: %+v budget=%+v", s.Counters, b)
			}
		})
	}
}
