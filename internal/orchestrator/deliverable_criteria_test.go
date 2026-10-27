package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
	"strings"
	"testing"
)

func TestAcquisitionCannotEraseUnmetUserDeliverableCriteria(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research a protocol learning roadmap with stages and supported time estimates.", func(s *domain.ResearchSession) {
		s.Policy.Version = 2
		s.Policy.AdaptiveEnabled = true
		s.Policy.SectionedReportEnabled = true
		s.Policy.MaxContinuations = 0
		s.Budget, _ = domain.RoundResearchBudgetForProfile(domain.ResearchProfileQuick)
	})
	files, err := artifacts.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	reviews := 0
	base := roundModelFixture(t, 1, &reviews)
	model := &researchRunModel{respond: func(ctx context.Context, req llm.ChatRequest, index int, stream bool) (string, error) {
		if index == 0 {
			raw, err := base.respond(ctx, req, index, stream)
			if err != nil {
				return "", err
			}
			var p domain.Plan
			_ = json.Unmarshal([]byte(raw), &p)
			p.DeliverableCriteria = []string{"Provide learning stages", "Provide supported time estimates"}
			rawJSON, err := json.Marshal(p)
			return string(rawJSON), err
		}
		if strings.Contains(req.Messages[0].Content, "You assess evidence readiness") {
			var in struct {
				Catalog []verificationCriterion `json:"criterion_catalog"`
			}
			_ = json.Unmarshal([]byte(req.Messages[1].Content), &in)
			verdicts := map[string]any{}
			supp := []string{}
			for _, c := range in.Catalog {
				ids := c.EvidenceIDs
				if len(ids) > 1 {
					ids = ids[:1]
				}
				if ids == nil {
					ids = []string{}
				}
				met := c.Kind != "deliverable"
				if !met {
					supp = append(supp, c.ID)
				}
				verdicts[c.ID] = map[string]any{"met": met, "evidence_ids": ids, "rationale": "Sources only establish acquisition, not a staged roadmap or timeline"}
			}
			// Deliberately inconsistent ready=true and supplementary declarations
			// cannot override explicit goal misses in the fixed backend catalog.
			b, err := json.Marshal(map[string]any{"ready_to_answer": true, "summary": "Sources acquired", "missing": []string{}, "supplementary_criteria": supp, "criterion_assessments": verdicts})
			return string(b), err
		}
		var in struct {
			Claims []citationClaim `json:"claims_to_check"`
		}
		_ = json.Unmarshal([]byte(req.Messages[1].Content), &in)
		if len(in.Claims) > 0 {
			for i := range in.Claims {
				in.Claims[i].Kind = "supported"
				in.Claims[i].Quote = in.Claims[i].Claim
			}
			b, err := json.Marshal(reportCitationReview{Claims: in.Claims, CriteriaCovered: []string{}})
			return string(b), err
		}
		return base.respond(ctx, req, index, stream)
	}}
	w := New(f.store, llm.NewGuardedClient(model), researchRunRegistry(t, f.store, &researchRunSearch{}, &researchRunFetcher{}), Options{ReportArtifacts: files})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	task, _ := f.store.GetTask(ctx, f.task.ID)
	state, err := w.currentCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Plan.DeliverableCriteria) != 3 || task.Plan.DeliverableCriteria[1] != "Provide learning stages" || state.Report == nil || state.Report.Outcome != "partial" || len(state.Report.Unresolved) < 2 || state.Adaptive == nil || state.Adaptive.StopReason != "max_rounds" {
		t.Fatalf("acquisition replaced goal criteria: plan=%+v report=%+v", task.Plan, state.Report)
	}
	if len(state.SourceNotes) != 2 {
		t.Fatal("partial lost evidence notes")
	}
}

func TestReportByteBudgetFallbackIsBoundedAndRecoveryStable(t *testing.T) {
	r := &domain.ResearchReportState{GeneratedAt: "2026-10-26T12:00:00Z", PrivacyMode: domain.PrivacyOffline}
	sources := []citationEvidence{{EvidenceID: "source-a", Excerpt: strings.Repeat("Preserved relevant excerpt. ", 100), URL: "https://a.example.org/source"}, {EvidenceID: "source-b", Excerpt: strings.Repeat("Independent excerpt. ", 100), URL: "https://b.example.net/source"}}
	a, encoded, err := boundedReportDelivery("run", r, sources, []string{"goal.0", "goal.1"}, 4096, 8)
	if err != nil || len(a) > 4096 || len(encoded) > 4096 || !strings.Contains(a, "[source-a]") || r.Outcome != "partial" || !r.ByteBudgetFallback {
		t.Fatalf("byte fallback a=%d json=%d err=%v state=%+v", len(a), len(encoded), err, r)
	}
	b, replayed, err := boundedReportDelivery("run", r, sources, []string{"goal.0", "goal.1"}, 4096, 8)
	if err != nil || a != b || !bytes.Equal(encoded, replayed) {
		t.Fatal("budget fallback changed on recovery")
	}
}

