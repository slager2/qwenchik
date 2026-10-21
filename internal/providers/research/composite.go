package research

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slager2/qwenchik/internal/urlcanon"
)

const (
	maxCompositeResults = 20
	providerSearchLimit = 12 * time.Second
)

// CompositeSearch queries configured providers concurrently and merges their
// hits deterministically, preserving useful partial results when one provider
// is unavailable.
type CompositeSearch struct {
	providers []SearchProvider
	mu        sync.RWMutex
	outcomes  []ProviderOutcome
}

type compositeSearchResult struct {
	index    int
	hits     []SearchHit
	err      error
	outcomes []ProviderOutcome
}

func NewCompositeSearch(providers ...SearchProvider) *CompositeSearch {
	configured := make([]SearchProvider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			configured = append(configured, provider)
		}
	}
	return &CompositeSearch{providers: configured}
}

func (c *CompositeSearch) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	report, err := c.SearchWithDiagnostics(ctx, options)
	return report.Hits, err
}

func (c *CompositeSearch) HealthSnapshot() []ProviderOutcome {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.outcomes != nil {
		return append([]ProviderOutcome{}, c.outcomes...)
	}
	outcomes := make([]ProviderOutcome, 0, len(c.providers))
	for _, provider := range c.providers {
		outcomes = append(outcomes, ProviderOutcome{Provider: providerName(provider), State: "configured", Code: "provider_not_checked", Message: outcomeMessage("provider_not_checked")})
	}
	return outcomes
}

func (c *CompositeSearch) SearchWithDiagnostics(ctx context.Context, options SearchOptions) (SearchReport, error) {
	report := SearchReport{Hits: []SearchHit{}, Outcomes: []ProviderOutcome{}}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if options.MaxResults < 1 || options.MaxResults > maxCompositeResults {
		return report, errors.New("search max results must be between 1 and 20")
	}
	if len(c.providers) == 0 {
		return report, &SearchFailure{Code: "search_unavailable", Outcomes: report.Outcomes}
	}
	searchCtx, cancel := context.WithTimeout(ctx, providerSearchLimit)
	defer cancel()
	started := time.Now()
	results := make(chan compositeSearchResult, len(c.providers))
	jobs := make(chan int, len(c.providers))
	for index := range c.providers {
		jobs <- index
	}
	close(jobs)
	for worker := 0; worker < min(4, len(c.providers)); worker++ {
		go func() {
			for index := range jobs {
				if searchCtx.Err() != nil {
					return
				}
				provider := c.providers[index]
				providerCtx, providerCancel := context.WithTimeout(searchCtx, providerSearchLimit)
				var providerReport SearchReport
				var err error
				if diagnostic, ok := provider.(DiagnosticSearchProvider); ok {
					providerReport, err = diagnostic.SearchWithDiagnostics(providerCtx, options)
				} else {
					providerReport, err = diagnosticSearch(providerCtx, options, provider)
				}
				providerCancel()
				results <- compositeSearchResult{index: index, hits: append([]SearchHit(nil), providerReport.Hits...), err: err, outcomes: providerReport.Outcomes}
			}
		}()
	}
	providerResults := make([]compositeSearchResult, len(c.providers))
	received := make([]bool, len(c.providers))
	for remaining := len(c.providers); remaining > 0; remaining-- {
		select {
		case result := <-results:
			providerResults[result.index] = result
			received[result.index] = true
		case <-searchCtx.Done():
			for draining := true; draining; {
				select {
				case result := <-results:
					providerResults[result.index] = result
					received[result.index] = true
				default:
					draining = false
				}
			}
			remaining = 0
		}
	}
	succeeded := false
	oversized := false
	filtered := false
	timedOut := false
	failed := false
	rawCount := 0
	for index := range c.providers {
		if !received[index] {
			providerResults[index] = compositeSearchResult{index: index, err: searchCtx.Err()}
		}
		result := providerResults[index]
		timedOut = timedOut || errors.Is(result.err, context.DeadlineExceeded)
		outcome := makeOutcome(providerName(c.providers[index]), result.hits, result.err, started)
		if len(result.outcomes) > 0 {
			outcome = result.outcomes[0]
			outcome.Provider = providerName(c.providers[index])
			outcome.Message = outcomeMessage(outcome.Code)
		}
		if result.err != nil && len(result.hits) > 0 {
			outcome.State, outcome.Code, outcome.Message = "degraded", "search_partial", outcomeMessage("search_partial")
			outcome.ResultCount = len(result.hits)
		}
		report.Outcomes = append(report.Outcomes, outcome)
		rawCount += len(result.hits)
		if result.err == nil || len(result.hits) > 0 {
			succeeded = true
		} else {
			failed = true
			if errors.Is(result.err, ErrSearchResponseTooBig) {
				oversized = true
			} else if errors.Is(result.err, ErrSearchFilteredByPolicy) {
				filtered = true
			}
		}
	}
	c.mu.Lock()
	c.outcomes = append([]ProviderOutcome(nil), report.Outcomes...)
	c.mu.Unlock()
	report.Hits = mergeSearchHits(providerResults, options)
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if !succeeded {
		code := "search_unavailable"
		if oversized {
			code = "search_response_too_big"
		} else if timedOut {
			code = "search_provider_timeout"
		} else if filtered {
			code = "search_filtered_by_policy"
		}
		var cause error
		if code == "search_provider_timeout" {
			cause = context.DeadlineExceeded
		}
		return report, &SearchFailure{Code: code, Outcomes: append([]ProviderOutcome(nil), report.Outcomes...), Cause: cause}
	}
	if len(report.Hits) == 0 && failed && rawCount == 0 {
		code := "search_unavailable"
		if oversized {
			code = "search_response_too_big"
		} else if timedOut {
			code = "search_provider_timeout"
		} else if filtered {
			code = "search_filtered_by_policy"
		}
		return report, &SearchFailure{Code: code, Outcomes: append([]ProviderOutcome(nil), report.Outcomes...)}
	}
	if len(report.Hits) == 0 && rawCount > 0 {
		for index := range report.Outcomes {
			if report.Outcomes[index].ResultCount > 0 {
				report.Outcomes[index].State, report.Outcomes[index].Code, report.Outcomes[index].Message = "degraded", "search_filtered_by_policy", outcomeMessage("search_filtered_by_policy")
			}
		}
		c.mu.Lock()
		c.outcomes = append([]ProviderOutcome(nil), report.Outcomes...)
		c.mu.Unlock()
		return report, &SearchFailure{Code: "search_filtered_by_policy", Outcomes: append([]ProviderOutcome(nil), report.Outcomes...)}
	}
	return report, nil
}

