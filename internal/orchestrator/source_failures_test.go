package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
)

type sourceFailureFetcher struct {
	extractionFallbackFetcher
	failures map[string]error
	unsafe   bool
}

func (f *sourceFailureFetcher) ValidateURL(ctx context.Context, rawURL string) error {
	if f.unsafe && rawURL == "https://docs.example.com/page" {
		return research.ErrUnsafeURL
	}
	return f.extractionFallbackFetcher.ValidateURL(ctx, rawURL)
}

func (f *sourceFailureFetcher) Fetch(ctx context.Context, rawURL string, options research.FetchOptions) (research.FetchedDocument, error) {
	if err := f.failures[rawURL]; err != nil {
		f.calls[rawURL]++
		return research.FetchedDocument{}, err
	}
	return f.extractionFallbackFetcher.Fetch(ctx, rawURL, options)
}

func TestSourceFailureCandidateMatrix(t *testing.T) {
	declared := int64(257)
	for _, fixture := range []struct {
		name                                       string
		cause                                      error
		code                                       string
		bytes                                      int64
		required, unsafe, cancel, deadline, budget bool
	}{
		{name: "header", cause: &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "pdf_source_too_large", DeclaredBytes: &declared, EffectiveLimitBytes: 256, MIMEType: "application/pdf", LimitKind: "pdf"}, code: "pdf_source_too_large"},
		{name: "stream", cause: &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "pdf_source_too_large", FetchedBytes: 257, EffectiveLimitBytes: 256, MIMEType: "application/pdf"}, code: "pdf_source_too_large", bytes: 257},
		{name: "legacy", cause: research.ErrResponseTooBig, code: "source_response_too_large"},
		{name: "remaining_budget", cause: &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "task_fetch_budget_exceeded", LimitKind: "task_remaining"}, code: "task_fetch_budget_exceeded", budget: true},
		{name: "stream_exhausts_budget", cause: &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "source_response_too_large", FetchedBytes: 4096}, code: "source_response_too_large", bytes: 4096, budget: true},
		{name: "required_url", cause: research.ErrResponseTooBig, code: "source_response_too_large", required: true},
		{name: "execution_ssrf", cause: research.ErrUnsafeURL, code: "url_rejected_ssrf"},
		{name: "authorization_ssrf", unsafe: true, code: "url_rejected_ssrf"},
		{name: "invalid_arguments", cause: tools.ErrInvalidArguments, code: "invalid_tool_arguments"},
		{name: "approval", cause: tools.ErrApprovalRequired, code: "approval_required"},
		{name: "cancel_error", cause: context.Canceled},
		{name: "cancel_task", cancel: true},
		{name: "task_deadline", deadline: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			store, worker, task, state, _ := newInterruptedFetch(t, ctx)
			defer store.Close()
			const first = "https://docs.example.com/page"
			const alternate = "https://reference.example.net/page"
			fetcher := &sourceFailureFetcher{extractionFallbackFetcher: extractionFallbackFetcher{
				bodies: map[string]string{first: "<main>First source</main>", alternate: "<main>A complete independent reference for protocol compatibility.</main>"}, calls: map[string]int{},
			}, failures: map[string]error{first: fixture.cause}, unsafe: fixture.unsafe}
			artifactStore, err := artifacts.New(t.TempDir(), 4096)
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{Fetcher: fetcher, Extractor: research.NewHTMLExtractorWithPDF(10_000, nil), Artifacts: artifactStore, Repository: store, MaxFetchBytes: 4096}); err != nil {
				t.Fatal(err)
			}
			registry.SeedTaskURLs(task.ID, first+" "+alternate)
			worker.tools, worker.options.MaxFetchBytes = registry, 4096
			ctx = tools.WithTaskID(ctx, task.ID)
			if fixture.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if fixture.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			var decision actionDecision
			if err := json.Unmarshal([]byte(`{"tool_call":{"name":"fetch_url","arguments":{"url":"https://docs.example.com/page"}}}`), &decision); err != nil {
				t.Fatal(err)
			}
			stage := domain.StageFetch
			result, _, exhausted, err := worker.executeFetchCandidates(ctx, task.ID, &stage, domain.PlanStep{ID: "fetch_1", Kind: "fetch"}, "researcher", "read", []string{first, alternate}, &decision, &state, map[string]struct{}{}, "", fixture.required)
			wantFallback := !fixture.required && !fixture.unsafe && !fixture.cancel && !fixture.deadline && !fixture.budget && sourceFallbackCode(fixture.code)
			if wantFallback {
				if err != nil || result == nil || exhausted || fetcher.calls[alternate] != 1 {
					t.Fatalf("fallback result=%v exhausted=%t calls=%v err=%v", result, exhausted, fetcher.calls, err)
				}
			} else if err == nil || fetcher.calls[alternate] != 0 {
				t.Fatalf("forbidden fallback: calls=%v err=%v", fetcher.calls, err)
			}
			if fixture.budget && !errors.Is(err, errFetchBudgetExceeded) {
				t.Fatalf("want aggregate error, got %v", err)
			}
			if state.FetchedBytes != fixture.bytes {
				t.Fatalf("charged=%d want=%d", state.FetchedBytes, fixture.bytes)
			}
			if fixture.code != "" {
				if len(state.SourceFailures) != 1 || state.SourceFailures[0].Code != fixture.code || state.SourceFailures[0].TryNextSource != wantFallback {
					t.Fatalf("metadata=%#v", state.SourceFailures)
				}
				if !wantFallback && state.PendingTool != "" {
					t.Fatalf("pending terminal failure=%q", state.PendingTool)
				}
			}
		})
	}
}

