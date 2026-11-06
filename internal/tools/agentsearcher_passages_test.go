package tools

import (
	"context"
	"encoding/json"
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
