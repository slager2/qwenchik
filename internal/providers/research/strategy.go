package research

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

type LocalPrimarySearch struct {
	providers []SearchProvider
	mu        sync.RWMutex
	outcomes  []ProviderOutcome
}

func NewLocalPrimarySearch(searxng, tavily, duckduckgo SearchProvider) *LocalPrimarySearch {
	return NewOrderedPrimarySearch(searxng, tavily, duckduckgo)
}

// NewOrderedPrimarySearch queries providers in the given order and stops as
// soon as the merged hits cover two independent sites; later providers are
// fallbacks for errors, empty answers, or single-site answers.
func NewOrderedPrimarySearch(providers ...SearchProvider) *LocalPrimarySearch {
	s := &LocalPrimarySearch{}
	for _, provider := range providers {
		if provider != nil {
			s.providers = append(s.providers, provider)
		}
	}
	return s
}

// fallbackSearchReserve is the time kept for snippet-only fallbacks after a
// slow primary provider with its own budget (agentSearcher) fails.
const fallbackSearchReserve = 8 * time.Second

// SearchBudget covers the default sequential budget plus the extra time that
// slow providers with their own budget declare.
func (s *LocalPrimarySearch) SearchBudget() time.Duration {
	budget := providerSearchLimit
	for index, provider := range s.providers {
		if extra := SearchTimeBudget(provider); extra > providerSearchLimit {
			budget += extra - providerSearchLimit
			if index+1 < len(s.providers) {
				budget = max(budget, extra+fallbackSearchReserve)
			}
		}
	}
	return budget
}

func (s *LocalPrimarySearch) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	report, err := s.SearchWithDiagnostics(ctx, options)
	return report.Hits, err
}

func (s *LocalPrimarySearch) HealthSnapshot() []ProviderOutcome {
	s.mu.RLock()
	defer s.mu.RUnlock()
	outcomes := make([]ProviderOutcome, 0, len(s.providers))
	for _, provider := range s.providers {
		outcomes = append(outcomes, ProviderOutcome{Provider: providerName(provider), State: "configured", Code: "provider_not_checked", Message: outcomeMessage("provider_not_checked")})
	}
	for _, observed := range s.outcomes {
		for i := range outcomes {
			if outcomes[i].Provider == observed.Provider {
				outcomes[i] = observed
				break
			}
		}
	}
	return outcomes
}

func (s *LocalPrimarySearch) SearchWithDiagnostics(ctx context.Context, options SearchOptions) (SearchReport, error) {
	report := SearchReport{Hits: []SearchHit{}, Outcomes: []ProviderOutcome{}}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if options.MaxResults < 1 || options.MaxResults > maxCompositeResults {
		return report, errors.New("search max results must be between 1 and 20")
	}
	searchCtx, cancel := context.WithTimeout(ctx, s.SearchBudget())
	defer cancel()
	var failures []error
	allHits := []SearchHit{}
	for index, provider := range s.providers {
		if searchCtx.Err() != nil {
			failures = append(failures, searchCtx.Err())
			break
		}
		deadline, _ := searchCtx.Deadline()
		budget := time.Until(deadline)
		if index+1 < len(s.providers) {
			if own := SearchTimeBudget(provider); own > providerSearchLimit && budget > 2*fallbackSearchReserve {
				// A slow provider with its own budget keeps a reserve for
				// the snippet-only fallbacks that follow it.
				budget = min(own, budget-fallbackSearchReserve)
			} else {
				budget = min(8*time.Second, budget*3/5)
			}
		}
		childCtx, childCancel := context.WithTimeout(searchCtx, budget)
		attempt, err := NewCompositeSearch(provider).SearchWithDiagnostics(childCtx, options)
		childCancel()
		report.Outcomes = append(report.Outcomes, attempt.Outcomes...)
		allHits = append(allHits, attempt.Hits...)
		report.Hits = mergeSearchHits([]compositeSearchResult{{hits: allHits}}, options)
		if err != nil {
			failures = append(failures, err)
		}
		if independentSearchDomains(report.Hits) >= 2 {
			break
		}
	}
	s.mu.Lock()
	s.outcomes = append([]ProviderOutcome(nil), report.Outcomes...)
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if len(report.Hits) > 0 {
		return report, nil
	}
	// A valid empty response is not the same as an outage. When a primary
	// provider is down but a later provider answered successfully with no
	// usable hits, keep the provider diagnostics but return a non-fatal
	// no-results outcome. The research worker can then issue another
	// diversified query instead of terminating the whole run as unavailable.
	sawNoResults := false
	for _, outcome := range report.Outcomes {
		if outcome.Code == "search_no_results" {
			sawNoResults = true
			break
		}
	}
	if sawNoResults && len(failures) > 0 {
		return report, &SearchFailure{Code: "search_no_results", Outcomes: append([]ProviderOutcome(nil), report.Outcomes...)}
	}
	if len(s.providers) == 0 || len(failures) > 0 {
		code := "search_unavailable"
		var cause error
		for _, failure := range failures {
			if errors.Is(failure, ErrSearchResponseTooBig) {
				code = "search_response_too_big"
				break
			}
			if errors.Is(failure, context.DeadlineExceeded) {
				code, cause = "search_provider_timeout", context.DeadlineExceeded
			} else if code == "search_unavailable" && errors.Is(failure, ErrSearchFilteredByPolicy) {
				code = "search_filtered_by_policy"
			}
		}
		return report, &SearchFailure{Code: code, Cause: cause, Outcomes: append([]ProviderOutcome(nil), report.Outcomes...)}
	}
	return report, nil
}

func independentSearchDomains(hits []SearchHit) int {
	domains := make(map[string]struct{})
	for _, hit := range hits {
		parsed, err := url.Parse(hit.URL)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
		if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
			host = domain
		}
		domains[host] = struct{}{}
	}
	return len(domains)
}
