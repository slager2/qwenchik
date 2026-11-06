package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type wideSearchStub struct {
	options *research.SearchOptions
	snippet string
}

func (s wideSearchStub) Search(_ context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	if s.options != nil {
		*s.options = options
	}
	hits := make([]research.SearchHit, 0, options.MaxResults)
	for i := 0; i < options.MaxResults; i++ {
		hits = append(hits, research.SearchHit{Provider: "agentsearcher", Query: options.Query, URL: fmt.Sprintf("https://site%d.example.org/page", i), Title: "Page", Snippet: s.snippet, Taint: research.TaintExternal})
	}
	return hits, nil
}

const passagePageArticle = "A practical bioinformatics roadmap starts with Python and R, then moves to sequence alignment with BLAST. Next, learners study genome assembly, variant calling and RNA-seq differential expression."

func passagePageText() string {
	boilerplate := strings.Repeat("Email Bluesky Facebook LinkedIn Reddit Share\nFact-checked by Editorial Team about bioinformatics roadmap\n", 40)
	return boilerplate + passagePageArticle + "\n" + boilerplate
}

type passagePageExtractor struct{}

func (passagePageExtractor) Extract(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
	return research.ExtractedDocument{Title: "Roadmap", Text: passagePageText(), Method: "test_html", Taint: research.TaintExternal}, nil
}

func newPassageTestRegistry(t *testing.T, fetcher research.Fetcher, hits validationSearchHits) (*Registry, *sqlite.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "passages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.CreateTask(ctx, domain.Task{ID: "passage-task", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{
		SearchProvider: hits, Fetcher: fetcher, Extractor: passagePageExtractor{}, Artifacts: artifactStore,
		Repository: store, MaxFetchBytes: 1 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	toolCtx := WithTaskID(ctx, "passage-task")
	if _, err := registry.Execute(toolCtx, "researcher", "search", json.RawMessage(`{"query":"bioinformatics roadmap"}`)); err != nil {
		t.Fatal(err)
	}
	return registry, store, toolCtx
}

