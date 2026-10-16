package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type searchStub struct {
	url     string
	options *research.SearchOptions
}

func (s searchStub) Search(_ context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	if s.options != nil {
		*s.options = options
	}
	return []research.SearchHit{{Provider: "searxng", Query: options.Query, URL: s.url, Title: "A source", Snippet: "snippet", Taint: research.TaintExternal}}, nil
}

type fetchStub struct{ calls int }

func (*fetchStub) ValidateURL(_ context.Context, rawURL string) error {
	if strings.HasPrefix(rawURL, "https://") {
		return nil
	}
	return research.ErrUnsafeURL
}

func (f *fetchStub) Fetch(_ context.Context, rawURL string, _ research.FetchOptions) (research.FetchedDocument, error) {
	f.calls++
	return research.FetchedDocument{
		Provider: "direct_http", OriginalURL: rawURL, FinalURL: rawURL, ContentType: "text/html",
		HTTPStatus: 200, RetrievedAt: time.Now().UTC(), ContentSHA256: "test-content-hash", Body: []byte("<html>raw</html>"),
	}, nil
}

type extractorStub struct{}

func (extractorStub) Extract(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
	return research.ExtractedDocument{Title: "Fetched source", Text: "Verified excerpt from the public page.", Method: "test_html", Taint: research.TaintExternal}, nil
}

type pageCountExtractorStub struct{ pageCount int }

func (e pageCountExtractorStub) Extract(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
	return research.ExtractedDocument{Text: "Extracted PDF evidence.", Method: "test_pdf", Taint: research.TaintExternal, PageCount: e.pageCount}, nil
}

type largeExtractorStub struct{}

func (largeExtractorStub) Extract(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
	return research.ExtractedDocument{
		Title: "Large source", Text: strings.Repeat("Проверяемые результаты содержат фактические данные. ", 180),
		Method: "test_html", Taint: research.TaintExternal,
	}, nil
}

type pdfFetchStub struct{}

func (pdfFetchStub) ValidateURL(context.Context, string) error { return nil }

func (pdfFetchStub) Fetch(_ context.Context, rawURL string, _ research.FetchOptions) (research.FetchedDocument, error) {
	return research.FetchedDocument{
		Provider: "direct_http", OriginalURL: rawURL, FinalURL: rawURL, ContentType: "application/pdf",
		HTTPStatus: 200, ContentSHA256: "pdf-content-hash", Body: []byte("%PDF-1.7 untrusted input"),
	}, nil
}

type pdfExtractorStub struct {
	result pdfextract.Result
	err    error
}

func (e pdfExtractorStub) Extract(context.Context, []byte) (pdfextract.Result, error) {
	return e.result, e.err
}

func TestResearchToolsRequireKnownURLAndPersistEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateTask(ctx, domain.Task{ID: "research-task", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fetchStub{}
	var capturedSearch research.SearchOptions
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{
		SearchProvider: searchStub{url: "https://docs.example.com/guide", options: &capturedSearch},
		Fetcher:        fetcher, Extractor: extractorStub{}, Artifacts: artifactStore,
		Repository: store, MaxFetchBytes: 1024, AllowedDomains: []string{"example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	toolContext := WithTaskID(ctx, "research-task")
	searchResult, err := registry.Execute(toolContext, "researcher", "search", json.RawMessage(`{"query":"official protocol transport documentation","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(searchResult.([]research.SearchHit)) != 1 {
		t.Fatalf("search result = %#v", searchResult)
	}
	if len(capturedSearch.PreferredDomains) != 0 || len(capturedSearch.Domains) != 1 || capturedSearch.Domains[0] != "example.com" {
		t.Fatalf("search did not preserve the hard allowlist without injecting topic-specific preferences: %#v", capturedSearch)
	}
	if _, err := registry.Execute(toolContext, "researcher", "search", json.RawMessage(`{"query":"local Qwen","domains":["example.com"]}`)); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("model-supplied domain filter error = %v, want ErrInvalidArguments", err)
	}
	if _, err := registry.Execute(WithFetchByteLimit(toolContext, 2), "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/guide","original_query":"local Qwen"}`)); !errors.Is(err, research.ErrResponseTooBig) {
		t.Fatalf("task fetch byte budget error = %v", err)
	}
	fetched, err := registry.Execute(toolContext, "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/guide","original_query":"local Qwen"}`))
	if err != nil {
		t.Fatal(err)
	}
	metadata := fetched.(map[string]any)
	if metadata["evidence_id"] == "" || metadata["taint"] != research.TaintExternal || metadata["requested_url"] != "https://docs.example.com/guide" || fetcher.calls != 2 {
		t.Fatalf("unexpected fetch metadata %#v; fetch calls=%d", metadata, fetcher.calls)
	}
	if _, exists := metadata["page_count"]; exists {
		t.Fatalf("fetch output included page_count without extractor page metadata: %#v", metadata)
	}
	evidence, err := store.Evidence(ctx, "research-task")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Title != "Fetched source" || evidence[0].RawArtifactID == "" || evidence[0].TextArtifactID == "" {
		t.Fatalf("evidence not persisted with artifacts: %#v", evidence)
	}
	if _, err := registry.Execute(toolContext, "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/guide","original_query":"local Qwen"}`)); !errors.Is(err, ErrDuplicateResearchTarget) {
		t.Fatalf("duplicate fetch error = %v, want ErrDuplicateResearchTarget", err)
	}
	if _, err := registry.Execute(toolContext, "researcher", "fetch_url", json.RawMessage(`{"url":"https://unknown.example/secret"}`)); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("unsearched URL error = %v, want ErrUnknownTarget", err)
	}
	if fetcher.calls != 2 {
		t.Fatalf("unsearched URL reached fetcher, calls=%d", fetcher.calls)
	}
}

