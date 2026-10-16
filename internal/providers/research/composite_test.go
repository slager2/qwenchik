package research

import (
	"context"
	"errors"
	"testing"
)

type compositeSearchStub struct {
	hits []SearchHit
	err  error
}

func (stub compositeSearchStub) Search(context.Context, SearchOptions) ([]SearchHit, error) {
	return stub.hits, stub.err
}

func TestCompositeSearchDeduplicatesFiltersAndCapsRoundRobinResults(t *testing.T) {
	providerA := compositeSearchStub{hits: []SearchHit{
		{Provider: "searxng", URL: "https://EXAMPLE.org:443/path?utm_source=one#top", Title: "first"},
		{Provider: "searxng", URL: "https://example.org/second", Title: "second"},
		{Provider: "searxng", URL: "https://outside.example/skip", Title: "outside"},
	}}
	providerB := compositeSearchStub{hits: []SearchHit{
		{Provider: "tavily", URL: "https://example.org/third", Title: "third"},
		{Provider: "tavily", URL: "https://example.org/path?fbclid=two", Title: "duplicate"},
		{Provider: "tavily", URL: "https://example.org/fourth", Title: "fourth"},
	}}
	provider := NewCompositeSearch(providerA, providerB)
	hits, err := provider.Search(context.Background(), SearchOptions{Query: "technical research", Domains: []string{"example.org"}, MaxResults: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want capped 3: %#v", len(hits), hits)
	}
	if hits[0].Title != "first" || hits[1].Title != "third" || hits[2].Title != "second" {
		t.Fatalf("hits were not deduplicated and interleaved deterministically: %#v", hits)
	}
	for _, hit := range hits {
		if hit.Taint != TaintExternal {
			t.Errorf("hit was not marked untrusted: %#v", hit)
		}
	}
}

func TestCompositeSearchMovesPreferredDomainsForwardWithoutDroppingOtherHits(t *testing.T) {
	provider := NewCompositeSearch(compositeSearchStub{hits: []SearchHit{
		{Provider: "tavily", URL: "https://aggregator.example/list", Title: "Aggregator"},
		{Provider: "tavily", URL: "https://docs.example.org/protocol", Title: "Official docs"},
	}})
	hits, err := provider.Search(context.Background(), SearchOptions{
		Query: "protocol reference", PreferredDomains: []string{"docs.example.org"}, MaxResults: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Title != "Official docs" || hits[1].Title != "Aggregator" {
		t.Fatalf("preferred results were not ranked first while retaining other leads: %#v", hits)
	}
}

func TestCompositeSearchKeepsPartialResultsAndEmptySuccess(t *testing.T) {
	t.Run("other provider has hits", func(t *testing.T) {
		provider := NewCompositeSearch(
			compositeSearchStub{err: errors.New("provider unavailable")},
			compositeSearchStub{hits: []SearchHit{{URL: "https://example.org/", Title: "hit"}}},
		)
		hits, err := provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 2})
		if err != nil || len(hits) != 1 {
			t.Fatalf("Search() hits=%#v err=%v", hits, err)
		}
	})

	t.Run("other provider succeeds empty", func(t *testing.T) {
		provider := NewCompositeSearch(
			compositeSearchStub{err: errors.New("provider unavailable")},
			compositeSearchStub{hits: []SearchHit{}},
		)
		hits, err := provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 2})
		if err != nil || len(hits) != 0 {
			t.Fatalf("Search() hits=%#v err=%v, want successful empty results", hits, err)
		}
	})
}

func TestCompositeSearchReturnsUnavailableOrOversizedWhenAllProvidersFail(t *testing.T) {
	for _, test := range []struct {
		name      string
		providers []SearchProvider
		want      error
	}{
		{name: "unavailable", providers: []SearchProvider{compositeSearchStub{err: errors.New("one")}, compositeSearchStub{err: errors.New("two")}}, want: ErrSearchUnavailable},
		{name: "oversized", providers: []SearchProvider{compositeSearchStub{err: ErrSearchResponseTooBig}, compositeSearchStub{err: ErrSearchUnavailable}}, want: ErrSearchResponseTooBig},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := NewCompositeSearch(test.providers...)
			_, err := provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 1})
			if !errors.Is(err, test.want) {
				t.Fatalf("Search() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCompositeSearchHonorsCanceledContextAndRejectsInvalidLimit(t *testing.T) {
	provider := NewCompositeSearch(compositeSearchStub{hits: []SearchHit{{URL: "https://example.org/"}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Search(ctx, SearchOptions{Query: "query", MaxResults: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Search() error = %v", err)
	}
	if _, err := provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 0}); err == nil {
		t.Fatal("invalid max results unexpectedly succeeded")
	}
}
