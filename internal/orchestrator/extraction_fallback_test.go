package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type extractionFallbackProvider struct{}

func (extractionFallbackProvider) Search(_ context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	return []research.SearchHit{
		{Provider: "searxng", Query: options.Query, URL: "https://docs.example.com/protocol", Title: "Specification"},
		{Provider: "searxng", Query: options.Query, URL: "https://reference.example.net/protocol", Title: "Reference"},
		{Provider: "searxng", Query: options.Query, URL: "https://benchmark.example.org/protocol", Title: "Compatibility"},
	}, nil
}

type extractionFallbackFetcher struct {
	bodies map[string]string
	calls  map[string]int
}

func (f *extractionFallbackFetcher) ValidateURL(ctx context.Context, rawURL string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, ok := f.bodies[rawURL]; !ok {
		return research.ErrUnsafeURL
	}
	return nil
}

func (f *extractionFallbackFetcher) Fetch(ctx context.Context, rawURL string, options research.FetchOptions) (research.FetchedDocument, error) {
	if err := f.ValidateURL(ctx, rawURL); err != nil {
		return research.FetchedDocument{}, err
	}
	f.calls[rawURL]++
	body := []byte(f.bodies[rawURL])
	if options.MaxBytes < int64(len(body)) {
		return research.FetchedDocument{}, research.ErrResponseTooBig
	}
	hash := sha256.Sum256(body)
	return research.FetchedDocument{Provider: "fixture", OriginalURL: rawURL, FinalURL: rawURL, HTTPStatus: 200, ContentType: "text/html", ContentSHA256: hex.EncodeToString(hash[:]), RetrievedAt: time.Now().UTC(), Body: body}, nil
}

type extractionFallbackExtractor struct {
	delegate *research.HTMLExtractor
	failures map[string]error
}

func (e *extractionFallbackExtractor) Extract(ctx context.Context, document research.FetchedDocument) (research.ExtractedDocument, error) {
	result, err := e.delegate.Extract(ctx, document)
	if err != nil {
		e.failures[document.OriginalURL] = err
	}
	return result, err
}

func TestExtractionFallbackRejectsBadSourceAndChargesAllBytes(t *testing.T) {
	for _, fixture := range []struct {
		name, body, code string
		cause            error
	}{
		{"empty", "<html><nav>navigation</nav><script>ignored</script></html>", "extraction_empty", research.ErrExtractionEmpty},
		{"challenge", `<html><title>Just a moment...</title><form id="challenge-form">Verify you are human</form></html>`, "extraction_challenge", research.ErrExtractionChallenge},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, err := sqlite.Open(ctx, t.TempDir()+"/extraction.db")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			artifactStore, err := artifacts.New(t.TempDir(), 1_000_000)
			if err != nil {
				t.Fatal(err)
			}
			badURL := "https://docs.example.com/protocol"
			fetcher := &extractionFallbackFetcher{bodies: map[string]string{
				badURL:                                   fixture.body,
				"https://reference.example.net/protocol": "<title>Reference</title><main>The protocol uses framed messages and supports independent implementations.</main>",
				"https://benchmark.example.org/protocol": "<title>Compatibility</title><main>Interoperability requires compatible framing and supported protocol versions.</main>",
			}, calls: map[string]int{}}
			var totalBytes int64
			for _, body := range fetcher.bodies {
				totalBytes += int64(len(body))
			}
			extractor := &extractionFallbackExtractor{delegate: research.NewHTMLExtractorWithPDF(10_000, nil), failures: map[string]error{}}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: extractionFallbackProvider{}, Fetcher: fetcher, Extractor: extractor, Artifacts: artifactStore, Repository: store, MaxFetchBytes: totalBytes}); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(ctx, domain.Task{ID: "extraction-fallback", Title: "Protocol review", Description: "Research protocol architecture and limitations."}, "")
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
					return `{"status":"continue","summary":"Inspect fresh compatibility reference","tool_call":{"name":"fetch_url","arguments":{"url":"https://benchmark.example.org/protocol","original_query":"protocol compatibility"}}}`, nil
				}
				return respond(request, index)
			}
			worker := New(store, model, registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxSearchQueries: 4, MaxFetchBytes: totalBytes})
			worker.runTask(ctx, claimed)
			current, err := store.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != domain.StatusCompleted {
				t.Fatalf("fallback did not complete with two usable sources: status=%s", current.Status)
			}
			if !errors.Is(extractor.failures[badURL], fixture.cause) {
				t.Fatalf("actual extractor returned %v, want %v", extractor.failures[badURL], fixture.cause)
			}
			evidence, err := store.Evidence(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(evidence) != 2 {
				t.Fatalf("evidence count=%d, want two valid sources", len(evidence))
			}
			for _, item := range evidence {
				if item.OriginalURL == badURL || item.CanonicalURL == badURL || strings.TrimSpace(item.Excerpt) == "" {
					t.Fatalf("rejected source created evidence: %#v", item)
				}
			}
			for rawURL := range fetcher.bodies {
				if fetcher.calls[rawURL] != 1 {
					t.Fatalf("candidate was retried or skipped: %s calls=%d", rawURL, fetcher.calls[rawURL])
				}
			}
			state, err := worker.currentCheckpoint(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.FetchedBytes != totalBytes || !contains(state.RejectedFetchURLs, badURL) || !contains(state.FailedFetchURLs, badURL) {
				t.Fatalf("failure accounting not persisted: bytes=%d want=%d rejected=%v failed=%v", state.FetchedBytes, totalBytes, state.RejectedFetchURLs, state.FailedFetchURLs)
			}
			remaining := filterFetchURLs([]string{badURL, "https://docs.example.com:443/protocol#retry"}, map[string]struct{}{}, state.RejectedFetchURLs)
			if len(remaining) != 0 {
				t.Fatalf("rejected source became eligible for retry: %v", remaining)
			}
			storedArtifacts, err := store.Artifacts(ctx, task.ID)
			if err != nil || len(storedArtifacts) != 4 {
				t.Fatalf("rejected source created artifacts: count=%d err=%v", len(storedArtifacts), err)
			}
			events, err := store.EventsAfter(ctx, task.ID, 0, 300)
			if err != nil {
				t.Fatal(err)
			}
			auditFailures, publicFailures, validFetches := 0, 0, 0
			for _, event := range events {
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if event.Type == "task.failed" {
					t.Fatalf("false terminal failure: %s", event.Payload)
				}
				if event.Type == "tool.failed" && event.Visibility == "audit" && payload["failure"] == fixture.code {
					auditFailures++
					if payload["target_url"] != badURL || payload["fetched_bytes"] != float64(len(fixture.body)) {
						t.Fatalf("incorrect rejected byte charge: %s", event.Payload)
					}
				}
				if event.Type == "tool.failed" && event.Visibility == "user" && payload["code"] == fixture.code {
					publicFailures++
					if payload["retryable"] != false {
						t.Fatalf("rejected source marked retryable: %s", event.Payload)
					}
				}
				if event.Type == "tool.completed" && payload["tool"] == "fetch_url" {
					validFetches++
					result := payload["result"].(map[string]any)
					if result["requested_url"] == badURL {
						t.Fatal("rejected source produced successful fetch result")
					}
				}
			}
			if auditFailures != 1 || publicFailures != 1 || validFetches != 2 {
				t.Fatalf("failure lifecycle missing: audit=%d public=%d valid_fetches=%d", auditFailures, publicFailures, validFetches)
			}
		})
	}
}
