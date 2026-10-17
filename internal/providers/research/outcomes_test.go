package research

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var _ DiagnosticSearchProvider = (*CompositeSearch)(nil)
var _ DiagnosticSearchProvider = (*SearXNG)(nil)
var _ DiagnosticSearchProvider = (*Tavily)(nil)
var _ DiagnosticSearchProvider = (*DuckDuckGo)(nil)

type diagnosticFixture struct {
	name, body, contentType, state, code string
	status                               int
	retryable                            bool
	want                                 error
}

func fixtureDiagnosticProvider(t *testing.T, name string, transport http.RoundTripper) DiagnosticSearchProvider {
	t.Helper()
	switch name {
	case "searxng":
		provider, err := NewSearXNG("http://localhost", &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	case "tavily":
		provider, err := NewTavily("test-api-key", &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	default:
		return newFixtureDuckDuckGo(t, transport)
	}
}

func TestProviderDiagnosticFixtureMatrix(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"searxng", "tavily", "duckduckgo"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cases := []diagnosticFixture{
				{name: "rate limit", status: 429, state: "degraded", code: "provider_rate_limited", retryable: true, want: ErrSearchUnavailable},
				{name: "forbidden", status: 403, state: "blocked", code: "provider_blocked", want: ErrSearchUnavailable},
				{name: "server failure", status: 503, state: "unavailable", code: "search_unavailable", retryable: true, want: ErrSearchUnavailable},
				{name: "redirect", status: 302, state: "unavailable", code: "search_unavailable", want: ErrSearchUnavailable},
			}
			if name == "duckduckgo" {
				cases = append(cases,
					diagnosticFixture{"empty", `<div class="no-results"><div class="no-results__message">No results found</div></div>`, "text/html", "degraded", "search_no_results", 200, false, nil},
				)
				for _, body := range []string{`<form id="challenge-form">challenge</form>`, `<div class="anomaly-modal">bot</div>`, `<form id="consent-form">consent</form>`, `<div id="captcha">CAPTCHA</div>`} {
					cases = append(cases, diagnosticFixture{"challenge", body, "text/html", "blocked", "provider_blocked", 200, false, ErrSearchUnavailable})
				}
				for _, body := range []string{`<html>unexpected maintenance page</html>`, `<div class="result"><a class="changed-link" href="https://example.com">drift</a></div>`} {
					cases = append(cases, diagnosticFixture{"parser drift", body, "text/html", "malformed", "provider_malformed", 200, false, ErrSearchUnavailable})
				}
			} else {
				for _, body := range []string{`{}`, `{"results":null}`, `{"results":{}}`, `{"results":[null]}`, `{"results":[{"url":42}]}`, `not json secret-query test-api-key`} {
					cases = append(cases, diagnosticFixture{"bad shape", body, "application/json", "malformed", "provider_malformed", 200, false, ErrSearchUnavailable})
				}
				cases = append(cases, diagnosticFixture{"empty", `{"results":[]}`, "application/json", "degraded", "search_no_results", 200, false, nil})
			}
			if name == "tavily" {
				cases = append(cases, diagnosticFixture{"credentials", "secret", "application/json", "misconfigured", "provider_misconfigured", 401, false, ErrSearchUnavailable})
				for _, status := range []int{432, 433} {
					cases = append(cases, diagnosticFixture{"quota", "secret-query test-api-key quota detail", "application/json", "misconfigured", "provider_misconfigured", status, false, ErrSearchUnavailable})
				}
				for index := range cases {
					if cases[index].status == 403 {
						cases[index].state, cases[index].code = "misconfigured", "provider_misconfigured"
					}
				}
			}
			if name == "searxng" {
				cases = append(cases, diagnosticFixture{"wrong content type", `{"results":[]}`, "text/html", "malformed", "provider_malformed", 200, false, ErrSearchUnavailable})
				cases = append(cases, diagnosticFixture{"upstream failures", `{"results":[],"unresponsive_engines":[["engine","secret-query test-api-key"]]}`, "application/json", "degraded", "search_unavailable", 200, true, ErrSearchUnavailable})
			}
			for _, fixture := range cases {
				t.Run(fixture.name, func(t *testing.T) {
					transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: fixture.status, Header: http.Header{"Content-Type": []string{fixture.contentType}}, Body: io.NopCloser(strings.NewReader(fixture.body)), Request: request}, nil
					})
					provider := fixtureDiagnosticProvider(t, name, transport)
					report, err := provider.SearchWithDiagnostics(context.Background(), SearchOptions{Query: "secret-query", MaxResults: 2})
					if fixture.want == nil && err != nil || fixture.want != nil && !errors.Is(err, fixture.want) {
						t.Fatalf("unexpected error: %v", err)
					}
					if len(report.Outcomes) != 1 {
						t.Fatalf("outcomes: %#v", report)
					}
					o := report.Outcomes[0]
					if o.Provider != name || o.State != fixture.state || o.Code != fixture.code || o.Retryable != fixture.retryable || o.HTTPStatus != fixture.status || o.CheckedAt.IsZero() || o.LatencyMS < 0 {
						t.Fatalf("outcome: %#v", o)
					}
					encoded, _ := json.Marshal(report.Outcomes)
					if strings.Contains(string(encoded), "secret-query") || strings.Contains(string(encoded), "test-api-key") || len(o.Message) > 200 {
						t.Fatalf("unsafe outcome: %s", encoded)
					}
				})
			}
		})
	}
}

