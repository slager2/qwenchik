package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type adaptiveSearchFixture struct{ queries []string }

type interruptAdaptiveAssessmentStore struct{ *sqlite.Store }

func (s interruptAdaptiveAssessmentStore) CommitVerification(ctx context.Context, id, verdict, summary string, missing, supplementary []string, criteria []domain.VerificationCriterionResult) error {
	if err := s.Store.CommitVerification(ctx, id, verdict, summary, missing, supplementary, criteria); err != nil {
		return err
	}
	if verdict == "PASS" {
		return ErrInterrupted
	}
	return nil
}

type adaptiveFetchFixture struct{ researchRunFetcher }

func (*adaptiveFetchFixture) ValidateURL(context.Context, string) error { return nil }

func (s *adaptiveSearchFixture) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	s.queries = append(s.queries, options.Query)
	if len(s.queries) <= 2 {
		return researchSearchStub{}.Search(ctx, options)
	}
	return []research.SearchHit{
		{Provider: "searxng", Query: options.Query, URL: fmt.Sprintf("https://round%d-a.com/protocol", len(s.queries)), Title: "New protocol source", Snippet: "Fresh source", Taint: research.TaintExternal},
		{Provider: "searxng", Query: options.Query, URL: fmt.Sprintf("https://round%d-b.org/protocol", len(s.queries)), Title: "Independent protocol source", Snippet: "Fresh source", Taint: research.TaintExternal},
	}, nil
}

func TestAdaptiveResearchResumesSecondRoundWithNewQueriesAndSources(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) {
		s.Policy.AdaptiveEnabled = true
		s.Policy.MaxContinuations = 2
		s.Budget.MaxSteps = 12
		s.Budget.MaxSearchQueries = 8
		s.Budget.MaxLLMCalls = 40
	})
	search, fetcher := &adaptiveSearchFixture{}, &adaptiveFetchFixture{}
	artifactStore, err := artifacts.New(t.TempDir(), 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: search, Fetcher: fetcher, Extractor: researchExtractorStub{}, Artifacts: artifactStore, Repository: f.store}); err != nil {
		t.Fatal(err)
	}
	base := integrationResearchModel()
	reviews, roundQueries := 0, 0
	model := &researchRunModel{respond: func(_ context.Context, req llm.ChatRequest, index int, streaming bool) (string, error) {
		if isPlannerRequest(req) {
			return base.respond(req, 0)
		}
		var input struct {
			Catalog []verificationCriterion    `json:"criterion_catalog"`
			Step    string                     `json:"step"`
			URLs    []string                   `json:"allowed_fetch_urls"`
			Claims  []citationClaim            `json:"claims_to_check"`
			Results map[string]json.RawMessage `json:"tool_results"`
		}
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
			return "", err
		}
		if len(input.Catalog) > 0 {
			reviews++
			verdicts := map[string]any{}
			for _, c := range input.Catalog {
				ids := c.EvidenceIDs
				if ids == nil {
					ids = []string{}
				}
				verdicts[c.ID] = map[string]any{"met": true, "evidence_ids": ids, "rationale": "Recorded operation supported"}
			}
			missing := []string{}
			if reviews == 1 {
				missing = []string{"Confirm deployment limitations from independent new evidence"}
			}
			raw, err := json.Marshal(map[string]any{"ready_to_answer": reviews > 1, "summary": "Checked protocol sources", "missing": missing, "supplementary_criteria": []string{}, "criterion_assessments": verdicts})
			return string(raw), err
		}
		if strings.Contains(input.Step, "targeted query") {
			roundQueries++
			return fmt.Sprintf(`{"status":"continue","summary":"Targeted independent search","tool_call":{"name":"search","arguments":{"query":"protocol deployment new round facet %d","language":null,"recency":null,"max_results":3}}}`, roundQueries), nil
		}
		if len(input.URLs) > 0 {
			return fmt.Sprintf(`{"status":"continue","summary":"Inspect new source","tool_call":{"name":"fetch_url","arguments":{"url":%q,"original_query":"protocol deployment limitations"}}}`, input.URLs[0]), nil
		}
		if len(input.Claims) > 0 {
			for i := range input.Claims {
				input.Claims[i].Kind = "supported"
				input.Claims[i].Quote = input.Claims[i].Claim
			}
			raw, err := json.Marshal(citationReview{Claims: input.Claims})
			return string(raw), err
		}
		if streaming {
			paragraphs := []string{}
			for _, raw := range input.Results {
				var e struct {
					ID      string `json:"evidence_id"`
					Excerpt string `json:"excerpt"`
				}
				if json.Unmarshal(raw, &e) == nil && e.ID != "" {
					paragraphs = append(paragraphs, e.Excerpt+" ["+e.ID+"]")
				}
			}
			raw, err := json.Marshal(map[string]string{"answer": strings.Join(paragraphs, "\n\n")})
			return string(raw), err
		}
		return "", fmt.Errorf("unexpected adaptive call %d step %q", index, input.Step)
	}}
	w := New(interruptedContinuationStore{f.store}, llm.NewGuardedClient(model), registry, Options{})
	w.runTask(ctx, f.task)
	if reviews != 1 {
		t.Fatalf("did not interrupt at round transition: reviews=%d", reviews)
	}
	f.reopen(t)
	registry = tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: search, Fetcher: fetcher, Extractor: researchExtractorStub{}, Artifacts: artifactStore, Repository: f.store}); err != nil {
		t.Fatal(err)
	}
	w = New(interruptAdaptiveAssessmentStore{f.store}, llm.NewGuardedClient(model), registry, Options{})
	w.runTask(ctx, f.task)
	task, err := f.store.GetTask(ctx, f.task.ID)
	if err != nil || task.Status != domain.StatusVerifying {
		t.Fatalf("post-assessment boundary not reached: %+v %v", task, err)
	}
	f.reopen(t)
	if count, err := f.store.RecoverInterrupted(ctx); err != nil || count != 1 {
		t.Fatalf("assessment recovery: count=%d err=%v", count, err)
	}
	f.claim(t)
	registry = tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: search, Fetcher: fetcher, Extractor: researchExtractorStub{}, Artifacts: artifactStore, Repository: f.store}); err != nil {
		t.Fatal(err)
	}
	w = New(f.store, llm.NewGuardedClient(model), registry, Options{})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	state, err := w.currentCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Adaptive == nil || len(state.Adaptive.Rounds) != 2 || state.Adaptive.StopReason != "criteria_covered" {
		t.Fatalf("missing round history: %+v", state.Adaptive)
	}
	if len(search.queries) != 4 || fetcher.calls.Load() != 4 || roundQueries != 2 {
		t.Fatalf("round calls: queries=%v fetches=%d", search.queries, fetcher.calls.Load())
	}
	for i, query := range search.queries {
		if duplicateResearchQuery(query, search.queries[:i]) {
			t.Fatal("adaptive query repeated")
		}
	}
	session := researchRunSession(t, f.store, f.task.ID)
	if session.CurrentRound != 1 || session.Counters.Rounds != 2 || session.Counters.QuestionsCreated != 1 || session.Counters.QuestionsAccepted != 1 {
		t.Fatalf("round accounting: %+v", session)
	}
	if len(state.Adaptive.Rounds[1].AcceptedEvidence) != 4 || len(state.Adaptive.Frontier) != 0 {
		t.Fatalf("frontier/evidence not advanced: %+v", state.Adaptive)
	}
	if len(state.SourceNotes) != 4 {
		t.Fatalf("source notes not persisted: %+v", state.SourceNotes)
	}
	for _, note := range state.SourceNotes {
		if note.ContentHash == "" || note.ExtractedTextHash == "" || note.Taint != "untrusted_external_content" {
			t.Fatalf("source note lost provenance: %+v", note)
		}
	}
	f.reopen(t)
	restored, err := wWithStore(w, f).currentCheckpoint(ctx, f.task.ID)
	if err != nil || len(restored.SourceNotes) != 4 {
		t.Fatalf("notes lost after reopen: count=%d err=%v", len(restored.SourceNotes), err)
	}
}