func TestSourceFailureExactExtractionCodes(t *testing.T) {
	for _, fixture := range []struct {
		cause error
		code  string
	}{
		{pdfextract.ErrInputTooLarge, "pdf_extraction_input_too_large"},
		{pdfextract.ErrTextTooLarge, "pdf_text_limit_exceeded"},
		{pdfextract.ErrOutputExceeded, "pdf_resource_exceeded"},
		{pdfextract.ErrResourceExceeded, "pdf_resource_exceeded"},
		{pdfextract.ErrTimeout, "pdf_timeout"},
		{pdfextract.ErrMalformed, "pdf_malformed"},
		{research.ErrPDFEncrypted, "pdf_encrypted"},
		{research.ErrPDFTooManyPages, "pdf_page_limit_exceeded"},
		{research.ErrPDFExtractorUnavailable, "pdf_extractor_unavailable"},
	} {
		err := &research.FetchFailure{Err: errors.Join(research.ErrPDFExtractionFailed, fixture.cause), FetchedBytes: 128}
		if got := fetchFailureCode(err); got != fixture.code {
			t.Errorf("cause=%v code=%s want=%s", fixture.cause, got, fixture.code)
		}
	}
}

func TestSourceFailureRecoveryDeduplicatesAndRepairsPublicEvent(t *testing.T) {
	for _, window := range []string{"audit_only", "audit_and_public", "checkpoint_before_public"} {
		t.Run(window, func(t *testing.T) {
			ctx := context.Background()
			store, worker, task, state, _ := newInterruptedFetch(t, ctx)
			defer store.Close()
			failure := sourceFailure{AttemptID: "durable-attempt", StepID: "fetch_1", Tool: "fetch_url", TargetURL: "https://docs.example.com/page", Status: "rejected", Code: "pdf_source_too_large", FetchedBytes: 257, EffectiveLimitBytes: 256, TryNextSource: true, RemainingCandidates: 1, CandidateIndex: 2}
			if _, err := store.RecordEvent(ctx, task.ID, "tool.failed", nil, "audit", failure.payload()); err != nil {
				t.Fatal(err)
			}
			if window == "checkpoint_before_public" {
				applySourceFailure(&state, failure)
				clearPendingFetch(&state)
				state.FetchedBytes = 257
				if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
					t.Fatal(err)
				}
			}
			if window == "audit_and_public" {
				if _, err := store.RecordEvent(ctx, task.ID, "tool.failed", nil, "user", failure.payload()); err != nil {
					t.Fatal(err)
				}
			}
			recovered, _, err := worker.reconcileResearchState(ctx, task.ID, state)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.FetchedBytes != 257 || len(recovered.SourceFailures) != 1 || recovered.PendingTool != "" || !contains(recovered.RejectedFetchURLs, failure.TargetURL) {
				t.Fatalf("recovered=%#v", recovered)
			}
			if _, err := store.SaveCheckpoint(ctx, task.ID, 1, recovered); err != nil {
				t.Fatal(err)
			}
			second, changed, err := worker.reconcileResearchState(ctx, task.ID, recovered)
			if err != nil || changed || second.FetchedBytes != 257 {
				t.Fatalf("second=%#v changed=%t err=%v", second, changed, err)
			}
			events, err := store.EventsAfter(ctx, task.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			public := 0
			for _, event := range events {
				if event.Type == "tool.failed" && event.Visibility == "user" {
					public++
				}
			}
			if public != 1 {
				t.Fatalf("public failures=%d", public)
			}
			if remaining := filterFetchURLs([]string{failure.TargetURL, "https://docs.example.com:443/page#again"}, map[string]struct{}{}, second.RejectedFetchURLs); len(remaining) != 0 {
				t.Fatalf("terminal source eligible after restart: %v", remaining)
			}
		})
	}
}

