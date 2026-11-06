package domain

import (
	"testing"
	"time"
)

func TestExplorationRulesPerProfile(t *testing.T) {
	now := time.Now()
	session := func(name string) ResearchSession {
		b, _ := RoundResearchBudgetForProfile(name)
		return ResearchSession{Profile: name, Policy: ResearchPolicy{Version: 2, AdaptiveEnabled: true, TaskFetchBudgetBytes: 128 << 20}, Budget: b,
			DeadlineAt: now.Add(time.Duration(b.WallTimeSeconds) * time.Second), Counters: ResearchCounters{Steps: 12, SearchQueries: 5, Fetches: 9, Evidence: 9, LLMCalls: 10}}
	}
	for name, want := range map[string]string{ResearchProfileQuick: "criteria_covered", ResearchProfileStandard: "criteria_covered", ResearchProfileDeepLaptop: "", ResearchProfileLongLaptop: "", ResearchProfileOvernight: ""} {
		s := session(name)
		if name == ResearchProfileDeepLaptop {
			s.Counters = ResearchCounters{Steps: 12, SearchQueries: 4, Fetches: 8, Evidence: 8, LLMCalls: 10}
		}
		if got := ExplorationStopReason(s, now); got != want {
			t.Fatalf("%s round 1: got %q want %q", name, got, want)
		}
	}
	deep := session(ResearchProfileDeepLaptop)
	deep.Counters = ResearchCounters{Steps: 12, SearchQueries: 4, Fetches: 8, Evidence: 8, LLMCalls: 10}
	deep.CurrentRound = 1
	if got := ExplorationStopReason(deep, now); got != "criteria_covered" {
		t.Fatalf("deep explores beyond its minimum rounds: %q", got)
	}
	overnight := session(ResearchProfileOvernight)
	if got := ExplorationStopReason(overnight, now.Add(5*time.Hour)); got != "deadline" {
		t.Fatalf("exploration ignores the report time reserve: %q", got)
	}
	overnight.Counters.LLMCalls = overnight.Budget.MaxLLMCalls - ReportLLMReserve(overnight) - 5
	if got := ExplorationStopReason(overnight, now); got != "llm_budget" {
		t.Fatalf("exploration ignores the report model-call reserve: %q", got)
	}
	overnight = session(ResearchProfileOvernight)
	overnight.CurrentRound = overnight.Budget.MaxRounds - 1
	if got := ExplorationStopReason(overnight, now); got != "criteria_covered" {
		t.Fatalf("exploration passes max rounds: %q", got)
	}
	overnight = session(ResearchProfileOvernight)
	overnight.Counters.Evidence = overnight.Budget.MaxEvidence - 1
	if got := ExplorationStopReason(overnight, now); got != "evidence_budget" {
		t.Fatalf("exploration ignores the evidence ceiling: %q", got)
	}
	overnight.Policy.AdaptiveEnabled = false
	if Exploring(overnight, 1) {
		t.Fatal("non-adaptive session explores")
	}
}