func TestAdaptiveBudgetStopDoesNotPerformUnreservedPartialInference(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) {
		s.Policy.AdaptiveEnabled = true
		s.Policy.MaxContinuations = 2
		s.Budget.MaxSteps = 12
		// Planner + verifier: canonical search/fetch steps are deterministic,
		// so the first round consumes exactly this budget.
		s.Budget.MaxLLMCalls = 2
		s.Budget.MaxSearchQueries = 8
	})
	base := integrationResearchModel()
	calls := 0
	model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, _ bool) (string, error) {
		calls++
		if isPlannerRequest(request) {
			return base.respond(request, 0)
		}
		if !isVerifierRequest(request) {
			t.Fatalf("exhausted session performed unreserved inference: call %d schema=%.200s", index, request.JSONSchema)
		}
		var input struct {
			Catalog []verificationCriterion `json:"criterion_catalog"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			return "", err
		}
		assessments := map[string]any{}
		for _, criterion := range input.Catalog {
			ids := criterion.EvidenceIDs
			if ids == nil {
				ids = []string{}
			}
			assessments[criterion.ID] = map[string]any{"met": true, "evidence_ids": ids, "rationale": "Recorded operation"}
		}
		raw, err := json.Marshal(map[string]any{"ready_to_answer": false, "summary": "Critical facts remain unavailable", "missing": []string{"Confirm the remaining deployment constraints"}, "supplementary_criteria": []string{}, "criterion_assessments": assessments})
		return string(raw), err
	}}
	w := New(f.store, llm.NewGuardedClient(model), researchRunRegistry(t, f.store, &researchRunSearch{}, &researchRunFetcher{}), Options{})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	session := researchRunSession(t, f.store, f.task.ID)
	if session.Counters.LLMCalls != 2 || calls != 2 {
		t.Fatalf("budget counter/provider calls diverged: counters=%+v calls=%d", session.Counters, calls)
	}
	state, err := w.currentCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Adaptive == nil || state.Adaptive.StopReason != "research_budget" {
		t.Fatalf("adaptive budget stop not durable: %+v", state.Adaptive)
	}
	messages, err := f.store.ConversationMessages(ctx, f.turn.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || !strings.Contains(messages[1].Content, "Дословные выдержки") || !strings.Contains(messages[1].Content, "Что не удалось подтвердить") {
		t.Fatalf("budget stop lost honest partial: %+v", messages)
	}
}
