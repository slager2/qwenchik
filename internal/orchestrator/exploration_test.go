package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
)

// explorationModelFixture is a verifier that finds the criteria covered from
// round one and proposes entity-level follow-ups (one university per round).
func explorationModelFixture(t *testing.T, reviews *int, followUps bool) *researchRunModel {
	t.Helper()
	base := roundModelFixture(t, 1, new(int))
	return &researchRunModel{respond: func(ctx context.Context, req llm.ChatRequest, index int, stream bool) (string, error) {
		var input struct {
			Catalog []verificationCriterion `json:"criterion_catalog"`
		}
		if len(req.Messages) > 1 && json.Unmarshal([]byte(req.Messages[1].Content), &input) == nil && len(input.Catalog) > 0 && input.Catalog[0].Kind != "" {
			*reviews++
			verdicts := map[string]any{}
			for _, c := range input.Catalog {
				ids := c.EvidenceIDs
				if len(ids) > 1 {
					ids = ids[:1]
				}
				if ids == nil {
					ids = []string{}
				}
				verdicts[c.ID] = map[string]any{"met": true, "evidence_ids": ids, "rationale": "Covered by fetched evidence"}
			}
			response := map[string]any{"ready_to_answer": true, "summary": "Criteria covered", "missing": []string{}, "supplementary_criteria": []string{}, "criterion_assessments": verdicts}
			asked := strings.Contains(string(req.JSONSchema), "follow_up_queries")
			if asked != strings.Contains(req.Messages[0].Content, "follow_up_queries") {
				return "", fmt.Errorf("follow-up schema and prompt disagree")
			}
			if followUps && asked { // the last allowed round is not asked
				response["follow_up_queries"] = []string{fmt.Sprintf("University %d informatics master scholarship requirements", *reviews), fmt.Sprintf("University %d application fee deadline", *reviews)}
			}
			b, err := json.Marshal(response)
			return string(b), err
		}
		return base.respond(ctx, req, index, stream)
	}}
}

func runExplorationFixture(t *testing.T, profile string, followUps bool, edit func(*domain.ResearchSession)) (*researchRunFixture, *Worker, *adaptiveSearchFixture, int) {
	t.Helper()
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research protocol architecture and deployment limitations.", func(s *domain.ResearchSession) {
		s.Profile = profile
		s.Policy.Profile = profile
		s.Policy.Version = 2
		s.Policy.AdaptiveEnabled = true
		s.Policy.SectionedReportEnabled = true
		s.Budget, _ = domain.RoundResearchBudgetForProfile(profile)
		s.Budget.WallTimeSeconds = 6 * 3600
		s.Policy.MaxContinuations = s.Budget.MaxRounds - 1
		if edit != nil {
			edit(s)
		}
		s.Policy.Budget = s.Budget
	})
	files, err := artifacts.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	search, fetch := &adaptiveSearchFixture{}, &adaptiveFetchFixture{}
	r := tools.NewRegistry()
	if err := tools.RegisterResearchTools(r, tools.ResearchTools{SearchProvider: search, Fetcher: fetch, Extractor: researchExtractorStub{}, Artifacts: files, Repository: f.store}); err != nil {
		t.Fatal(err)
	}
	reviews := 0
	w := New(f.store, llm.NewGuardedClient(explorationModelFixture(t, &reviews, followUps)), r, Options{ReportArtifacts: files})
	w.runTask(ctx, f.task)
	return f, w, search, reviews
}

func TestOvernightRunKeepsExploringFollowUpsAfterCriteriaAreCovered(t *testing.T) {
	ctx := context.Background()
	f, w, search, reviews := runExplorationFixture(t, domain.ResearchProfileOvernight, true, nil)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	s := researchRunSession(t, f.store, f.task.ID)
	state, err := w.currentCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviews < 3 || s.Counters.Rounds < 3 || len(state.Adaptive.Rounds) < 3 {
		t.Fatalf("overnight run stopped early: reviews=%d counters=%+v", reviews, s.Counters)
	}
	if s.Counters.QuestionsCreated == 0 || s.Counters.QuestionsCreated != len(state.Adaptive.Questions) {
		t.Fatalf("no follow-up questions recorded: %+v", s.Counters)
	}
	for _, query := range []string{"University 1 informatics master scholarship requirements", "University 2 application fee deadline"} {
		found := false
		for _, executed := range search.queries {
			found = found || executed == query
		}
		if !found {
			t.Fatalf("follow-up %q was not searched: %q", query, search.queries)
		}
	}
	for _, round := range state.Adaptive.Rounds {
		if round.Outcome != "PASS" {
			t.Fatalf("exploration round blocked the covered answer: %+v", round)
		}
	}
	if state.Adaptive.StopReason == "" || state.Report == nil || state.Report.ArtifactID == "" || state.Partial {
		t.Fatalf("exploration did not finish with a complete report: stop=%q report=%+v partial=%v", state.Adaptive.StopReason, state.Report, state.Partial)
	}
	t.Logf("rounds=%d stop=%s counters=%+v", len(state.Adaptive.Rounds), state.Adaptive.StopReason, s.Counters)
	b := s.Budget
	if s.Counters.SearchQueries > b.MaxSearchQueries || s.Counters.Fetches > b.MaxFetches || s.Counters.Evidence > b.MaxEvidence || s.Counters.LLMCalls > b.MaxLLMCalls || s.Counters.Steps > b.MaxTotalSteps || s.Counters.QuestionsAccepted > b.MaxQuestionsAccepted {
		t.Fatalf("exploration exceeded a hard budget: %+v %+v", s.Counters, b)
	}
}

func TestOvernightExplorationFallsBackToDeliverableFollowUps(t *testing.T) {
	f, _, _, reviews := runExplorationFixture(t, domain.ResearchProfileOvernight, false, func(s *domain.ResearchSession) { s.Budget.MaxRounds, s.Budget.MinRounds = 3, 3 })
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	s := researchRunSession(t, f.store, f.task.ID)
	if reviews < 2 || s.Counters.Rounds < 2 || s.Counters.QuestionsCreated == 0 {
		t.Fatalf("silent verifier stopped exploration: reviews=%d %+v", reviews, s.Counters)
	}
}

func TestStandardRunStillCompletesAfterCoveredRound(t *testing.T) {
	f, _, _, reviews := runExplorationFixture(t, domain.ResearchProfileStandard, false, nil)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	s := researchRunSession(t, f.store, f.task.ID)
	if reviews != 1 || s.Counters.Rounds != 1 || s.Counters.QuestionsCreated != 0 {
		t.Fatalf("standard profile explored: reviews=%d %+v", reviews, s.Counters)
	}
}