func TestProviderDiagnosticResultsPolicyAndLimits(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"searxng", "tavily", "duckduckgo"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body, contentType := `{"results":[{"url":"https://docs.example.com/a","title":"CAPTCHA reference"},{"url":"https://docs.example.com/b","title":"Other"}]}`, "application/json"
			if name == "duckduckgo" {
				body, contentType = `<div class="result"><a class="result__a" href="https://docs.example.com/a">CAPTCHA reference</a></div><div class="result"><a class="result__a" href="https://docs.example.com/b">Other</a></div>`, "text/html"
			}
			provider := fixtureDiagnosticProvider(t, name, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			}))
			options := SearchOptions{Query: "query", MaxResults: 2}
			report, err := provider.SearchWithDiagnostics(context.Background(), options)
			if err != nil || len(report.Hits) != 2 || report.Outcomes[0].ResultCount != 2 || report.Outcomes[0].DistinctDomains != 1 || report.Outcomes[0].State != "healthy" {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			if report.Outcomes[0].BodySize != int64(len(body)) || report.Outcomes[0].ContentType != contentType || report.Outcomes[0].ParserVersion != map[string]string{"searxng": "searxng-json-v1", "tavily": "tavily-json-v1", "duckduckgo": "duckduckgo-html-v1"}[name] {
				t.Fatalf("metadata=%#v", report.Outcomes[0])
			}
			options.Domains = []string{"different.example"}
			report, err = provider.SearchWithDiagnostics(context.Background(), options)
			if !errors.Is(err, ErrSearchFilteredByPolicy) || len(report.Hits) != 0 || report.Outcomes[0].Code != "search_filtered_by_policy" {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			for _, declared := range []bool{false, true} {
				provider := fixtureDiagnosticProvider(t, name, roundTripFunc(func(request *http.Request) (*http.Response, error) {
					response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (2<<20)+1))), ContentLength: -1, Request: request}
					if declared {
						response.ContentLength = (2 << 20) + 1
					}
					return response, nil
				}))
				report, err := provider.SearchWithDiagnostics(context.Background(), options)
				if !errors.Is(err, ErrSearchResponseTooBig) || report.Outcomes[0].Code != "search_response_too_big" || report.Outcomes[0].HTTPStatus != 200 {
					t.Fatalf("report=%#v err=%v", report, err)
				}
				if report.Outcomes[0].BodySize != (2<<20)+1 {
					t.Fatalf("unbounded or missing body size: %#v", report.Outcomes[0])
				}
			}
			provider = fixtureDiagnosticProvider(t, name, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("secret-query test-api-key raw provider body")
			}))
			report, err = provider.SearchWithDiagnostics(context.Background(), options)
			encoded, _ := json.Marshal(report.Outcomes)
			if !errors.Is(err, ErrSearchUnavailable) || report.Outcomes[0].HTTPStatus != 0 || strings.Contains(string(encoded), "secret-query") || strings.Contains(string(encoded), "test-api-key") {
				t.Fatalf("report=%#v err=%v", report, err)
			}
		})
	}
}

