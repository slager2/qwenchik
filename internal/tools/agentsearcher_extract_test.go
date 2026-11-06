package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type pageExtractorStub struct {
	page    research.AgentSearcherPage
	err     error
	calls   int
	queries []string
}

func (s *pageExtractorStub) Extract(_ context.Context, rawURL, query string) (research.AgentSearcherPage, error) {
	s.calls++
	s.queries = append(s.queries, query)
	if s.err != nil {
		return research.AgentSearcherPage{}, s.err
	}
	page := s.page
	page.URL = rawURL
	return page, nil
}

func (*pageExtractorStub) Budget() time.Duration { return 60 * time.Second }

type unavailablePDFExtractor struct{ passagePageExtractor }

func (unavailablePDFExtractor) PreflightPDF(context.Context) error {
	return research.ErrPDFExtractorUnavailable
}

type extractFixture struct {
	registry  *Registry
	store     *sqlite.Store
	artifacts *artifacts.Store
	ctx       context.Context
}

func newExtractTestRegistry(t *testing.T, fetcher research.Fetcher, extractor research.Extractor, pages PageExtractor, allowed []string, hits validationSearchHits) extractFixture {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "extract.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.CreateTask(ctx, domain.Task{ID: "extract-task", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{
		SearchProvider: hits, Fetcher: fetcher, Extractor: extractor, Artifacts: artifactStore,
		Repository: store, MaxFetchBytes: 1 << 20, PageExtractor: pages, AllowedDomains: allowed,
	}); err != nil {
		t.Fatal(err)
	}
	toolCtx := WithTaskID(ctx, "extract-task")
	if _, err := registry.Execute(toolCtx, "researcher", "search", json.RawMessage(`{"query":"attention heads"}`)); err != nil {
		t.Fatal(err)
	}
	return extractFixture{registry: registry, store: store, artifacts: artifactStore, ctx: toolCtx}
}

func extractPageText() string {
	filler := strings.Repeat("Site navigation, cookie banner and related links.\n", 150)
	return filler + "The Transformer employs h = 8 parallel attention layers, or heads.\n" + filler
}