func TestFetchLocatesExcerptWithAgentSearcherPassages(t *testing.T) {
	passage := "a practical bioinformatics roadmap starts with Python and R, then moves to sequence alignment with BLAST … learners study genome assembly, variant calling and RNA-seq differential expression"
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://docs.example.org/roadmap", Title: "Roadmap", Snippet: passage}}
	registry, store, ctx := newPassageTestRegistry(t, &fetchStub{}, hits)
	if got := registry.urlLedger.Passages("passage-task", "https://docs.example.org/roadmap"); len(got) != 1 || got[0] != passage {
		t.Fatalf("ledger passages = %#v", got)
	}
	result, err := registry.Execute(ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.org/roadmap","original_query":"bioinformatics roadmap"}`))
	if err != nil {
		t.Fatal(err)
	}
	excerpt, _ := result.(map[string]any)["excerpt"].(string)
	if excerpt != passagePageArticle {
		t.Fatalf("excerpt = %q", excerpt)
	}
	evidence, err := store.Evidence(context.Background(), "passage-task")
	if err != nil || len(evidence) != 1 {
		t.Fatalf("evidence=%#v err=%v", evidence, err)
	}
	span := evidence[0].QuoteSpans[0]
	if string([]rune(passagePageText())[span.Start:span.End]) != evidence[0].Excerpt || evidence[0].Provider != "direct_http" {
		t.Fatalf("span %#v does not slice the stored text", span)
	}
}

type blockedFetchStub struct {
	fetchStub
	err error
}

func (f *blockedFetchStub) Fetch(context.Context, string, research.FetchOptions) (research.FetchedDocument, error) {
	f.calls++
	return research.FetchedDocument{}, f.err
}

func TestFetchFallsBackToAgentSearcherPassagesWhenBlocked(t *testing.T) {
	passages := "Bioinformatics roadmap: learn Python, statistics and sequence alignment first.\n\nThen practise genome assembly and RNA-seq analysis on public datasets."
	hits := validationSearchHits{
		{Provider: "agentsearcher", URL: "https://blocked.example.org/roadmap", Title: "Blocked roadmap", Snippet: passages},
		{Provider: "searxng", URL: "https://engine.example.org/page", Title: "Engine hit", Snippet: "short engine snippet"},
	}
	blocked := &research.FetchFailure{Err: fmt.Errorf("%w: HTTP 403", research.ErrFetchSourceUnavailable), Code: "source_unavailable", Phase: "fetch", FetchedBytes: 120}
	registry, store, ctx := newPassageTestRegistry(t, &blockedFetchStub{err: blocked}, hits)
	result, err := registry.Execute(ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://blocked.example.org/roadmap","original_query":"bioinformatics roadmap"}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["source_text"] != "agentsearcher_passages" || output["direct_fetch_error"] != "source_unavailable" || output["fetched_bytes"] != int64(120) || output["title"] != "Blocked roadmap" {
		t.Fatalf("fallback output = %#v", output)
	}
	evidence, err := store.Evidence(context.Background(), "passage-task")
	if err != nil || len(evidence) != 1 {
		t.Fatalf("evidence=%#v err=%v", evidence, err)
	}
	item := evidence[0]
	if item.Provider != "agentsearcher" || item.ExtractionMethod != "agentsearcher_passages" || item.HTTPStatus != 200 || len(item.QualityFlags) != 0 || item.RawArtifactID == "" || item.TextArtifactID == "" || item.ContentSHA256 == "" {
		t.Fatalf("passage evidence provenance = %#v", item)
	}
	span := item.QuoteSpans[0]
	if item.Excerpt != passages || string([]rune(passages)[span.Start:span.End]) != item.Excerpt {
		t.Fatalf("passage excerpt %q span %#v", item.Excerpt, span)
	}
	if !registry.WasTaskURLFetched("passage-task", "https://blocked.example.org/roadmap") {
		t.Fatal("fallback source was not marked fetched")
	}
	// Engine snippets are not page text: no fallback for them.
	if _, err := registry.Execute(ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://engine.example.org/page"}`)); !errors.Is(err, research.ErrFetchSourceUnavailable) {
		t.Fatalf("engine-snippet URL error = %v, want the direct fetch failure", err)
	}
}

func TestFetchDoesNotFallBackForPolicyFailures(t *testing.T) {
	hits := validationSearchHits{{Provider: "agentsearcher", URL: "https://big.example.org/file", Title: "Big", Snippet: "Long enough agentSearcher passage about the page content."}}
	tooBig := &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "source_response_too_large", Phase: "fetch"}
	registry, _, ctx := newPassageTestRegistry(t, &blockedFetchStub{err: tooBig}, hits)
	if _, err := registry.Execute(ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://big.example.org/file"}`)); !errors.Is(err, research.ErrResponseTooBig) {
		t.Fatalf("policy failure error = %v", err)
	}
}

func TestSearchAllowsWideAgentSearcherResultSetsWithinOutputLimit(t *testing.T) {
	var captured research.SearchOptions
	passage := strings.Repeat("Биоинформатика изучает последовательности ДНК и белков. ", 40)
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: wideSearchStub{options: &captured, snippet: passage}, Fetcher: &fetchStub{}}); err != nil {
		t.Fatal(err)
	}
	ctx := WithTaskID(context.Background(), "wide-search")
	result, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"bioinformatics roadmap","max_results":15}`))
	if err != nil {
		t.Fatal(err)
	}
	hits := result.([]research.SearchHit)
	if captured.MaxResults != 15 || len(hits) != 15 {
		t.Fatalf("max_results=%d hits=%d, want 15", captured.MaxResults, len(hits))
	}
	encoded, _ := json.Marshal(hits)
	if len(encoded) > searchOutputBytes || hits[0].Snippet == "" || !strings.HasPrefix(passage, hits[0].Snippet) {
		t.Fatalf("output %d bytes (limit %d), snippet %q", len(encoded), searchOutputBytes, hits[0].Snippet)
	}
	if _, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"bioinformatics","max_results":16}`)); err == nil {
		t.Fatal("max_results above the cap was accepted")
	}
	if _, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"genomics"}`)); err != nil || captured.MaxResults != DefaultSearchResults {
		t.Fatalf("default max_results=%d err=%v", captured.MaxResults, err)
	}
}
