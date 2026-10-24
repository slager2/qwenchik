package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/providers/research"
)

type healthyValidationSearch struct{}

type timedValidationSearch struct{ empty bool }

func validationSafeFetcher(t *testing.T, maxBytes int64, timeout time.Duration, resolver research.IPResolver) *research.SafeFetcher {
	t.Helper()
	policy := research.DefaultFetchPolicy()
	policy.TempRoot = filepath.Join(t.TempDir(), "fetch-temp")
	if maxBytes > 0 {
		policy.SourceFetchBytes = min(policy.SourceFetchBytes, maxBytes)
		policy.PDFBytes = min(policy.PDFBytes, maxBytes)
	}
	if timeout > 0 {
		policy.SourceDownloadTimeout = timeout
		policy.PDFDownloadTimeout = timeout
	}
	fetcher, err := research.NewSafeFetcherWithFetchPolicy(policy, resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fetcher
}

func (p timedValidationSearch) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	report, err := p.SearchWithDiagnostics(ctx, options)
	return report.Hits, err
}

func (p timedValidationSearch) SearchWithDiagnostics(ctx context.Context, options research.SearchOptions) (research.SearchReport, error) {
	<-ctx.Done()
	report, _ := (healthyValidationSearch{}).SearchWithDiagnostics(ctx, options)
	if p.empty {
		report.Hits = nil
	}
	return report, ctx.Err()
}

func TestSearchBudgetsReserveValidationAfterCollectionTimeout(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			registry := NewRegistry()
			resolver := &validationDeadlineResolver{}
			if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: timedValidationSearch{empty: empty}, Fetcher: validationSafeFetcher(t, 1024, time.Second, resolver)}); err != nil {
				t.Fatal(err)
			}
			tool := registry.byName["search"]
			tool.descriptor.TimeoutSeconds = 1
			registry.byName["search"] = tool
			var outcomes []research.ProviderOutcome
			var observations []research.SearchHitObservation
			ctx := WithTaskID(context.Background(), "reserved-validation")
			ctx = WithSearchOutcomeObserver(ctx, func(o research.ProviderOutcome) { outcomes = append(outcomes, o) })
			ctx = WithSearchHitObserver(ctx, func(_ context.Context, o research.SearchHitObservation) error {
				observations = append(observations, o)
				return nil
			})
			result, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"official evidence"}`))
			if empty {
				var failure *research.SearchFailure
				if !errors.As(err, &failure) || failure.Code != "search_provider_timeout" || result != nil || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("result=%#v err=%#v", result, err)
				}
				return
			}
			if err != nil || len(result.([]research.SearchHit)) != 1 || len(outcomes) != 1 || outcomes[0].Code != "search_partial" {
				t.Fatalf("result=%#v err=%v outcomes=%#v", result, err, outcomes)
			}
			if len(observations) != 4 || observations[2].Status != "validated" || observations[3].Status != "rejected" || observations[3].ErrorCode != "search_validation_timeout" {
				t.Fatalf("observations=%#v", observations)
			}
		})
	}
}

func (p healthyValidationSearch) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	report, err := p.SearchWithDiagnostics(ctx, options)
	return report.Hits, err
}

func TestSearchValidationDeduplicatesBeforeDNSAndSerializesObservations(t *testing.T) {
	resolver := &validationDeadlineResolver{}
	registry := NewRegistry()
	hits := validationSearchHits{
		{Provider: "searxng", URL: "https://usable.example/source?utm_source=one#top", Title: "Source", Snippet: "Useful discovery"},
		{Provider: "tavily", URL: "https://USABLE.example:443/source", Title: "Duplicate"},
	}
	if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: hits, Fetcher: validationSafeFetcher(t, 1024, time.Second, resolver)}); err != nil {
		t.Fatal(err)
	}
	var observations []research.SearchHitObservation
	ctx := WithSearchHitObserver(WithTaskID(context.Background(), "dedup"), func(_ context.Context, o research.SearchHitObservation) error {
		observations = append(observations, o)
		return nil
	})
	result, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"source"}`))
	if err != nil || len(result.([]research.SearchHit)) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.hosts) != 1 || len(observations) != 2 || observations[0].Status != "discovered" || observations[1].Status != "validated" || observations[0].ID != observations[1].ID {
		t.Fatalf("DNS=%v observations=%#v", resolver.hosts, observations)
	}
}

