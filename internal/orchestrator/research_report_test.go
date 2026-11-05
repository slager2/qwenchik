package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type interruptReportSectionStore struct {
	*sqlite.Store
	interrupted    bool
	afterArtifacts bool
}

func (s *interruptReportSectionStore) SaveCheckpoint(ctx context.Context, id string, version int, state any) (domain.Checkpoint, error) {
	checkpoint, err := s.Store.SaveCheckpoint(ctx, id, version, state)
	if err != nil {
		return checkpoint, err
	}
	if value, ok := state.(checkpointState); ok && value.Report != nil && !s.interrupted {
		if s.afterArtifacts {
			if value.Report.ArtifactID != "" && value.Report.MarkdownArtifactID != "" {
				s.interrupted = true
				return checkpoint, ErrInterrupted
			}
			return checkpoint, nil
		}
		for _, section := range value.Report.Sections {
			if section.Status == "supported" {
				s.interrupted = true
				return checkpoint, ErrInterrupted
			}
		}
	}
	return checkpoint, nil
}

func reportModelFixture(t *testing.T, mode string) *researchRunModel {
	t.Helper()
	base := integrationResearchModel()
	return &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, _ bool) (string, error) {
		if isPlannerRequest(request) || isVerifierRequest(request) {
			return base.respond(request, index)
		}
		if request.Model != "saved-local-model" || request.BaseURL != "http://127.0.0.1:8001/v1" {
			t.Fatalf("report bypassed immutable session settings: model=%q base=%q", request.Model, request.BaseURL)
		}
		var input struct {
			Notes      []domain.SourceNote `json:"source_notes"`
			IDs        []string            `json:"criterion_ids"`
			Evidence   []citationEvidence  `json:"evidence_catalogue"`
			Claims     []citationClaim     `json:"claims_to_check"`
			Criteria   []string            `json:"criteria"`
			Correction bool                `json:"correction"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			return "", err
		}
		if strings.Contains(request.Messages[0].Content, "Create a small research report outline") {
			if len(input.Notes) != 2 {
				t.Fatalf("outline lost compact notes: %+v", input.Notes)
			}
			// Duplicate IDs are schema-valid model content, not corrupt durable state.
			raw, err := json.Marshal(map[string]any{"title": "Проверяемый отчёт", "sections": []map[string]any{{"title": "Основные сведения", "evidence_ids": []string{input.Notes[0].EvidenceID, input.Notes[0].EvidenceID}, "criteria": input.IDs}, {"title": "Независимый источник", "evidence_ids": []string{input.Notes[1].EvidenceID}, "criteria": []string{}}}})
			return string(raw), err
		}
		if len(input.Claims) > 0 {
			for index := range input.Claims {
				input.Claims[index].Kind = "supported"
				input.Claims[index].Quote = input.Claims[index].Claim
			}
			covered := input.Criteria
			if mode == "coverage_gap" {
				covered = []string{}
			}
			raw, err := json.Marshal(reportCitationReview{Claims: input.Claims, CriteriaCovered: covered})
			return string(raw), err
		}
		if mode == "infrastructure" {
			return "", sqlite.ErrCheckpointInvalid
		}
		if mode == "fallback" || (mode == "correction" && !input.Correction) {
			return `{"answer":"UNSUPPORTED uncited assertion"}`, nil
		}
		if len(input.Evidence) == 0 {
			return "", errors.New("section has no evidence")
		}
		raw, err := json.Marshal(map[string]string{"answer": input.Evidence[0].Excerpt + " [" + input.Evidence[0].EvidenceID + "]"})
		return string(raw), err
	}}
}

func TestSectionedReportsPersistResumeCorrectAndFallback(t *testing.T) {
	for _, mode := range []string{"valid", "artifact_recovery", "correction", "fallback", "coverage_gap", "infrastructure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) { s.Policy.SectionedReportEnabled = true; s.Budget.MaxLLMCalls = 40 })
			files, err := artifacts.New(t.TempDir(), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			model := reportModelFixture(t, mode)
			search, fetcher := &researchRunSearch{}, &researchRunFetcher{}
			registry := researchRunRegistry(t, f.store, search, fetcher)
			interrupt := &interruptReportSectionStore{Store: f.store, interrupted: mode != "valid" && mode != "artifact_recovery", afterArtifacts: mode == "artifact_recovery"}
			w := New(interrupt, llm.NewGuardedClient(model), registry, Options{ReportArtifacts: files})
			w.runTask(ctx, f.task)
			if mode == "valid" || mode == "artifact_recovery" {
				if !interrupt.interrupted {
					t.Fatal("section checkpoint interruption did not happen")
				}
				f.reopen(t)
				if recovered, err := f.store.RecoverInterrupted(ctx); err != nil || recovered != 1 {
					t.Fatalf("report recovery failed: count=%d err=%v", recovered, err)
				}
				f.claim(t)
				w = New(f.store, llm.NewGuardedClient(model), researchRunRegistry(t, f.store, search, fetcher), Options{ReportArtifacts: files})
				w.runTask(ctx, f.task)
			}
			if mode == "infrastructure" {
				task, err := f.store.GetTask(ctx, f.task.ID)
				if err != nil || task.Status != domain.StatusFailed {
					t.Fatalf("infrastructure error became completion: %+v %v", task, err)
				}
				items, err := f.store.Artifacts(ctx, f.task.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range items {
					if item.Name == "research_report.json" {
						t.Fatal("infrastructure failure produced a report")
					}
				}
				return
			}
			assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
			state, err := w.currentCheckpoint(ctx, f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Report == nil || state.Report.ArtifactID == "" || state.Report.MarkdownArtifactID == "" || len(state.Report.Sections) != 2 {
				t.Fatalf("report artifacts/state missing: %+v", state.Report)
			}
			for index, section := range state.Report.Sections {
				want := "supported"
				if mode == "fallback" || (mode == "coverage_gap" && len(section.Criteria) > 0) {
					want = "fallback_excerpts"
				}
				// After the first section fails the same content check twice
				// (draft + correction), the report circuit breaker skips further
				// model calls and later sections fall back directly.
				corrected := section.CorrectionUsed || (mode == "fallback" && index > 0 && section.Limitation == "report_circuit_breaker")
				if section.Status != want || ((mode == "correction" || mode == "fallback") && !corrected) {
					t.Fatalf("section validation/correction failed: %+v", section)
				}
			}
			if mode == "coverage_gap" && (state.Report.Outcome != "partial" || len(state.Report.UnrepresentedCriteria) == 0) {
				t.Fatalf("section support invented criterion coverage: %+v", state.Report)
			}
			messages, err := f.store.ConversationMessages(ctx, f.turn.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			answer := messages[1].Content
			for _, heading := range []string{"## Содержание", "## Ограничения", "## Методология", "## Источники", "## Coverage"} {
				if !strings.Contains(answer, heading) {
					t.Fatalf("missing backend report section %q", heading)
				}
			}
			if strings.Contains(answer, "UNSUPPORTED") {
				t.Fatal("rejected section draft leaked into final report")
			}
			raw, err := files.ReadRange(ctx, f.task.ID, state.Report.ArtifactID, 0, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"type":"research_report"`) || strings.Contains(string(raw), "UNSUPPORTED") {
				t.Fatalf("invalid report artifact: %s", raw)
			}
			if search.calls.Load() != 2 || fetcher.calls.Load() != 2 {
				t.Fatal("report restart repeated discovery/fetch")
			}
			// planner + verifier + outline + two sections + two section critics.
			if (mode == "valid" || mode == "artifact_recovery") && len(model.calls()) != 7 {
				t.Fatalf("completed report calls replayed after reopen: count=%d", len(model.calls()))
			}
			items, err := f.store.Artifacts(ctx, f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			reportArtifacts := 0
			for _, item := range items {
				if item.Name == "research_report.json" || item.Name == "research_report.md" {
					reportArtifacts++
				}
			}
			if reportArtifacts != 2 {
				t.Fatalf("report recovery duplicated artifacts: %d", reportArtifacts)
			}
		})
	}
}

