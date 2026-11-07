package orchestrator

import (
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestBalancedSearchResultURLsSpreadsFetchesAcrossFacets(t *testing.T) {
	hits := func(urls ...string) []research.SearchHit {
		out := []research.SearchHit{}
		for _, u := range urls {
			out = append(out, research.SearchHit{URL: u})
		}
		return out
	}
	results := map[string]any{
		"search_1": hits("https://hu.example/a", "https://hu.example/b", "https://hu2.example/c"),
		"search_2": hits("https://tr.example/a", "https://tr.example/b"),
		"search_3": hits("https://kr.example/a"),
		"fetch_1":  map[string]any{"evidence_id": "x"},
	}
	fetched := map[string]struct{}{"https://hu.example/a": {}}
	got := balancedSearchResultURLs(results, fetched)
	want := []string{"https://tr.example/a", "https://kr.example/a", "https://hu.example/a", "https://tr.example/b", "https://hu.example/b", "https://hu2.example/c"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}