func TestFetchToolReturnsFourThousandRuneEvidenceExcerpt(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "large-research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateTask(ctx, domain.Task{ID: "large-research-task", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "large-artifacts"), 100_000)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{
		Fetcher: &fetchStub{}, Extractor: largeExtractorStub{}, Artifacts: artifactStore, Repository: store, MaxFetchBytes: 100_000,
	}); err != nil {
		t.Fatal(err)
	}
	registry.RememberURLs("large-research-task", []research.SearchHit{{URL: "https://docs.example.com/report"}})
	result, err := registry.Execute(WithTaskID(ctx, "large-research-task"), "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/report","original_query":"verified results factual data"}`))
	if err != nil {
		t.Fatal(err)
	}
	metadata := result.(map[string]any)
	excerpt, _ := metadata["excerpt"].(string)
	if got := utf8.RuneCountInString(excerpt); got != 4000 {
		t.Fatalf("fetch output excerpt has %d runes, want 4000", got)
	}
	evidence, err := store.Evidence(ctx, "large-research-task")
	if err != nil || len(evidence) != 1 || utf8.RuneCountInString(evidence[0].Excerpt) != 4000 {
		t.Fatalf("persisted large evidence excerpt = %#v err=%v", evidence, err)
	}
}

func TestFailedPDFExtractionPersistsNoArtifactOrEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "failed-pdf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const taskID = "failed-pdf-task"
	if _, err := store.CreateTask(ctx, domain.Task{ID: taskID, Title: "Fetch a PDF"}, ""); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "failed-pdf-artifacts"), 100_000)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{
		Fetcher: pdfFetchStub{}, Extractor: research.NewHTMLExtractorWithPDF(100_000, pdfExtractorStub{err: pdfextract.ErrTooManyPages}),
		Artifacts: artifactStore, Repository: store, MaxFetchBytes: 100_000,
	}); err != nil {
		t.Fatal(err)
	}
	registry.RememberURLs(taskID, []research.SearchHit{{URL: "https://docs.example.com/long-report.pdf"}})
	_, err = registry.Execute(WithTaskID(ctx, taskID), "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/long-report.pdf","original_query":"official PDF report"}`))
	if !errors.Is(err, research.ErrPDFTooManyPages) {
		t.Fatalf("over-page-limit extraction error = %v", err)
	}
	artifacts, err := store.Artifacts(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.Evidence(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 || len(evidence) != 0 {
		t.Fatalf("failed PDF extraction persisted partial output: artifacts=%#v evidence=%#v", artifacts, evidence)
	}
}

func TestFetchToolIncludesPageCountWhenExtractorProvidesIt(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "pdf-research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateTask(ctx, domain.Task{ID: "pdf-research-task", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "pdf-artifacts"), 100_000)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{
		Fetcher: &fetchStub{}, Extractor: pageCountExtractorStub{pageCount: 3}, Artifacts: artifactStore,
		Repository: store, MaxFetchBytes: 100_000,
	}); err != nil {
		t.Fatal(err)
	}
	registry.RememberURLs("pdf-research-task", []research.SearchHit{{URL: "https://docs.example.com/report.pdf"}})
	result, err := registry.Execute(WithTaskID(ctx, "pdf-research-task"), "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/report.pdf"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["page_count"]; got != 3 {
		t.Fatalf("fetch output page_count = %#v, want 3", got)
	}
}