func TestReportBodiesCannotInventSourceLinks(t *testing.T) {
	for _, body := range []string{"A [link](https://invented.example)", "A https://invented.example", "<a href='https://invented.example'>link</a>", "# Forged heading", "<https://invented.example>"} {
		if err := validateReportBody(body); err == nil {
			t.Fatalf("model-controlled link/heading accepted: %q", body)
		}
	}
	if err := validateReportBody(fmt.Sprintf("Supported claim [%s]", strings.Repeat("a", 36))); err != nil {
		t.Fatal(err)
	}
}

func TestReportSectionRetriesOnlyRepairableFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		code       string
		repairable bool
	}{
		{"citation shape", errCitationMapping, "citation_mapping_invalid", true},
		{"invalid JSON", errInvalidModelOutput, "invalid_model_output", true},
		{"truncated", llm.ErrOutputTruncated, "output_truncated", true},
		{"unsupported claim", errUnsupportedCitationClaim, "claim_not_supported", false},
		{"input budget", errInputBudgetExceeded, "input_budget_exceeded", false},
		{"research budget", sqlite.ErrResearchBudgetExceeded, "budget_exhausted", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := reportFailureCode(test.err); got != test.code {
				t.Fatalf("failure code = %q, want %q", got, test.code)
			}
			if got := reportFailureRepairable(test.err); got != test.repairable {
				t.Fatalf("repairable = %v, want %v", got, test.repairable)
			}
		})
	}
}

func TestReportContentBreakerTripsOnlyOnRepeatedSameContentFailure(t *testing.T) {
	var breaker reportContentBreaker
	if breaker.observe(errInvalidModelOutput) {
		t.Fatal("circuit breaker tripped on the first content failure")
	}
	if breaker.failures != 1 || breaker.code != "invalid_model_output" {
		t.Fatalf("first failure state=%+v", breaker)
	}
	if !breaker.observe(errInvalidModelOutput) || !breaker.tripped {
		t.Fatalf("circuit breaker did not trip on repeated content failure: %+v", breaker)
	}

	breaker.success()
	if breaker.tripped || breaker.failures != 0 || breaker.code != "" {
		t.Fatalf("successful section did not reset breaker: %+v", breaker)
	}
	if breaker.observe(errCitationMapping) {
		t.Fatal("circuit breaker tripped on a different first failure")
	}
	if breaker.observe(errUnsupportedCitationClaim) {
		t.Fatal("different failure codes were counted as identical")
	}
	if breaker.failures != 1 || breaker.code != "claim_not_supported" {
		t.Fatalf("different failure state=%+v", breaker)
	}
	if breaker.observe(sqlite.ErrResearchBudgetExceeded) {
		t.Fatal("budget failure entered content circuit breaker")
	}
}
