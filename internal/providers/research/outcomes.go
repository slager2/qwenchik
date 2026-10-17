package research

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrSearchFilteredByPolicy = errors.New("search results filtered by policy")

type SearchFailure struct {
	Code     string
	Outcomes []ProviderOutcome
}

func (e *SearchFailure) Error() string { return outcomeMessage(e.Code) }

func (e *SearchFailure) Is(target error) bool {
	if target == ErrSearchUnavailable {
		return e.Code != "search_filtered_by_policy"
	}
	return target == ErrSearchResponseTooBig && e.Code == "search_response_too_big" || target == ErrSearchFilteredByPolicy && e.Code == "search_filtered_by_policy"
}

type ClassifiedError struct {
	Code       string
	State      string
	HTTPStatus int
	Retryable  bool
	Cause      error
}

func (e *ClassifiedError) Error() string { return outcomeMessage(e.Code) }
func (e *ClassifiedError) Unwrap() error { return e.Cause }
func (e *ClassifiedError) Is(target error) bool {
	return target == ErrSearchUnavailable && e.Code != "search_filtered_by_policy" || target == ErrSearchFilteredByPolicy && e.Code == "search_filtered_by_policy" || target == ErrSearchResponseTooBig && e.Code == "search_response_too_big"
}

func classified(code, state string, status int, retryable bool) error {
	return &ClassifiedError{Code: code, State: state, HTTPStatus: status, Retryable: retryable}
}

func classifyHTTP(status int, tavily bool) error {
	if status == 429 {
		return classified("provider_rate_limited", "degraded", status, true)
	}
	if tavily && (status == 401 || status == 403 || status == 432 || status == 433) {
		return classified("provider_misconfigured", "misconfigured", status, false)
	}
	if status == 401 || status == 403 {
		return classified("provider_blocked", "blocked", status, false)
	}
	return classified("search_unavailable", "unavailable", status, status >= 500)
}

func outcomeMessage(code string) string {
	switch code {
	case "search_no_results":
		return "Provider returned no results."
	case "provider_blocked":
		return "Provider blocked the request or requires an interactive challenge."
	case "provider_malformed":
		return "Provider returned an unexpected response."
	case "provider_misconfigured":
		return "Provider configuration, credentials, or usage limits require attention."
	case "provider_rate_limited":
		return "Provider rate limit reached."
	case "search_filtered_by_policy":
		return "All results were excluded by search policy."
	case "search_response_too_big":
		return "Provider response exceeded the size limit."
	case "provider_not_checked":
		return "Configured provider has not been checked."
	case "search_ok":
		return "Provider returned results."
	default:
		return "Provider search is unavailable."
	}
}

func providerName(provider SearchProvider) string {
	switch provider.(type) {
	case *SearXNG:
		return "searxng"
	case *Tavily:
		return "tavily"
	case *DuckDuckGo:
		return "duckduckgo"
	default:
		return "search"
	}
}

func makeOutcome(name string, hits []SearchHit, err error, started time.Time) ProviderOutcome {
	o := ProviderOutcome{Provider: name, State: "healthy", Code: "search_ok", HTTPStatus: 200, CheckedAt: time.Now().UTC(), LatencyMS: time.Since(started).Milliseconds(), ResultCount: len(hits)}
	domains := map[string]bool{}
	for _, hit := range hits {
		if u, parseErr := url.Parse(hit.URL); parseErr == nil && u.Hostname() != "" {
			domains[strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))] = true
		}
	}
	o.DistinctDomains = len(domains)
	if err == nil && len(hits) == 0 {
		o.State, o.Code = "degraded", "search_no_results"
	}
	if err != nil {
		o.State, o.Code, o.HTTPStatus, o.Retryable = "unavailable", "search_unavailable", 0, true
		var classifiedErr *ClassifiedError
		if errors.As(err, &classifiedErr) {
			o.State, o.Code, o.HTTPStatus, o.Retryable = classifiedErr.State, classifiedErr.Code, classifiedErr.HTTPStatus, classifiedErr.Retryable
		} else if errors.Is(err, ErrSearchResponseTooBig) {
			o.State, o.Code, o.Retryable = "malformed", "search_response_too_big", false
		} else if errors.Is(err, ErrSearchFilteredByPolicy) {
			o.State, o.Code, o.Retryable = "degraded", "search_filtered_by_policy", false
		}
	}
	o.Message = outcomeMessage(o.Code)
	return o
}

