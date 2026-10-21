package research

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type strategyProvider func(context.Context, SearchOptions) ([]SearchHit, error)

func (f strategyProvider) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	return f(ctx, options)
}

func strategyHits(urls ...string) []SearchHit {
	hits := make([]SearchHit, 0, len(urls))
	for _, raw := range urls {
		hits = append(hits, SearchHit{URL: raw, Provider: "original", Query: "original query", Score: 0.7})
	}
	return hits
}

func TestLocalPrimarySufficientSourcesSkipFallback(t *testing.T) {
	for _, degraded := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "degraded"}[degraded], func(t *testing.T) {
			primary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
				var err error
				if degraded {
					err = ErrSearchUnavailable
				}
				return strategyHits("https://one.com/a", "https://two.org/b"), err
			})
			fallback := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
				t.Error("unexpected fallback")
				return nil, nil
			})
			s := NewLocalPrimarySearch(primary, fallback, fallback)
			report, err := s.SearchWithDiagnostics(context.Background(), SearchOptions{MaxResults: 5})
			if err != nil || len(report.Hits) != 2 || len(report.Outcomes) != 1 || report.Hits[0].Provider != "original" || report.Hits[0].Query != "original query" || report.Hits[0].Score != 0.7 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if degraded && report.Outcomes[0].Code != "search_partial" {
				t.Fatalf("partial outcome=%+v", report.Outcomes)
			}
		})
	}
}

func TestLocalPrimaryTimeoutLeavesFallbackBudget(t *testing.T) {
	var primaryDeadline time.Time
	primary := strategyProvider(func(ctx context.Context, _ SearchOptions) ([]SearchHit, error) {
		primaryDeadline, _ = ctx.Deadline()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	fallback := strategyProvider(func(ctx context.Context, _ SearchOptions) ([]SearchHit, error) {
		deadline, _ := ctx.Deadline()
		if !deadline.After(primaryDeadline) || ctx.Err() != nil {
			t.Error("fallback did not retain its budget")
		}
		return strategyHits("https://one.com", "https://two.org"), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	report, err := NewLocalPrimarySearch(primary, fallback, nil).SearchWithDiagnostics(ctx, SearchOptions{MaxResults: 5})
	if err != nil || len(report.Hits) != 2 || len(report.Outcomes) != 2 || report.Outcomes[0].Code != "search_provider_timeout" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestLocalPrimaryPartialSurvivesFailedSecondary(t *testing.T) {
	primary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		return strategyHits("https://one.com/a"), ErrSearchUnavailable
	})
	secondary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		return nil, ErrSearchUnavailable
	})
	report, err := NewLocalPrimarySearch(primary, secondary, nil).SearchWithDiagnostics(context.Background(), SearchOptions{MaxResults: 5})
	if err != nil || len(report.Hits) != 1 || len(report.Outcomes) != 2 || report.Outcomes[0].Code != "search_partial" || report.Outcomes[1].State == "healthy" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestLocalPrimaryEmptyVersusOutage(t *testing.T) {
	for _, test := range []struct {
		name         string
		primaryErr   error
		secondaryErr error
		wantFailure  bool
	}{
		{name: "valid empty"},
		{name: "primary outage", primaryErr: ErrSearchUnavailable, wantFailure: true},
		{name: "secondary outage", secondaryErr: ErrSearchUnavailable, wantFailure: true},
		{name: "filtered", primaryErr: ErrSearchFilteredByPolicy, wantFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			primary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) { calls++; return nil, test.primaryErr })
			secondary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) { calls++; return nil, test.secondaryErr })
			report, err := NewLocalPrimarySearch(primary, secondary, nil).SearchWithDiagnostics(context.Background(), SearchOptions{MaxResults: 5})
			if (err != nil) != test.wantFailure || len(report.Hits) != 0 || calls != 2 || len(report.Outcomes) != 2 {
				t.Fatalf("report=%+v err=%v calls=%d", report, err, calls)
			}
		})
	}
}