func mergeSearchHits(providerResults []compositeSearchResult, options SearchOptions) []SearchHit {
	if len(options.PreferredDomains) > 0 {
		for providerIndex := range providerResults {
			sort.SliceStable(providerResults[providerIndex].hits, func(i, j int) bool {
				return preferredSearchURL(providerResults[providerIndex].hits[i].URL, options.PreferredDomains) &&
					!preferredSearchURL(providerResults[providerIndex].hits[j].URL, options.PreferredDomains)
			})
		}
	}
	canonicalByProvider := make([][]string, len(providerResults))
	for providerIndex, result := range providerResults {
		for _, hit := range result.hits {
			parsed, err := url.Parse(hit.URL)
			if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
				canonicalByProvider[providerIndex] = append(canonicalByProvider[providerIndex], "")
				continue
			}
			if !matchesDomainAllowlist(parsed.Hostname(), options.Domains) {
				canonicalByProvider[providerIndex] = append(canonicalByProvider[providerIndex], "")
				continue
			}
			canonical, err := urlcanon.Canonical(hit.URL)
			if err != nil {
				canonicalByProvider[providerIndex] = append(canonicalByProvider[providerIndex], "")
				continue
			}
			canonicalByProvider[providerIndex] = append(canonicalByProvider[providerIndex], canonical)
		}
	}

	merged := make([]SearchHit, 0, options.MaxResults)
	seen := make(map[string]struct{}, options.MaxResults)
	for offset := 0; ; offset++ {
		foundCandidate := false
		for providerIndex, result := range providerResults {
			if offset >= len(result.hits) {
				continue
			}
			foundCandidate = true
			canonical := canonicalByProvider[providerIndex][offset]
			if canonical == "" {
				continue
			}
			if _, exists := seen[canonical]; exists {
				continue
			}
			seen[canonical] = struct{}{}
			hit := result.hits[offset]
			hit.Taint = TaintExternal
			merged = append(merged, hit)
			if len(merged) == options.MaxResults {
				return merged
			}
		}
		if !foundCandidate {
			return merged
		}
	}
}

func preferredSearchURL(rawURL string, domains []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, ".")))
		if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
			return true
		}
	}
	return false
}