func TestSourceFailuresAllCandidatesFailVerification(t *testing.T) {
	ctx := context.Background()
	store, _, _, _, _ := newInterruptedFetch(t, ctx)
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "all-source-failures", Title: "Protocol review", Description: "Research protocol architecture and limitations."}, "")
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
	urls := []string{"https://docs.example.com/protocol", "https://reference.example.net/protocol", "https://benchmark.example.org/protocol"}
	fetcher := &sourceFailureFetcher{extractionFallbackFetcher: extractionFallbackFetcher{bodies: map[string]string{}, calls: map[string]int{}}, failures: map[string]error{}}
	for _, rawURL := range urls {
		fetcher.bodies[rawURL] = "unused"
		fetcher.failures[rawURL] = &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "pdf_source_too_large"}
	}
	artifactStore, err := artifacts.New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: extractionFallbackProvider{}, Fetcher: fetcher, Extractor: research.NewHTMLExtractorWithPDF(10_000, nil), Artifacts: artifactStore, Repository: store, MaxFetchBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	worker := New(store, integrationResearchModel(), registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxFetchBytes: 4096})
	worker.runTask(ctx, claimed)
	state, err := worker.currentCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SourceFailures) != 3 || state.PendingTool != "" || state.FetchedBytes != 0 {
		t.Fatalf("state=%#v", state)
	}
	for _, rawURL := range urls {
		if fetcher.calls[rawURL] != 1 {
			t.Fatalf("calls=%v", fetcher.calls)
		}
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 300)
	if err != nil {
		t.Fatal(err)
	}
	code := ""
	for _, event := range events {
		if event.Type == "task.failed" {
			var payload struct {
				Code string `json:"error_code"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			code = payload.Code
		}
	}
	if code != "verification_incomplete" {
		t.Fatalf("all sources failed code=%q", code)
	}
}

func TestSourceFailureSensitiveURLUsesFingerprint(t *testing.T) {
	ctx := context.Background()
	store, worker, task, state, _ := newInterruptedFetch(t, ctx)
	defer store.Close()
	const rawURL = "https://docs.example.com/page?api_key=private-fixture-secret"
	if err := worker.recordSourceFailure(ctx, task.ID, nil, "fetch_1", rawURL, research.ErrUnsafeURL, "url_rejected_ssrf", true, false, 0, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.SourceFailures) != 1 || state.SourceFailures[0].TargetURL != "" || len(state.SourceFailures[0].Fingerprint) != 64 {
		t.Fatalf("metadata=%#v", state.SourceFailures)
	}
	if remaining := filterFailedSourceIdentities([]string{rawURL}, state.SourceFailures); len(remaining) != 0 {
		t.Fatalf("rejected fingerprint eligible=%v", remaining)
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "tool.failed" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["target_url"] != nil || payload["source_fingerprint"] == "" {
			t.Fatalf("unsafe public/audit URL metadata: %s", event.Payload)
		}
	}
}
