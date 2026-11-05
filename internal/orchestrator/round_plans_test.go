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

func roundModelFixture(t *testing.T, rounds int, reviews *int) *researchRunModel {
	t.Helper()
	base := integrationResearchModel()
	queries := 0
	return &researchRunModel{respond: func(_ context.Context, req llm.ChatRequest, index int, stream bool) (string, error) {
		if isPlannerRequest(req) {
			raw, err := base.respond(req, 0)
			if err != nil {
				return "", err
			}
			var p domain.Plan
			_ = json.Unmarshal([]byte(raw), &p)
			p.DeliverableCriteria = []string{"Explain protocol architecture and deployment limitations from relevant evidence"}
			b, err := json.Marshal(p)
			return string(b), err
		}
		var input struct {
			Catalog  []verificationCriterion `json:"criterion_catalog"`
			Step     string                  `json:"step"`
			URLs     []string                `json:"allowed_fetch_urls"`
			Notes    []domain.SourceNote     `json:"source_notes"`
			IDs      []string                `json:"criterion_ids"`
			Claims   []citationClaim         `json:"claims_to_check"`
			Criteria []string                `json:"criteria"`
			Evidence []citationEvidence      `json:"evidence_catalogue"`
		}
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
			return "", err
		}
		if len(input.Catalog) > 0 && input.Catalog[0].Kind != "" {
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
				met := c.Kind != "deliverable" || *reviews >= rounds
				verdicts[c.ID] = map[string]any{"met": met, "evidence_ids": ids, "rationale": "Fixture explicitly distinguishes execution from substantive readiness"}
			}
			missing := []string{}
			if *reviews < rounds {
				missing = []string{"Substantive deployment limitations remain unresolved"}
			}
			b, err := json.Marshal(map[string]any{"ready_to_answer": *reviews >= rounds, "summary": "Readiness assessed", "missing": missing, "supplementary_criteria": []string{}, "criterion_assessments": verdicts})
			return string(b), err
		}
		if strings.Contains(input.Step, "targeted query") {
			queries++
			return fmt.Sprintf(`{"status":"continue","summary":"Independent targeted search","tool_call":{"name":"search","arguments":{"query":"protocol deployment independent facet round %d","language":null,"recency":null,"max_results":3}}}`, queries), nil
		}
		if len(input.URLs) > 0 {
			return fmt.Sprintf(`{"status":"continue","summary":"Inspect new evidence","tool_call":{"name":"fetch_url","arguments":{"url":%q,"original_query":"protocol deployment"}}}`, input.URLs[0]), nil
		}
		if strings.Contains(req.Messages[0].Content, "Create a small research report outline") {
			b, err := json.Marshal(map[string]any{"title": "Protocol review", "sections": []map[string]any{{"title": "Architecture and limitations", "evidence_ids": []string{input.Notes[0].EvidenceID}, "criteria": input.IDs}}})
			return string(b), err
		}
		if len(input.Claims) > 0 {
			for i := range input.Claims {
				input.Claims[i].Kind = "supported"
				input.Claims[i].Quote = input.Claims[i].Claim
			}
			b, err := json.Marshal(reportCitationReview{Claims: input.Claims, CriteriaCovered: input.Criteria})
			return string(b), err
		}
		if len(input.Evidence) > 0 {
			b, err := json.Marshal(map[string]string{"answer": input.Evidence[0].Excerpt + " [" + input.Evidence[0].EvidenceID + "]"})
			return string(b), err
		}
		return "", fmt.Errorf("unexpected round call %d step=%q", index, input.Step)
	}}
}