func TestSearchValidationBoundsConcurrency(t *testing.T) {
	var active, maximum atomic.Int64
	started, release := make(chan struct{}, 8), make(chan struct{})
	fetcher := &validationFetchStub{validate: func(context.Context, string) error {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return nil
	}}
	hits := make([]research.SearchHit, 8)
	for i := range hits {
		hits[i].URL = fmt.Sprintf("https://example.org/source/%d", i)
	}
	go func() {
		for i := 0; i < 4; i++ {
			<-started
		}
		close(release)
	}()
	result, err := validateSearchHits(context.Background(), fetcher, hits, nil, "query")
	if err != nil || len(result) != 8 || maximum.Load() > 4 || maximum.Load() < 2 {
		t.Fatalf("hits=%d err=%v concurrency=%d", len(result), err, maximum.Load())
	}
}

func TestSearchValidationTypedFailuresAndSafeRejectedEvents(t *testing.T) {
	for _, fixture := range []struct {
		name, rawURL, code string
		validation         error
	}{
		{"policy", "https://example.org/source", "search_filtered_by_policy", research.ErrUnsafeURL},
		{"DNS", "https://example.org/source", "search_no_usable_hits", errors.New("private resolver details")},
		{"deadline", "https://example.org/source", "search_validation_timeout", context.DeadlineExceeded},
		{"sensitive", "https://user:secret@example.org/source?access_token=secret", "search_filtered_by_policy", nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var observations []research.SearchHitObservation
			ctx := WithSearchHitObserver(context.Background(), func(_ context.Context, o research.SearchHitObservation) error {
				observations = append(observations, o)
				return nil
			})
			fetcher := &validationFetchStub{validate: func(context.Context, string) error { return fixture.validation }}
			_, err := validateSearchHits(ctx, fetcher, []research.SearchHit{{URL: fixture.rawURL, Title: "secret", Snippet: "secret"}}, nil, "private query")
			var failure *research.SearchFailure
			if !errors.As(err, &failure) || failure.Code != fixture.code || len(observations) != 2 || observations[1].Status != "rejected" {
				t.Fatalf("err=%v observations=%#v", err, observations)
			}
			if fixture.name == "sensitive" {
				encoded, _ := json.Marshal(observations)
				if strings.Contains(string(encoded), "secret") || observations[1].Origin != "https://example.org" {
					t.Fatalf("unsafe observation: %s", encoded)
				}
			}
		})
	}
}