func TestLocalPrimaryIndependentDomainsAndPriority(t *testing.T) {
	calls := 0
	primary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		return strategyHits("https://a.one.co.uk/a", "https://b.one.co.uk/b"), nil
	})
	secondary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		calls++
		return strategyHits("https://a.one.co.uk/a", "https://two.org"), nil
	})
	report, err := NewLocalPrimarySearch(primary, secondary, nil).SearchWithDiagnostics(context.Background(), SearchOptions{MaxResults: 3})
	if err != nil || calls != 1 || len(report.Hits) != 3 || report.Hits[0].URL != "https://a.one.co.uk/a" || report.Hits[1].URL != "https://b.one.co.uk/b" || report.Hits[2].URL != "https://two.org" {
		t.Fatalf("report=%+v err=%v calls=%d", report, err, calls)
	}
}

func TestLocalPrimaryDDGFallbackAndTavilyOnly(t *testing.T) {
	for _, withLocal := range []bool{false, true} {
		calls := 0
		empty := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) { calls++; return nil, nil })
		ddg := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
			calls++
			return strategyHits("https://one.com"), nil
		})
		var local SearchProvider
		if withLocal {
			local = empty
		}
		report, err := NewLocalPrimarySearch(local, empty, ddg).SearchWithDiagnostics(context.Background(), SearchOptions{MaxResults: 5})
		wantCalls := 2
		if withLocal {
			wantCalls++
		}
		if err != nil || calls != wantCalls || len(report.Hits) != 1 {
			t.Fatalf("report=%+v err=%v calls=%d", report, err, calls)
		}
	}
}

func TestLocalPrimaryCancellationDominatesHits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	primary := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		cancel()
		return strategyHits("https://one.com", "https://two.org"), nil
	})
	fallback := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		t.Error("fallback after cancellation")
		return nil, nil
	})
	_, err := NewLocalPrimarySearch(primary, fallback, nil).SearchWithDiagnostics(ctx, SearchOptions{MaxResults: 5})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestLocalPrimaryNoProvidersAndInvalidOptions(t *testing.T) {
	s := NewLocalPrimarySearch(nil, nil, nil)
	_, err := s.SearchWithDiagnostics(context.Background(), SearchOptions{MaxResults: 5})
	if !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("error=%v", err)
	}
	_, err = s.SearchWithDiagnostics(context.Background(), SearchOptions{})
	if err == nil {
		t.Fatal("invalid options accepted")
	}
}

func TestLocalPrimaryHealthCopiesAndConcurrentAccess(t *testing.T) {
	s := NewLocalPrimarySearch(strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) { return strategyHits("https://one.com"), nil }), nil, nil)
	initial := s.HealthSnapshot()
	if len(initial) != 1 || initial[0].State != "configured" {
		t.Fatalf("initial=%+v", initial)
	}
	initial[0].State = "mutated"
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report, _ := s.SearchWithDiagnostics(context.Background(), SearchOptions{MaxResults: 5})
			report.Outcomes[0].State = "mutated"
			snapshot := s.HealthSnapshot()
			snapshot[0].State = "mutated"
		}()
	}
	wg.Wait()
	if got := s.HealthSnapshot(); got[0].State != "healthy" {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestLocalPrimaryUnattemptedProvidersRemainConfigured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"url":"https://one.com/a","title":"One"},{"url":"https://two.org/b","title":"Two"}],"unresponsive_engines":[["engine","timeout"]]}`))
	}))
	defer server.Close()
	primary, err := NewSearXNG(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	blockedClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unattempted provider made a request")
		return nil, ErrSearchUnavailable
	})}
	secondary, err := NewTavily("test-key", blockedClient)
	if err != nil {
		t.Fatal(err)
	}
	ddg := NewDuckDuckGo()
	ddg.fetcher.client = blockedClient
	s := NewLocalPrimarySearch(primary, secondary, ddg)
	report, err := s.SearchWithDiagnostics(context.Background(), SearchOptions{Query: "fixture", MaxResults: 5})
	if err != nil || len(report.Hits) != 2 || len(report.Outcomes) != 1 || report.Outcomes[0].State != "degraded" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	health := s.HealthSnapshot()
	if len(health) != 3 || health[0].Provider != "searxng" || health[1].Provider != "tavily" || health[2].Provider != "duckduckgo" || health[1].State != "configured" || health[2].State != "configured" {
		t.Fatalf("health=%+v", health)
	}
}