func diagnosticSearch(ctx context.Context, options SearchOptions, provider SearchProvider) (SearchReport, error) {
	started := time.Now()
	metadata := &responseMetadata{}
	ctx = context.WithValue(ctx, responseStatusKey{}, metadata)
	hits, err := provider.Search(ctx, options)
	if ctx.Err() != nil {
		err = ctx.Err()
		hits = nil
	}
	if hits == nil {
		hits = []SearchHit{}
	}
	outcome := makeOutcome(providerName(provider), hits, err, started)
	metadata.mu.Lock()
	if metadata.status != 0 {
		outcome.HTTPStatus = metadata.status
	}
	outcome.BodySize, outcome.ContentType = metadata.bodySize, metadata.contentType
	if err == nil && len(hits) > 0 && metadata.degraded {
		outcome.State, outcome.Code, outcome.Retryable = "degraded", "search_unavailable", true
		outcome.Message = outcomeMessage(outcome.Code)
	}
	metadata.mu.Unlock()
	switch provider.(type) {
	case *SearXNG:
		outcome.ParserVersion = "searxng-json-v1"
	case *Tavily:
		outcome.ParserVersion = "tavily-json-v1"
	case *DuckDuckGo:
		outcome.ParserVersion = "duckduckgo-html-v1"
	}
	return SearchReport{Hits: hits, Outcomes: []ProviderOutcome{outcome}}, err
}

type responseStatusKey struct{}

type responseMetadata struct {
	mu          sync.Mutex
	status      int
	bodySize    int64
	contentType string
	degraded    bool
}

func recordResponseDegraded(ctx context.Context) {
	if destination, ok := ctx.Value(responseStatusKey{}).(*responseMetadata); ok {
		destination.mu.Lock()
		destination.degraded = true
		destination.mu.Unlock()
	}
}

func recordResponseStatus(ctx context.Context, status int) {
	if destination, ok := ctx.Value(responseStatusKey{}).(*responseMetadata); ok {
		destination.mu.Lock()
		destination.status = status
		destination.mu.Unlock()
	}
}

func recordResponseMetadata(ctx context.Context, response *http.Response) {
	recordResponseStatus(ctx, response.StatusCode)
	if destination, ok := ctx.Value(responseStatusKey{}).(*responseMetadata); ok {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
		switch mediaType {
		case "application/json", "text/html", "application/xhtml+xml", "text/plain", "application/octet-stream":
		default:
			mediaType = "unknown"
		}
		destination.mu.Lock()
		destination.contentType = mediaType
		if response.ContentLength > 2<<20 {
			destination.bodySize = (2 << 20) + 1
		}
		destination.mu.Unlock()
	}
}

func recordResponseBodySize(ctx context.Context, size int) {
	if destination, ok := ctx.Value(responseStatusKey{}).(*responseMetadata); ok {
		destination.mu.Lock()
		destination.bodySize = int64(min(max(size, 0), (2<<20)+1))
		destination.mu.Unlock()
	}
}

func (s *SearXNG) SearchWithDiagnostics(ctx context.Context, options SearchOptions) (SearchReport, error) {
	return diagnosticSearch(ctx, options, s)
}

func (t *Tavily) SearchWithDiagnostics(ctx context.Context, options SearchOptions) (SearchReport, error) {
	return diagnosticSearch(ctx, options, t)
}

func (d *DuckDuckGo) SearchWithDiagnostics(ctx context.Context, options SearchOptions) (SearchReport, error) {
	return diagnosticSearch(ctx, options, d)
}
