package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
			Section    string              `json:"section_title"`
			Problem    string              `json:"validation_error"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			return "", err
		}
		// Report roles see numbered source aliases, never raw evidence UUIDs.
		for _, item := range input.Evidence {
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(item.EvidenceID) {
				t.Fatalf("report role saw a raw evidence ID: %q", item.EvidenceID)
			}
		}
		for _, item := range input.Notes {
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(item.EvidenceID) {
				t.Fatalf("outline saw a raw evidence ID: %q", item.EvidenceID)
			}
		}
		if regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).Match(request.JSONSchema) {
			t.Fatalf("report schema exposes raw evidence IDs: %s", request.JSONSchema)
		}
		russian := strings.Contains(request.Messages[0].Content, "in Russian")
		// The fact checker only compares quotes; it writes no report text.
		if len(input.Claims) == 0 && !strings.Contains(request.Messages[0].Content, "You check fact records") && (mode == "russian") != russian {
			t.Fatalf("report prompt language does not follow the question: %s", request.Messages[0].Content)
		}
		if strings.Contains(request.Messages[0].Content, "Create a small research report outline") {
			if len(input.Notes) != 2 {
				t.Fatalf("outline lost compact notes: %+v", input.Notes)
			}
			// Duplicate IDs are schema-valid model content, not corrupt durable state.
			raw, err := json.Marshal(map[string]any{"title": "Research Report Outline: Проверяемый отчёт", "entities": []string{}, "sections": []map[string]any{{"title": "Основные сведения", "evidence_ids": []string{input.Notes[0].EvidenceID, input.Notes[0].EvidenceID}, "criteria": input.IDs}, {"title": "Независимый источник", "evidence_ids": []string{input.Notes[1].EvidenceID}, "criteria": []string{}}}})
			return string(raw), err
		}
		if reply, ok := reportFactsFixtureReply(request, input.Evidence); ok {
			return reply, nil
		}
		if len(input.Claims) > 0 {
			covered := input.Criteria
			if mode == "coverage_gap" {
				covered = []string{}
			}
			return indexedCriticReply(request.Messages[1].Content, covered, func(claim string) bool { return !strings.Contains(claim, "Invented") })
		}
		if input.Correction && !strings.Contains(input.Problem, "[1]") {
			t.Fatalf("correction did not explain the format error: %q", input.Problem)
		}
		if mode == "infrastructure" {
			return "", sqlite.ErrCheckpointInvalid
		}
		if mode == "fallback" || (mode == "correction" && !input.Correction) || (mode == "first_fails" && input.Section == "Основные сведения") {
			return `{"answer":"UNSUPPORTED uncited assertion"}`, nil
		}
		if len(input.Evidence) == 0 {
			return "", errors.New("section has no evidence")
		}
		answer := input.Evidence[0].Excerpt + " [" + input.Evidence[0].EvidenceID + "]"
		if mode == "partial" {
			answer = "Invented roadmap claim [" + input.Evidence[0].EvidenceID + "]\n\n" + answer
		}
		if mode == "list" {
			answer = "- " + answer + "\n* " + input.Evidence[0].Excerpt + " [" + input.Evidence[0].EvidenceID + "][" + input.Evidence[len(input.Evidence)-1].EvidenceID + "]."
		}
		raw, err := json.Marshal(map[string]string{"answer": answer})
		return string(raw), err
	}}
}

func TestSectionedReportsPersistResumeCorrectAndFallback(t *testing.T) {
	for _, mode := range []string{"valid", "artifact_recovery", "correction", "fallback", "coverage_gap", "infrastructure", "list", "partial", "first_fails", "russian"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			question := "Research protocol architecture and limitations."
			if mode == "russian" {
				question = "Исследуй архитектуру протокола и его ограничения."
			}
			f.submit(t, question, func(s *domain.ResearchSession) { s.Policy.SectionedReportEnabled = true; s.Budget.MaxLLMCalls = 40 })
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
				if mode == "fallback" || (mode == "first_fails" && index == 0) {
					want = "fallback_excerpts"
				}
				// After the first section fails the same content check twice
				// (draft + correction), the report circuit breaker skips further
				// model calls and later sections fall back directly.
				corrected := section.CorrectionUsed || (mode == "fallback" && index > 0 && section.Limitation == "report_circuit_breaker")
				if section.Status != want || ((mode == "correction" || mode == "fallback") && !corrected) {
					t.Fatalf("section validation/correction failed: %+v", section)
				}
				if mode == "partial" && (strings.Contains(section.Body, "Invented") || section.Limitation != "unsupported_claims_removed" || !strings.Contains(section.Body, "["+section.EvidenceIDs[0]+"]")) {
					t.Fatalf("unsupported claim was not dropped individually: %+v", section)
				}
				if mode == "list" && (!strings.HasPrefix(section.Body, "- ") || strings.Count(section.Body, "\n- ") != 1) {
					t.Fatalf("list items were not kept as a list: %q", section.Body)
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
			headings := []string{"## TL;DR", "## Contents", "## Limitations", "## Methodology", "## Sources", "## Verification", "## Coverage", "| # | Source | Site |"}
			if mode != "fallback" {
				headings = append(headings, "| supported by sources |")
			}
			if mode == "russian" {
				headings = []string{"## Коротко", "## Содержание", "## Ограничения", "## Методология", "## Источники", "## Проверка", "## Покрытие", "| подтверждено источниками |", "**Факты**"}
			}
			// Statuses moved into the verification table at the end.
			if strings.Contains(answer, "Section status:") || strings.Contains(answer, "Статус секции:") || strings.Contains(answer, "Анализ основан на") {
				t.Fatalf("noisy per-section status or boilerplate summary in the body: %s", answer)
			}
			if state.Report.Title != "Проверяемый отчёт" {
				t.Fatalf("outline label leaked into the report title: %q", state.Report.Title)
			}
			for _, heading := range headings {
				if !strings.Contains(answer, heading) {
					t.Fatalf("missing backend report section %q", heading)
				}
			}
			if strings.Contains(answer, "UNSUPPORTED") || strings.Contains(answer, "Invented") {
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
			// One failed section must not skip the next: section 1 uses its
			// draft and correction, section 2 still gets its own attempt.
			// Each section also gets a fact extraction and a fact check, the
			// report one TL;DR call.
			if mode == "fallback" && len(model.calls()) != 11 {
				t.Fatalf("later section was not attempted after one failure: calls=%d", len(model.calls()))
			}
			// planner + verifier + outline + two sections + two section critics
			// + two fact extractions + two fact checks + TL;DR.
			if (mode == "valid" || mode == "artifact_recovery") && len(model.calls()) != 12 {
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

func TestReportContentBreakerTripsOnlyAcrossDifferentSections(t *testing.T) {
	var breaker reportContentBreaker
	if breaker.observe("section_1", errCitationMapping) || breaker.observe("section_1", errCitationMapping) {
		t.Fatal("draft and correction of one section tripped the breaker")
	}
	if breaker.observe("section_2", errUnsupportedCitationClaim) {
		t.Fatal("different failure codes were counted as identical")
	}
	if !breaker.observe("section_2", errCitationMapping) || !breaker.tripped {
		t.Fatalf("same failure in two sections did not trip: %+v", breaker)
	}
	breaker.success()
	for _, section := range []string{"section_4", "section_5", "section_6"} {
		if breaker.observe(section, errUnsupportedCitationClaim) {
			t.Fatal("unsupported claims in several sections tripped the breaker")
		}
	}
	if breaker.tripped || len(breaker.failed) != 0 {
		t.Fatalf("successful section did not reset breaker: %+v", breaker)
	}
	if breaker.observe("section_3", sqlite.ErrResearchBudgetExceeded) || len(breaker.failed) != 0 {
		t.Fatal("budget failure entered content circuit breaker")
	}
}