func TestBoundedRoundPlansRunOneTwoThreeFiveAndTenRounds(t *testing.T) {
	for _, rounds := range []int{1, 2, 3, 5, 10} {
		t.Run(fmt.Sprint(rounds), func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			submitted := f.submit(t, "Research protocol architecture and deployment limitations.", func(s *domain.ResearchSession) {
				s.Policy.Version = 2
				s.Policy.AdaptiveEnabled = true
				s.Policy.SectionedReportEnabled = true
				s.Policy.MaxContinuations = rounds - 1
				s.Budget, _ = domain.RoundResearchBudgetForProfile(domain.ResearchProfileOvernight)
				s.Budget.MaxRounds = rounds
				s.Budget.MaxRoundSteps = 4
				s.Budget.MaxTotalSteps = rounds * 4
				s.Budget.MaxSearchQueries = rounds * 2
				s.Budget.MaxFetches = rounds * 2
				s.Budget.MaxEvidence = rounds * 2
				s.Budget.MaxStagnantRounds = min(2, rounds)
			})
			files, err := artifacts.New(t.TempDir(), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			search, fetch := &adaptiveSearchFixture{}, &adaptiveFetchFixture{}
			makeRegistry := func() *tools.Registry {
				r := tools.NewRegistry()
				if err := tools.RegisterResearchTools(r, tools.ResearchTools{SearchProvider: search, Fetcher: fetch, Extractor: researchExtractorStub{}, Artifacts: files, Repository: f.store}); err != nil {
					t.Fatal(err)
				}
				return r
			}
			reviews := 0
			modelFixture := roundModelFixture(t, rounds, &reviews)
			model := llm.NewGuardedClient(modelFixture)
			w := New(f.store, model, makeRegistry(), Options{ReportArtifacts: files})
			if rounds == 2 {
				w = New(interruptedContinuationStore{f.store}, model, makeRegistry(), Options{ReportArtifacts: files})
				w.runTask(ctx, f.task)
				if reviews != 1 {
					t.Fatalf("transition not reached: %d", reviews)
				}
				f.reopen(t)
				if _, err := f.store.RecoverInterrupted(ctx); err != nil {
					t.Fatal(err)
				}
				f.claim(t)
				w = New(f.store, model, makeRegistry(), Options{ReportArtifacts: files})
			}
			w.runTask(ctx, f.task)
			assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
			s := researchRunSession(t, f.store, f.task.ID)
			if !s.DeadlineAt.Equal(submitted.DeadlineAt) || s.Counters.LLMCalls != len(modelFixture.calls()) {
				t.Fatal("restart refunded calls or changed the absolute deadline")
			}
			state, err := w.currentCheckpoint(ctx, f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if reviews != rounds || s.Counters.Rounds != rounds || s.Counters.Steps != rounds*4 || len(state.ResearchPlans.Rounds) != rounds || len(state.Adaptive.Rounds) != rounds {
				t.Fatalf("round accounting: reviews=%d session=%+v", reviews, s)
			}
			task, err := f.store.GetTask(ctx, f.task.ID)
			if err != nil || len(task.Plan.Subtasks) != 4 || len(state.ResearchPlans.InitialPlan.Subtasks) != 4 {
				t.Fatal("initial plan was expanded", err)
			}
			steps, err := f.store.Steps(ctx, f.task.ID)
			if err != nil || len(steps) != rounds*4 {
				t.Fatalf("history lost: steps=%d err=%v", len(steps), err)
			}
			if len(search.queries) != 2*rounds || fetch.calls.Load() != int32(2*rounds) || s.Counters.Evidence != 2*rounds {
				t.Fatalf("provider operations diverged: search=%d fetch=%d evidence=%d", len(search.queries), fetch.calls.Load(), s.Counters.Evidence)
			}
			if state.Report == nil || state.Report.ArtifactID == "" || len(task.Plan.DeliverableCriteria) != 2 || len(state.Report.ExecutionCriteria) != 4 {
				t.Fatalf("deliverable/operational criteria or report lost: %+v", state.Report)
			}
		})
	}
}

func TestRoundBudgetExhaustionPreservesUsefulPartialWithoutUnreservedInference(t *testing.T) {
	for _, reason := range []string{"total_steps", "search_budget", "fetch_budget", "evidence_budget", "llm_budget"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			f.submit(t, "Research protocol architecture and deployment limitations.", func(s *domain.ResearchSession) {
				s.Policy.Version = 2
				s.Policy.AdaptiveEnabled = true
				s.Policy.SectionedReportEnabled = true
				s.Policy.MaxContinuations = 2
				s.Budget, _ = domain.RoundResearchBudgetForProfile(domain.ResearchProfileStandard)
				switch reason {
				case "total_steps":
					s.Budget.MaxTotalSteps = 4
				case "search_budget":
					s.Budget.MaxSearchQueries = 2
				case "fetch_budget":
					s.Budget.MaxFetches = 2
				case "evidence_budget":
					s.Budget.MaxEvidence = 2
				case "llm_budget":
					s.Budget.MaxLLMCalls = 6
				}
			})
			files, err := artifacts.New(t.TempDir(), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			r := tools.NewRegistry()
			search, fetch := &adaptiveSearchFixture{}, &adaptiveFetchFixture{}
			if err := tools.RegisterResearchTools(r, tools.ResearchTools{SearchProvider: search, Fetcher: fetch, Extractor: researchExtractorStub{}, Artifacts: files, Repository: f.store}); err != nil {
				t.Fatal(err)
			}
			reviews := 0
			model := roundModelFixture(t, 3, &reviews)
			w := New(f.store, llm.NewGuardedClient(model), r, Options{ReportArtifacts: files})
			w.runTask(ctx, f.task)
			assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
			state, err := w.currentCheckpoint(ctx, f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			s := researchRunSession(t, f.store, f.task.ID)
			if state.Adaptive.StopReason != reason || state.Report == nil || state.Report.Outcome != "partial" || s.CurrentRound != 0 || s.Counters.Steps != 4 {
				t.Fatalf("wrong bounded stop: state=%+v session=%+v", state.Adaptive, s)
			}
			messages, err := f.store.ConversationMessages(ctx, f.turn.ConversationID)
			if err != nil || len(messages[1].Content) == 0 {
				t.Fatal("partial result lost", err)
			}
			if s.Counters.LLMCalls != len(model.calls()) || len(search.queries) != 2 || fetch.calls.Load() != 2 {
				t.Fatal("budget stop dispatched extra calls")
			}
		})
	}
}