func TestSearchHitObserverFailurePreventsLedgerApproval(t *testing.T) {
	for _, failedStatus := range []string{"discovered", "validated"} {
		t.Run(failedStatus, func(t *testing.T) {
			registry := NewRegistry()
			if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: searchStub{url: "https://example.org/source"}, Fetcher: &fetchStub{}}); err != nil {
				t.Fatal(err)
			}
			writeErr := errors.New("event write failed")
			ctx := WithSearchHitObserver(WithTaskID(context.Background(), "observer-failure"), func(_ context.Context, o research.SearchHitObservation) error {
				if o.Status == failedStatus {
					return writeErr
				}
				return nil
			})
			result, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"source"}`))
			if !errors.Is(err, writeErr) || result != nil || len(registry.KnownTaskURLs("observer-failure")) != 0 {
				t.Fatalf("result=%#v err=%v ledger=%v", result, err, registry.KnownTaskURLs("observer-failure"))
			}
		})
	}
}

func TestSearchSafeObservationSanitizer(t *testing.T) {
	for _, raw := range []string{"https://example.org/?refresh_token=secret", "https://example.org/session/secret", "https://user:secret@example.org/source", "https://example.org/?X-Amz-Signature=secret"} {
		o := SafeSearchHitObservation(research.SearchHitObservation{URL: raw, Origin: "https://example.org/secret", Title: "secret", Snippet: "secret", Status: "rejected"})
		encoded, _ := json.Marshal(o)
		if strings.Contains(string(encoded), "secret") || o.URL != "" {
			t.Fatalf("unsafe observation: %s", encoded)
		}
	}
	o := SafeSearchHitObservation(research.SearchHitObservation{URL: "https://example.org/docs?q=protocol", Title: "Protocol docs", Snippet: "Useful text", Status: "validated", Provider: "searxng"})
	if o.URL == "" || o.Title == "" || o.Snippet == "" {
		t.Fatalf("legitimate source card lost: %#v", o)
	}
}

func (healthyValidationSearch) SearchWithDiagnostics(_ context.Context, options research.SearchOptions) (research.SearchReport, error) {
	return research.SearchReport{
		Hits: []research.SearchHit{
			{Provider: "searxng", Query: options.Query, URL: "https://usable.example/source", Title: "Usable source", Snippet: "Discovery lead"},
			{Provider: "searxng", Query: options.Query, URL: "https://slow.example/source", Title: "Slow source", Snippet: "Another lead"},
		},
		Outcomes: []research.ProviderOutcome{{Provider: "searxng", State: "healthy", Code: "search_ok", HTTPStatus: 200, ResultCount: 2}},
	}, nil
}

type validationDeadlineResolver struct {
	mu    sync.Mutex
	hosts []string
}

func (r *validationDeadlineResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	r.hosts = append(r.hosts, host)
	r.mu.Unlock()
	if host == "usable.example" {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSearchValidationPreservesUsableHitsAfterSlowDNS(t *testing.T) {
	registry := NewRegistry()
	resolver := &validationDeadlineResolver{}
	fetcher := validationSafeFetcher(t, 1024, time.Second, resolver)
	if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: healthyValidationSearch{}, Fetcher: fetcher}); err != nil {
		t.Fatal(err)
	}
	tool := registry.byName["search"]
	tool.descriptor.TimeoutSeconds = 1
	registry.byName["search"] = tool
	var outcomes []research.ProviderOutcome
	parent := WithTaskID(context.Background(), "validation-partial")
	ctx := WithSearchOutcomeObserver(parent, func(outcome research.ProviderOutcome) {
		outcomes = append(outcomes, outcome)
	})
	result, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"official evidence","max_results":2}`))
	resolver.mu.Lock()
	hosts := append([]string(nil), resolver.hosts...)
	resolver.mu.Unlock()
	t.Logf("result=%#v err=%v parent_err=%v DNS_calls=%v provider_outcomes=%#v ledger=%v", result, err, parent.Err(), hosts, outcomes, registry.KnownTaskURLs("validation-partial"))
	if parent.Err() != nil {
		t.Fatalf("parent was canceled: %v", parent.Err())
	}
	if len(outcomes) != 1 || outcomes[0].Provider != "searxng" || outcomes[0].Code != "search_ok" || outcomes[0].ResultCount != 2 {
		t.Errorf("healthy provider report was lost: %#v", outcomes)
	}
	if err != nil {
		t.Errorf("slow URL validation discarded usable discovery leads: %v", err)
	}
	hits, ok := result.([]research.SearchHit)
	if !ok || len(hits) != 1 || hits[0].URL != "https://usable.example/source" || hits[0].Taint != research.TaintExternal {
		t.Errorf("want the validated usable hit, got %#v", result)
	}
	if !registry.urlLedger.Contains("validation-partial", "https://usable.example/source") {
		t.Error("validated usable hit was not retained in task URL ledger")
	}
	if registry.urlLedger.Contains("validation-partial", "https://slow.example/source") {
		t.Error("unvalidated slow hit entered the approved task URL ledger")
	}
}