func TestCompositeMixedEmptyFailures(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		name      string
		err, want error
		code      string
	}{
		{"unavailable", ErrSearchUnavailable, ErrSearchUnavailable, "search_unavailable"},
		{"blocked", classified("provider_blocked", "blocked", 403, false), ErrSearchUnavailable, "search_unavailable"},
		{"malformed", classified("provider_malformed", "malformed", 200, false), ErrSearchUnavailable, "search_unavailable"},
		{"misconfigured", classified("provider_misconfigured", "misconfigured", 401, false), ErrSearchUnavailable, "search_unavailable"},
		{"rate limited", classified("provider_rate_limited", "degraded", 429, true), ErrSearchUnavailable, "search_unavailable"},
		{"oversized", ErrSearchResponseTooBig, ErrSearchResponseTooBig, "search_response_too_big"},
		{"filtered", ErrSearchFilteredByPolicy, ErrSearchFilteredByPolicy, "search_filtered_by_policy"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				providers := []SearchProvider{compositeSearchStub{}, compositeSearchStub{err: fixture.err}}
				if reverse {
					providers[0], providers[1] = providers[1], providers[0]
				}
				c := NewCompositeSearch(providers...)
				report, err := c.SearchWithDiagnostics(context.Background(), SearchOptions{Query: "query", MaxResults: 2})
				var failure *SearchFailure
				if !errors.As(err, &failure) || !errors.Is(err, fixture.want) || failure.Code != fixture.code || len(report.Hits) != 0 || len(report.Outcomes) != 2 {
					t.Fatalf("report=%#v err=%v", report, err)
				}
				emptyIndex := 0
				if reverse {
					emptyIndex = 1
				}
				if report.Outcomes[emptyIndex].Code != "search_no_results" || !reflect.DeepEqual(report.Outcomes, failure.Outcomes) || !reflect.DeepEqual(report.Outcomes, c.HealthSnapshot()) {
					t.Fatalf("inconsistent outcomes: %#v", report)
				}
			}
		})
	}
}

func TestCompositeHTTPAllEmptyIsValid(t *testing.T) {
	t.Parallel()
	var providers []SearchProvider
	for _, name := range []string{"searxng", "tavily", "duckduckgo"} {
		provider := fixtureDiagnosticProvider(t, name, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, contentType := `{"results":[]}`, "application/json"
			if name == "duckduckgo" {
				body, contentType = `<div class="no-results">No results found</div>`, "text/html"
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		}))
		providers = append(providers, provider.(SearchProvider))
	}
	c := NewCompositeSearch(providers...)
	report, err := c.SearchWithDiagnostics(context.Background(), SearchOptions{Query: "query", MaxResults: 2})
	if err != nil || report.Hits == nil || len(report.Hits) != 0 || len(report.Outcomes) != 3 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	for _, outcome := range report.Outcomes {
		if outcome.Code != "search_no_results" || outcome.State != "degraded" || outcome.HTTPStatus != 200 || outcome.BodySize == 0 || outcome.ContentType == "" || outcome.ParserVersion == "" {
			t.Fatalf("outcome=%#v", outcome)
		}
	}
}

type concurrentMetadataStub struct {
	wg *sync.WaitGroup
}

func (s concurrentMetadataStub) Search(ctx context.Context, _ SearchOptions) ([]SearchHit, error) {
	for i := 0; i < 8; i++ {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for j := 0; j < 100; j++ {
				recordResponseStatus(ctx, 200)
				recordResponseMetadata(ctx, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{strings.Repeat("secret-query", 1000)}}, ContentLength: 1 << 40})
				recordResponseBodySize(ctx, 1<<30)
			}
		}()
	}
	return nil, nil
}

func TestResponseMetadataConcurrentRecording(t *testing.T) {
	var wg sync.WaitGroup
	report, err := diagnosticSearch(context.Background(), SearchOptions{}, concurrentMetadataStub{&wg})
	before := append([]ProviderOutcome(nil), report.Outcomes...)
	wg.Wait()
	if err != nil || !reflect.DeepEqual(before, report.Outcomes) {
		t.Fatalf("report mutated after return: %#v err=%v", report, err)
	}
	metadata := &responseMetadata{}
	ctx := context.WithValue(context.Background(), responseStatusKey{}, metadata)
	recordResponseMetadata(ctx, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{strings.Repeat("secret-query", 1000)}}, ContentLength: 1 << 40})
	recordResponseBodySize(ctx, 1<<30)
	if metadata.contentType != "unknown" || metadata.bodySize != (2<<20)+1 {
		t.Fatalf("metadata not bounded: type=%q size=%d", metadata.contentType, metadata.bodySize)
	}
}

type completionDiagnosticProvider struct {
	provider DiagnosticSearchProvider
	done     chan struct{}
}

func (p completionDiagnosticProvider) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	report, err := p.SearchWithDiagnostics(ctx, options)
	return report.Hits, err
}

func (p completionDiagnosticProvider) SearchWithDiagnostics(ctx context.Context, options SearchOptions) (SearchReport, error) {
	defer close(p.done)
	return p.provider.SearchWithDiagnostics(ctx, options)
}

