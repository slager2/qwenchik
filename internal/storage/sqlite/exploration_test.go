package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func explorationFixture(t *testing.T, edit func(*domain.ResearchSession)) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, func(r *domain.ResearchSession) {
		r.Policy.Version = 2
		r.Policy.AdaptiveEnabled = true
		r.Policy.MaxContinuations = 9
		r.Budget, _ = domain.RoundResearchBudgetForProfile(domain.ResearchProfileOvernight)
		r.Budget.MaxRoundSteps = 4
		if edit != nil {
			edit(r)
		}
	})
	claimResearchFixture(t, s)
	p := domain.Plan{SchemaVersion: 1, Goal: "Research funded IT programs", FinalDeliverable: "Substantive report", DeliverableCriteria: []string{"List programs"}, MaxSteps: 1, Subtasks: []domain.PlanStep{{ID: "initial", Kind: "search", Description: "Discovery", SuccessCriteria: []string{"recorded"}}}}
	if _, err := s.SavePlan(ctx, turn.Run.ID, p, map[string]any{"completed_steps": []string{}, "tool_results": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepStatus(ctx, turn.Run.ID, "initial", "completed"); err != nil {
		t.Fatal(err)
	}
	return s, turn.Run.ID
}

func adaptiveCheckpoint(t *testing.T, s *Store, id string) (domain.AdaptiveResearchState, domain.ResearchPlans) {
	t.Helper()
	checkpoint, err := s.LatestCheckpoint(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(checkpoint.State)
	var state struct {
		Adaptive domain.AdaptiveResearchState `json:"adaptive_research"`
		Plans    domain.ResearchPlans         `json:"research_plans"`
	}
	_ = json.Unmarshal(raw, &state)
	return state.Adaptive, state.Plans
}

func TestCoveredOvernightRoundExploresVerifierFollowUps(t *testing.T) {
	ctx := context.Background()
	s, id := explorationFixture(t, func(r *domain.ResearchSession) { r.Budget.MaxStagnantRounds = 10 }) // fixture rounds store no evidence
	followUps := []string{"TU Munich informatics master DAAD scholarship requirements", "KAIST computer science scholarship IELTS minimum", "TU Munich informatics master DAAD scholarship requirements"}
	if err := s.CommitVerificationWithFollowUps(ctx, id, "PASS", "Criteria covered", nil, nil, nil, followUps); err != nil {
		t.Fatal(err)
	}
	adaptive, _ := adaptiveCheckpoint(t, s, id)
	if adaptive.StopReason != "" || len(adaptive.Frontier) != 2 || len(adaptive.Questions) != 2 {
		t.Fatalf("covered overnight round did not open follow-ups: %+v", adaptive)
	}
	session, _ := s.GetResearchSession(ctx, id)
	if session.Counters.QuestionsCreated != 2 || session.Counters.QuestionsAccepted != 2 {
		t.Fatalf("questions not counted: %+v", session.Counters)
	}
	// A replay derives follow-ups against the advanced frontier: still idempotent.
	if err := s.CommitVerificationWithFollowUps(ctx, id, "PASS", "Criteria covered", nil, nil, nil, followUps[:1]); err != nil {
		t.Fatal("idempotent replay failed:", err)
	}
	if replayed, _ := adaptiveCheckpoint(t, s, id); len(replayed.Questions) != 2 {
		t.Fatalf("replay changed the frontier: %+v", replayed.Questions)
	}
	if err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), followUps[:2]); err != nil {
		t.Fatal(err)
	}
	_, plans := adaptiveCheckpoint(t, s, id)
	p, err := plans.Current()
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{}
	for _, step := range p.Subtasks {
		if step.Kind == "search" {
			queries = append(queries, step.Query)
			if step.Query != "" && !strings.HasPrefix(step.Description, domain.ResearchFacetPrefix) {
				t.Fatalf("follow-up search is not a validated facet: %+v", step)
			}
		}
	}
	if len(queries) < 2 || queries[0] != followUps[0] || queries[1] != followUps[1] {
		t.Fatalf("follow-ups not turned into searches: %q", queries)
	}
	// The next covered round answers those follow-ups and may open new ones.
	if err := s.CommitVerificationWithFollowUps(ctx, id, "PASS", "Criteria covered again", nil, nil, nil, []string{"KAIST application fee 2025"}); err != nil {
		t.Fatal(err)
	}
	adaptive, _ = adaptiveCheckpoint(t, s, id)
	if adaptive.StopReason != "" || len(adaptive.Frontier) != 1 || len(adaptive.Questions) != 3 || adaptive.Questions[0].Status != "resolved" {
		t.Fatalf("explored follow-ups not resolved or new ones not opened: %+v", adaptive)
	}
}

func TestCoveredRoundStopsWhenProfileDoesNotExplore(t *testing.T) {
	ctx := context.Background()
	s, id := explorationFixture(t, func(r *domain.ResearchSession) {
		b, _ := domain.RoundResearchBudgetForProfile(domain.ResearchProfileStandard)
		r.Profile, r.Policy.Profile, r.Budget = domain.ResearchProfileStandard, domain.ResearchProfileStandard, b
		r.Policy.MaxContinuations = 2
	})
	if err := s.CommitVerificationWithFollowUps(ctx, id, "PASS", "Criteria covered", nil, nil, nil, []string{"extra query"}); err != nil {
		t.Fatal(err)
	}
	adaptive, _ := adaptiveCheckpoint(t, s, id)
	if adaptive.StopReason != "criteria_covered" || len(adaptive.Frontier) != 0 || len(adaptive.Questions) != 0 {
		t.Fatalf("standard profile explored: %+v", adaptive)
	}
}

func TestCoveredExplorationStopsWithoutFollowUpsOrOnStagnation(t *testing.T) {
	ctx := context.Background()
	s, id := explorationFixture(t, nil)
	if err := s.CommitVerification(ctx, id, "PASS", "Criteria covered", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	adaptive, _ := adaptiveCheckpoint(t, s, id)
	if adaptive.StopReason != "criteria_covered" {
		t.Fatalf("empty follow-up frontier kept exploring: %+v", adaptive)
	}

	s, id = explorationFixture(t, func(r *domain.ResearchSession) { r.Budget.MaxStagnantRounds = 1 })
	if err := s.CommitVerificationWithFollowUps(ctx, id, "PASS", "Covered", nil, nil, nil, []string{"first follow-up"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestResearchContinuation(ctx, id, 0, strings.Repeat("a", 64), strings.Repeat("b", 64), []string{"first follow-up"}); err != nil {
		t.Fatal(err)
	}
	// No new evidence in the exploration round: real stagnation stops it.
	if err := s.CommitVerificationWithFollowUps(ctx, id, "PASS", "Covered", nil, nil, nil, []string{"second follow-up"}); err != nil {
		t.Fatal(err)
	}
	adaptive, _ = adaptiveCheckpoint(t, s, id)
	if adaptive.StopReason != "stagnation" || len(adaptive.Frontier) != 0 {
		t.Fatalf("stagnant exploration continued: %+v", adaptive)
	}
}
