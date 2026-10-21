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
	s := &LocalPrimarySearch{}
	for _, provider := range []SearchProvider{searxng, tavily, duckduckgo} {
		if provider != nil {
			s.providers = append(s.providers, provider)
		}
	}
	return s
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
	searchCtx, cancel := context.WithTimeout(ctx, providerSearchLimit)
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
			budget = min(8*time.Second, budget*3/5)
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