func TestCanceledSearchSnapshotIgnoresLateResponse(t *testing.T) {
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	provider := fixtureDiagnosticProvider(t, "searxng", roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"results":[]}`)), Request: request}, nil
	}))
	c := NewCompositeSearch(completionDiagnosticProvider{provider: provider, done: completed})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-entered
		cancel()
	}()
	report, err := c.SearchWithDiagnostics(ctx, SearchOptions{Query: "query", MaxResults: 2})
	if !errors.Is(err, context.Canceled) {
		close(release)
		t.Fatalf("err=%v", err)
	}
	before := c.HealthSnapshot()
	close(release)
	<-completed
	for i := 0; i < 100; i++ {
		if !reflect.DeepEqual(before, c.HealthSnapshot()) || !reflect.DeepEqual(before, report.Outcomes) {
			t.Fatal("late response changed snapshot")
		}
	}
}

type diagnosticBlockingStub struct {
	release <-chan struct{}
	active  *atomic.Int32
	peak    *atomic.Int32
}

func (s diagnosticBlockingStub) Search(context.Context, SearchOptions) ([]SearchHit, error) {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for old := s.peak.Load(); active > old; old = s.peak.Load() {
		if s.peak.CompareAndSwap(old, active) {
			break
		}
	}
	<-s.release
	return nil, ErrSearchUnavailable
}

func TestCompositeDiagnosticsBoundFanoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	active, peak := &atomic.Int32{}, &atomic.Int32{}
	providers := make([]SearchProvider, 12)
	for i := range providers {
		providers[i] = diagnosticBlockingStub{release, active, peak}
	}
	c := NewCompositeSearch(providers...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	report, err := c.SearchWithDiagnostics(ctx, SearchOptions{Query: "query", MaxResults: 1})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || len(report.Outcomes) != 12 || peak.Load() > 4 {
		t.Fatalf("err=%v peak=%d report=%#v", err, peak.Load(), report)
	}
}

func TestCompositeDiagnosticHealthAndFailures(t *testing.T) {
	c := NewCompositeSearch(&SearXNG{}, &Tavily{}, &DuckDuckGo{})
	initial := c.HealthSnapshot()
	if len(initial) != 3 || initial[0].Provider != "searxng" || initial[1].Provider != "tavily" || initial[2].Provider != "duckduckgo" {
		t.Fatalf("initial health: %#v", initial)
	}
	for _, o := range initial {
		if !o.CheckedAt.IsZero() || o.Code != "provider_not_checked" || o.State != "configured" {
			t.Fatalf("initial outcome: %#v", o)
		}
	}
	report, err := c.SearchWithDiagnostics(context.Background(), SearchOptions{Query: "query", MaxResults: 1})
	var failure *SearchFailure
	if !errors.As(err, &failure) || !errors.Is(err, ErrSearchUnavailable) || len(failure.Outcomes) != 3 {
		t.Fatalf("failure: %v", err)
	}
	report.Outcomes[0].Code = "mutated"
	snapshot := c.HealthSnapshot()
	if snapshot[0].Code == "mutated" {
		t.Fatal("report aliases health")
	}
	snapshot[0].Code = "mutated"
	if c.HealthSnapshot()[0].Code == "mutated" {
		t.Fatal("snapshot aliases health")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.SearchWithDiagnostics(context.Background(), SearchOptions{Query: "query", MaxResults: 1})
			_ = c.HealthSnapshot()
		}()
	}
	wg.Wait()
}

func TestCompositeDiagnosticPolicyAndWarnings(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		providers []SearchProvider
		domains   []string
		code      string
		want      error
		hits      int
	}{
		{"empty", []SearchProvider{compositeSearchStub{}}, nil, "search_no_results", nil, 0},
		{"policy", []SearchProvider{compositeSearchStub{hits: []SearchHit{{URL: "https://outside.example/"}}}}, []string{"allowed.example"}, "search_filtered_by_policy", ErrSearchFilteredByPolicy, 0},
		{"partial", []SearchProvider{compositeSearchStub{err: errors.New("secret-query")}, compositeSearchStub{hits: []SearchHit{{URL: "https://example.com/"}}}}, nil, "search_unavailable", nil, 1},
		{"oversized", []SearchProvider{compositeSearchStub{err: ErrSearchResponseTooBig}}, nil, "search_response_too_big", ErrSearchResponseTooBig, 0},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			c := NewCompositeSearch(fixture.providers...)
			report, err := c.SearchWithDiagnostics(context.Background(), SearchOptions{Query: "query", Domains: fixture.domains, MaxResults: 2})
			if fixture.want == nil && err != nil || fixture.want != nil && !errors.Is(err, fixture.want) || len(report.Hits) != fixture.hits || report.Outcomes[0].Code != fixture.code {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			if fixture.name == "policy" && errors.Is(err, ErrSearchUnavailable) {
				t.Fatal("policy failure classified unavailable")
			}
		})
	}
}
