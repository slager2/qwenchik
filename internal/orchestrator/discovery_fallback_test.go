package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
)

type discoveryFallbackProvider struct{ calls int }

func (p *discoveryFallbackProvider) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	p.calls++
	return (extractionFallbackProvider{}).Search(ctx, options)
}

func TestWorkerOptionalURLFailureContinuesIndependentDiscovery(t *testing.T) {
	for _, fixture := range []struct {
		name                                   string
		required, tooFewFetches, laterFailures bool
		wantCode                               string
		wantSearches                           int
	}{
		{name: "optional_pdf_then_two_valid_sources", wantSearches: 2},
		{name: "required_url_without_additional_search", required: true, wantCode: "pdf_source_too_large"},
		{name: "remaining_fetch_slots_cannot_meet_floor", tooFewFetches: true, wantCode: "verification_incomplete"},
		{name: "later_discovery_cannot_supply_evidence", laterFailures: true, wantCode: "verification_incomplete", wantSearches: 2},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			store, _, _, _, _ := newInterruptedFetch(t, ctx)
			defer store.Close()
			const pdfURL = "https://docs.example.com/protocol"
			const referenceURL = "https://reference.example.net/protocol"
			const alternateURL = "https://benchmark.example.org/protocol"
			goal := "The PDF at " + pdfURL + " is optional. Research protocol architecture and limitations using other independent sources if it is unavailable."
			if fixture.required {
				goal = "Read " + pdfURL + " without additional search and summarize that PDF only."
			}
			task, err := store.CreateTask(ctx, domain.Task{ID: "discovery-fallback", Title: "Protocol review", Description: goal}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := store.ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim=%t err=%v", ok, err)
			}
			fetcher := &sourceFailureFetcher{extractionFallbackFetcher: extractionFallbackFetcher{bodies: map[string]string{
				pdfURL:       "unused oversized PDF",
				referenceURL: "<title>Reference</title><main>The protocol uses framed messages and supports independent implementations.</main>",
				alternateURL: "<title>Compatibility</title><main>Interoperability requires compatible framing and supported protocol versions.</main>",
			}, calls: map[string]int{}}, failures: map[string]error{pdfURL: &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "pdf_source_too_large", Phase: "download", EffectiveLimitBytes: 256, MIMEType: "application/pdf", LimitKind: "pdf"}}}
			if fixture.laterFailures {
				fetcher.failures[referenceURL] = research.ErrFetchRequestFailed
				fetcher.failures[alternateURL] = research.ErrFetchRequestFailed
			}
			provider := &discoveryFallbackProvider{}
			artifactStore, err := artifacts.New(t.TempDir(), 4096)
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider, Fetcher: fetcher, Extractor: research.NewHTMLExtractorWithPDF(10_000, nil), Artifacts: artifactStore, Repository: store, MaxFetchBytes: 4096}); err != nil {
				t.Fatal(err)
			}
			model := integrationResearchModel()
			respond := model.respond
			verifierSawSkipped := false
			model.respond = func(request llm.ChatRequest, index int) (string, error) {
				if index == 0 {
					encoded, err := respond(request, 0)
					if err != nil {
						return "", err
					}
					var plan domain.Plan
					if err := json.Unmarshal([]byte(encoded), &plan); err != nil {
						return "", err
					}
					optional := domain.PlanStep{ID: "fetch_optional", Kind: "fetch", Description: "Inspect optional supplied PDF; independent sources may replace it", Dependencies: []string{}, ExpectedOutput: "PDF evidence if available", SuccessCriteria: []string{"PDF evidence stored if available"}}
					plan.Subtasks = append([]domain.PlanStep{optional}, plan.Subtasks...)
					if fixture.tooFewFetches {
						plan.Subtasks = plan.Subtasks[:len(plan.Subtasks)-1]
					}
					plan.MaxSteps = len(plan.Subtasks)
					out, err := json.Marshal(plan)
					return string(out), err
				}
				if index == 1 {
					return `{"status":"continue","summary":"Inspect supplied PDF","tool_call":{"name":"fetch_url","arguments":{"url":"https://docs.example.com/protocol","original_query":"optional supplied source"}}}`, nil
				}
				if index == 4 {
					return `{"status":"continue","summary":"Inspect discovered reference","tool_call":{"name":"fetch_url","arguments":{"url":"https://reference.example.net/protocol","original_query":"protocol architecture"}}}`, nil
				}
				if index == 5 {
					return `{"status":"continue","summary":"Inspect independent compatibility source","tool_call":{"name":"fetch_url","arguments":{"url":"https://benchmark.example.org/protocol","original_query":"protocol compatibility"}}}`, nil
				}
				if index == 6 {
					var input struct {
						Skipped []string    `json:"skipped_steps"`
						Plan    domain.Plan `json:"plan"`
					}
					if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
						return "", err
					}
					verifierSawSkipped = contains(input.Skipped, "fetch_1")
					for _, step := range input.Plan.Subtasks {
						if step.ID == "fetch_1" {
							t.Error("verifier requires failed optional PDF")
						}
					}
				}
				if index == 6 || index == 7 {
					var input map[string]any
					if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
						return "", err
					}
					results := input["tool_results"].(map[string]any)
					results["fetch_1"], results["fetch_2"] = results["fetch_2"], results["fetch_3"]
					encoded, err := json.Marshal(input)
					if err != nil {
						return "", err
					}
					request.Messages[1].Content = string(encoded)
				}
				return respond(request, index-1)
			}
			worker := New(store, model, registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxSearchQueries: 4, MaxFetchBytes: 4096})
			worker.runTask(ctx, claimed)
			current, err := store.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			state, err := worker.currentCheckpoint(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if provider.calls != fixture.wantSearches || fetcher.calls[pdfURL] != 1 {
				t.Fatalf("searches=%d fetches=%v", provider.calls, fetcher.calls)
			}
			events, err := store.EventsAfter(ctx, task.ID, 0, 300)
			if err != nil {
				t.Fatal(err)
			}
			code := ""
			warnings := 0
			skipReason := ""
			for _, event := range events {
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if event.Type == "task.failed" {
					code, _ = payload["error_code"].(string)
				}
				if event.Type == "research.warning" && payload["partial"] == true {
					warnings++
				}
				if event.Type == "step.skipped" && payload["step_id"] == "fetch_1" {
					skipReason, _ = payload["reason"].(string)
				}
			}
			if code != fixture.wantCode {
				t.Fatalf("code=%q want=%q status=%s", code, fixture.wantCode, current.Status)
			}
			if fixture.wantCode != "" {
				if current.Status != domain.StatusFailed {
					t.Fatalf("false completion: %s", current.Status)
				}
				if fixture.wantSearches == 0 && (contains(state.SkippedSteps, "fetch_optional") || contains(state.SkippedSteps, "fetch_1")) {
					t.Fatal("required or non-viable source was skipped")
				}
				return
			}
			evidence, err := store.Evidence(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != domain.StatusCompleted || len(evidence) != 2 || !state.Partial || warnings != 1 || !verifierSawSkipped || !strings.Contains(skipReason, "fallback discovery pending") {
				t.Fatalf("status=%s evidence=%d partial=%t warnings=%d verifierSkipped=%t reason=%q", current.Status, len(evidence), state.Partial, warnings, verifierSawSkipped, skipReason)
			}
			if len(state.SourceFailures) != 1 || !contains(state.RejectedFetchURLs, pdfURL) {
				t.Fatalf("source failure lost: %#v", state)
			}
		})
	}
}