func TestFetchReadsPageThroughAgentSearcherExtractFirst(t *testing.T) {
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://protected.example.org/paper", Title: "Paper", Snippet: "search passage"}}
	text := extractPageText()
	pages := &pageExtractorStub{page: research.AgentSearcherPage{
		Text: text, Chunks: []string{"The Transformer employs h = 8 parallel attention layers, or heads."},
		TextResponse: json.RawMessage(`{"results":[{"url":"https://protected.example.org/paper","raw_content":"…"}],"failed_results":[]}`),
	}}
	fetcher := &fetchStub{}
	fixture := newExtractTestRegistry(t, fetcher, passagePageExtractor{}, pages, nil, hits)
	result, err := fixture.registry.Execute(fixture.ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://protected.example.org/paper","original_query":"attention heads"}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if fetcher.calls != 0 || pages.calls != 1 || pages.queries[0] != "attention heads" {
		t.Fatalf("direct fetch calls=%d extract calls=%d queries=%v", fetcher.calls, pages.calls, pages.queries)
	}
	if output["extraction_method"] != "agentsearcher_extract" || output["fetched_bytes"] != int64(len(text)) || output["title"] != "Paper" || output["source_text"] != nil {
		t.Fatalf("output = %#v", output)
	}
	evidence, err := fixture.store.Evidence(context.Background(), "extract-task")
	if err != nil || len(evidence) != 1 {
		t.Fatalf("evidence=%#v err=%v", evidence, err)
	}
	item := evidence[0]
	if item.Provider != "agentsearcher" || item.ExtractionMethod != "agentsearcher_extract" || item.HTTPStatus != 200 || len(item.QualityFlags) != 0 {
		t.Fatalf("provenance = %#v", item)
	}
	stored, err := fixture.artifacts.Read(context.Background(), "extract-task", item.TextArtifactID)
	if err != nil || string(stored) != text {
		t.Fatalf("text artifact differs from page text: err=%v", err)
	}
	span := item.QuoteSpans[0]
	if !strings.Contains(item.Excerpt, "h = 8 parallel attention layers") || strings.Count(item.Excerpt, "cookie banner") > 20 || string([]rune(text)[span.Start:span.End]) != item.Excerpt {
		t.Fatalf("excerpt %q span %#v", item.Excerpt, span)
	}
	raw, err := fixture.artifacts.Read(context.Background(), "extract-task", item.RawArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil || record["provider"] != "agentsearcher" || record["endpoint"] != "/extract" || record["text_response"] == nil || hex.EncodeToString(hash[:]) != item.ContentSHA256 {
		t.Fatalf("raw record = %s err=%v", raw, err)
	}
	if !fixture.registry.WasTaskURLFetched("extract-task", "https://protected.example.org/paper") {
		t.Fatal("extracted source was not marked fetched")
	}
}

func TestFetchReadsPDFThroughAgentSearcherWithoutLocalPDFExtractor(t *testing.T) {
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://arxiv.org/pdf/1706.03762.pdf", Title: "Attention Is All You Need", Snippet: "passage"}}
	pages := &pageExtractorStub{page: research.AgentSearcherPage{Text: "Page 1\n\nAttention Is All You Need\n\nPage 2\n\nWe employ eight heads.", PDF: true, TextResponse: json.RawMessage(`{}`)}}
	fixture := newExtractTestRegistry(t, &fetchStub{}, unavailablePDFExtractor{}, pages, nil, hits)
	result, err := fixture.registry.Execute(fixture.ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://arxiv.org/pdf/1706.03762.pdf"}`))
	if err != nil {
		t.Fatalf("PDF preflight blocked agentSearcher: %v", err)
	}
	if output := result.(map[string]any); output["extraction_method"] != "agentsearcher_extract_pdf" {
		t.Fatalf("output = %#v", output)
	}
}

func TestFetchFallsBackToDirectFetchWhenAgentSearcherExtractFails(t *testing.T) {
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://docs.example.org/roadmap", Title: "Roadmap", Snippet: "passage"}}
	pages := &pageExtractorStub{err: &research.AgentSearcherExtractError{Code: "extract_source_unavailable", Reason: "HTTP 403"}}
	fetcher := &fetchStub{}
	fixture := newExtractTestRegistry(t, fetcher, passagePageExtractor{}, pages, nil, hits)
	result, err := fixture.registry.Execute(fixture.ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.org/roadmap"}`))
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.calls != 1 || pages.calls != 1 || result.(map[string]any)["extraction_method"] != nil {
		t.Fatalf("calls direct=%d extract=%d output=%#v", fetcher.calls, pages.calls, result)
	}
	evidence, _ := fixture.store.Evidence(context.Background(), "extract-task")
	if len(evidence) != 1 || evidence[0].Provider != "direct_http" {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestFetchFallsBackToSearchPassagesWhenExtractAndDirectFetchFail(t *testing.T) {
	passages := "Bioinformatics roadmap: learn Python, statistics and sequence alignment first."
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://blocked.example.org/roadmap", Title: "Blocked", Snippet: passages}}
	blocked := &research.FetchFailure{Err: fmt.Errorf("%w: HTTP 403", research.ErrFetchSourceUnavailable), Code: "source_unavailable", Phase: "fetch"}
	pages := &pageExtractorStub{page: research.AgentSearcherPage{Text: " \n"}}
	fixture := newExtractTestRegistry(t, &blockedFetchStub{err: blocked}, passagePageExtractor{}, pages, nil, hits)
	result, err := fixture.registry.Execute(fixture.ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://blocked.example.org/roadmap"}`))
	if err != nil {
		t.Fatal(err)
	}
	if output := result.(map[string]any); output["source_text"] != "agentsearcher_passages" || pages.calls != 1 {
		t.Fatalf("output = %#v", output)
	}
}

func TestFetchExtractRespectsTaskFetchBudget(t *testing.T) {
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://big.example.org/page", Title: "Big", Snippet: "passage"}}
	pages := &pageExtractorStub{page: research.AgentSearcherPage{Text: strings.Repeat("Long page text. ", 200), TextResponse: json.RawMessage(`{}`)}}
	fetcher := &fetchStub{}
	fixture := newExtractTestRegistry(t, fetcher, passagePageExtractor{}, pages, nil, hits)
	ctx := WithFetchByteLimit(fixture.ctx, 1000)
	_, err := fixture.registry.Execute(ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://big.example.org/page"}`))
	var failure *research.FetchFailure
	if !errors.As(err, &failure) || failure.Code != "task_fetch_budget_exceeded" || failure.FetchedBytes != 3200 || fetcher.calls != 0 {
		t.Fatalf("err=%v failure=%#v direct calls=%d", err, failure, fetcher.calls)
	}
}

func TestFetchSkipsAgentSearcherExtractForUnsafeOrAllowListedTargets(t *testing.T) {
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://docs.example.org/roadmap", Title: "Roadmap", Snippet: "passage"}}
	pages := &pageExtractorStub{page: research.AgentSearcherPage{Text: "text", TextResponse: json.RawMessage(`{}`)}}
	fetcher := &fetchStub{}
	fixture := newExtractTestRegistry(t, fetcher, passagePageExtractor{}, pages, []string{"example.org"}, hits)
	if _, err := fixture.registry.Execute(fixture.ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.org/roadmap"}`)); err != nil {
		t.Fatal(err)
	}
	if pages.calls != 0 || fetcher.calls != 1 {
		t.Fatalf("allow-listed fetch used agentSearcher: extract=%d direct=%d", pages.calls, fetcher.calls)
	}

	// fetchStub's URL policy rejects plain HTTP; agentSearcher must not be
	// asked for a URL the backend policy refuses.
	unsafe := newExtractTestRegistry(t, &fetchStub{}, passagePageExtractor{}, pages, nil, validationSearchHits{})
	unsafe.registry.urlLedger.Add("extract-task", "http://plain.example.org/admin")
	if _, err := unsafe.registry.Execute(unsafe.ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"http://plain.example.org/admin"}`)); !errors.Is(err, research.ErrUnsafeURL) {
		t.Fatalf("unsafe URL error = %v", err)
	}
	if pages.calls != 0 {
		t.Fatalf("agentSearcher was asked for an unsafe URL: %d calls", pages.calls)
	}
}