func TestSearchObserverCancellationRemainsParentCancellation(t *testing.T) {
	for _, status := range []string{"discovered", "rejected"} {
		t.Run(status, func(t *testing.T) {
			registry := NewRegistry()
			fetcher := &validationFetchStub{validate: func(context.Context, string) error { return research.ErrUnsafeURL }}
			if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: searchStub{url: "https://example.org/source"}, Fetcher: fetcher}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = WithSearchHitObserver(WithTaskID(ctx, "observer-cancel"), func(_ context.Context, o research.SearchHitObservation) error {
				if o.Status == status {
					cancel()
				}
				return nil
			})
			result, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"source"}`))
			if !errors.Is(err, context.Canceled) || result != nil || errors.Is(err, research.ErrSearchFilteredByPolicy) || len(registry.KnownTaskURLs("observer-cancel")) != 0 {
				t.Fatalf("observer cancellation misclassified: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestSearchResultDoesNotPersistProviderInternalQuery(t *testing.T) {
	registry := NewRegistry()
	hits := validationSearchHits{{Provider: "searxng", Query: "https://provider.example/search?q=source&api_key=PRIVATE", URL: "https://example.org/source", Title: "Source"}}
	if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: hits, Fetcher: &fetchStub{}}); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(WithTaskID(context.Background(), "query-safety"), "researcher", "search", json.RawMessage(`{"query":"public research"}`))
	encoded, _ := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "PRIVATE") || result.([]research.SearchHit)[0].Query != "public research" {
		t.Fatalf("provider query propagated: %s err=%v", encoded, err)
	}
}

func TestValidationBudgetSurvivesUnresponsiveURLValidator(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	fetcher := &validationFetchStub{validate: func(_ context.Context, rawURL string) error {
		if strings.Contains(rawURL, "/slow") {
			<-release
		}
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	hits, err := validateSearchHits(ctx, fetcher, []research.SearchHit{{URL: "https://example.org/good"}, {URL: "https://example.org/slow"}}, nil, "query")
	if err != nil || ctx.Err() != nil || len(hits) != 1 || hits[0].URL != "https://example.org/good" {
		t.Fatalf("validation consumed parent budget: hits=%#v err=%v parent=%v", hits, err, ctx.Err())
	}
}

func TestSearchObserverDeadlineRemainsParentDeadline(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: searchStub{url: "https://example.org/source"}, Fetcher: &fetchStub{}}); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ctx := WithSearchHitObserver(WithTaskID(parent, "observer-deadline"), func(_ context.Context, o research.SearchHitObservation) error {
		if o.Status == "validated" {
			<-parent.Done()
		}
		return nil
	})
	result, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"source"}`))
	var failure *research.SearchFailure
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &failure) || result != nil || len(registry.KnownTaskURLs("observer-deadline")) != 0 {
		t.Fatalf("parent deadline misclassified: result=%#v err=%v", result, err)
	}
}

func TestSearchDiagnosticsStripCredentialTextAndUnfetchableURLs(t *testing.T) {
	for _, text := range []string{"API_KEY : PRIVATE", "client-secret: PRIVATE", "access_token = PRIVATE", "Authorization: PRIVATE"} {
		o := SafeSearchHitObservation(research.SearchHitObservation{URL: "https://example.org/source", Title: text, Snippet: text, Status: "validated"})
		if o.Title != "" || o.Snippet != "" || o.URL == "" {
			t.Fatalf("credential-like metadata survived: %#v", o)
		}
	}
	raw := "https://example.org/" + strings.Repeat("a", 2100)
	hits, err := validateSearchHits(context.Background(), &fetchStub{}, []research.SearchHit{{URL: raw}}, nil, "query")
	if len(hits) != 0 || !errors.Is(err, research.ErrSearchFilteredByPolicy) {
		t.Fatalf("URL exceeding fetch argument limit admitted: %#v err=%v", hits, err)
	}
}
