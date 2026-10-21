package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type pdfFallbackFetcher struct {
	extractionFallbackFetcher
	pdfURL   string
	pdfLimit int64
}

func (f *pdfFallbackFetcher) Fetch(ctx context.Context, rawURL string, options research.FetchOptions) (research.FetchedDocument, error) {
	if err := f.ValidateURL(ctx, rawURL); err != nil {
		return research.FetchedDocument{}, err
	}
	if rawURL == f.pdfURL && int64(len(f.bodies[rawURL])) > min(f.pdfLimit, options.MaxBytes) {
		f.calls[rawURL]++
		declared := int64(len(f.bodies[rawURL]))
		return research.FetchedDocument{}, &research.FetchFailure{
			Err:  fmt.Errorf("%w: application/pdf Content-Length=%d limit=%d", research.ErrResponseTooBig, len(f.bodies[rawURL]), min(f.pdfLimit, options.MaxBytes)),
			Code: "pdf_source_too_large", Phase: "download", DeclaredBytes: &declared,
			EffectiveLimitBytes: min(f.pdfLimit, options.MaxBytes), MIMEType: "application/pdf", LimitKind: "pdf",
		}
	}
	return f.extractionFallbackFetcher.Fetch(ctx, rawURL, options)
}

func TestPDFFallbackOversizedFirstSourceMustNotExhaustTaskBudget(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/pdf-fallback.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifactStore, err := artifacts.New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	const pdfURL = "https://docs.example.com/protocol"
	const referenceURL = "https://reference.example.net/protocol"
	const alternateURL = "https://benchmark.example.org/protocol"
	const taskBudget int64 = 4096
	fetcher := &pdfFallbackFetcher{
		extractionFallbackFetcher: extractionFallbackFetcher{
			bodies: map[string]string{
				pdfURL:       "%PDF-1.7\n" + strings.Repeat("x", 256),
				referenceURL: "<title>Reference</title><main>The protocol uses framed messages and supports independent implementations.</main>",
				alternateURL: "<title>Compatibility</title><main>Interoperability requires compatible framing and supported protocol versions.</main>",
			},
			calls: map[string]int{},
		},
		pdfURL: pdfURL, pdfLimit: 256,
	}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{
		SearchProvider: extractionFallbackProvider{}, Fetcher: fetcher,
		Extractor: research.NewHTMLExtractorWithPDF(10_000, nil),
		Artifacts: artifactStore, Repository: store, MaxFetchBytes: taskBudget,
	}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, domain.Task{ID: "pdf-fallback", Title: "Protocol review", Description: "Research protocol architecture and limitations."}, "")
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
	model := integrationResearchModel()
	respond := model.respond
	model.respond = func(request llm.ChatRequest, index int) (string, error) {
		if index == 4 {
			return `{"status":"continue","summary":"Inspect independent HTML alternate","tool_call":{"name":"fetch_url","arguments":{"url":"https://benchmark.example.org/protocol","original_query":"protocol compatibility"}}}`, nil
		}
		return respond(request, index)
	}
	worker := New(store, model, registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxSearchQueries: 4, MaxFetchBytes: taskBudget})
	worker.runTask(ctx, claimed)
	current, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusCompleted {
		t.Errorf("source-only PDF rejection stopped research despite two valid HTML alternates: status=%s", current.Status)
	}
	for _, rawURL := range []string{pdfURL, referenceURL, alternateURL} {
		if fetcher.calls[rawURL] != 1 {
			t.Errorf("candidate must be attempted exactly once: url=%s calls=%d", rawURL, fetcher.calls[rawURL])
		}
	}
	evidence, err := store.Evidence(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 {
		t.Errorf("evidence=%d, want two accepted HTML sources", len(evidence))
	}
	for _, item := range evidence {
		if item.OriginalURL == pdfURL || item.CanonicalURL == pdfURL {
			t.Errorf("rejected PDF became evidence: %s", item.ID)
		}
	}
	state, err := worker.currentCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(len(fetcher.bodies[referenceURL]) + len(fetcher.bodies[alternateURL]))
	if state.FetchedBytes != wantBytes || state.FetchedBytes >= taskBudget {
		t.Errorf("header-only PDF rejection must consume zero bytes: fetched=%d want=%d task_budget=%d", state.FetchedBytes, wantBytes, taskBudget)
	}
	if !contains(state.FailedFetchURLs, pdfURL) || !contains(state.RejectedFetchURLs, pdfURL) || state.PendingTool != "" {
		t.Errorf("terminal PDF rejection missing from checkpoint: failed=%v rejected=%v pending=%q", state.FailedFetchURLs, state.RejectedFetchURLs, state.PendingTool)
	}
	if !state.Partial || len(state.SourceFailures) != 1 {
		t.Errorf("source failure not persisted as partial metadata: partial=%t failures=%v", state.Partial, state.SourceFailures)
	}
	storedArtifacts, err := store.Artifacts(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedArtifacts) != 4 {
		t.Errorf("artifacts=%d, want only raw/text artifacts for two HTML sources", len(storedArtifacts))
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 300)
	if err != nil {
		t.Fatal(err)
	}
	auditFailures, publicFailures := 0, 0
	warnings := 0
	var attemptID string
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if event.Type == "task.failed" {
			t.Errorf("source oversize must not become aggregate/generic task failure: %s", event.Payload)
		}
		if event.Type == "research.warning" && payload["partial"] == true {
			warnings++
		}
		if event.Type != "tool.failed" || payload["tool"] != "fetch_url" {
			continue
		}
		code, _ := payload["error_code"].(string)
		if code == "" {
			code, _ = payload["code"].(string)
		}
		if code == "" {
			code, _ = payload["failure"].(string)
		}
		if code != "pdf_source_too_large" {
			t.Errorf("PDF rejection lost source-level reason: %s", event.Payload)
		}
		if payload["declared_bytes"] != float64(265) || payload["effective_limit_bytes"] != float64(256) || payload["mime_type"] != "application/pdf" || payload["remaining_candidates"] != float64(2) || payload["retryable"] != false {
			t.Errorf("incorrect PDF size/fallback metadata: %s", event.Payload)
		}
		id, _ := payload["attempt_id"].(string)
		if id == "" || attemptID != "" && attemptID != id {
			t.Errorf("failure events must share attempt_id: %s", event.Payload)
		}
		attemptID = id
		if payload["step_id"] != "fetch_1" || payload["target_url"] != pdfURL || payload["fetched_bytes"] != float64(0) || payload["try_next_source"] != true {
			t.Errorf("PDF failure lifecycle metadata missing: %s", event.Payload)
		}
		if event.Visibility == "audit" {
			auditFailures++
		}
		if event.Visibility == "user" {
			publicFailures++
		}
	}
	if auditFailures != 1 || publicFailures != 1 {
		t.Errorf("PDF tool.started needs terminal source failure: audit=%d user=%d", auditFailures, publicFailures)
	}
	if warnings != 1 {
		t.Errorf("partial completion warning count=%d", warnings)
	}
}
