package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
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