type budgetExcerptExtractor struct{}

func (budgetExcerptExtractor) Extract(ctx context.Context, doc research.FetchedDocument) (research.ExtractedDocument, error) {
	v, err := (researchExtractorStub{}).Extract(ctx, doc)
	v.Text = strings.Repeat(v.Text+" ", 30)
	return v, err
}

func TestReportByteBudgetArtifactsResumeWithoutModelReplay(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research protocol architecture and deployment limitations.", func(s *domain.ResearchSession) {
		s.Policy.Version = 2
		s.Policy.AdaptiveEnabled = true
		s.Policy.SectionedReportEnabled = true
		s.Policy.MaxContinuations = 0
		s.Budget, _ = domain.RoundResearchBudgetForProfile(domain.ResearchProfileQuick)
		s.Budget.MaxReportBytes = 4096
	})
	files, err := artifacts.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	search, fetch := &adaptiveSearchFixture{}, &adaptiveFetchFixture{}
	registry := func() *tools.Registry {
		r := tools.NewRegistry()
		if err := tools.RegisterResearchTools(r, tools.ResearchTools{SearchProvider: search, Fetcher: fetch, Extractor: budgetExcerptExtractor{}, Artifacts: files, Repository: f.store}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	reviews := 0
	model := roundModelFixture(t, 1, &reviews)
	interrupted := &interruptReportSectionStore{Store: f.store, afterArtifacts: true}
	w := New(interrupted, llm.NewGuardedClient(model), registry(), Options{ReportArtifacts: files})
	w.runTask(ctx, f.task)
	if !interrupted.interrupted {
		t.Fatal("artifact boundary not reached")
	}
	calls := len(model.calls())
	f.reopen(t)
	if _, err := f.store.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	f.claim(t)
	w = New(f.store, llm.NewGuardedClient(model), registry(), Options{ReportArtifacts: files})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	state, err := w.currentCheckpoint(ctx, f.task.ID)
	if err != nil || state.Report == nil || !state.Report.ByteBudgetFallback || state.Report.Outcome != "partial" {
		t.Fatalf("byte-limited delivery missing: %+v %v", state.Report, err)
	}
	items, err := f.store.Artifacts(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range items {
		if strings.HasPrefix(a.Name, "research_report") {
			count++
			if a.SizeBytes > 4096 {
				t.Fatalf("report exceeded byte cap: %+v", a)
			}
		}
	}
	if count != 2 || len(model.calls()) != calls {
		t.Fatalf("recovery duplicated report work: artifacts=%d model=%d -> %d", count, calls, len(model.calls()))
	}
}

func TestGoalSupportingEvidenceSurvivesBoundedReportSelection(t *testing.T) {
	p := domain.Plan{SchemaVersion: 1, Goal: "Research", FinalDeliverable: "Report", MaxSteps: 1, Subtasks: []domain.PlanStep{{ID: "source_1", Kind: "fetch", Description: "Inspect", SuccessCriteria: []string{"stored"}}}}
	state := checkpointState{ResearchPlans: &domain.ResearchPlans{Version: 1, CurrentRound: 1, InitialPlan: p, Rounds: []domain.RoundPlan{{Number: 1, Plan: p}}}, ToolResults: map[string]any{}}
	for i := 1; i <= 9; i++ {
		state.ToolResults[fmt.Sprintf("source_%d", i)] = map[string]any{"evidence_id": fmt.Sprintf("ev%d", i)}
	}
	state.Adaptive = &domain.AdaptiveResearchState{Rounds: []domain.ResearchRound{{Coverage: []domain.VerificationCriterionResult{{StepID: "goal", Criterion: 0, Met: true, EvidenceIDs: []string{"ev9"}}}}}}
	ids := researchReportEvidenceIDs(state, 8)
	if len(ids) != 8 || !contains(ids, "ev9") {
		t.Fatalf("supporting ninth source discarded: %v", ids)
	}
}
