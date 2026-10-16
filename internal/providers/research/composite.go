package research

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
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
}

type compositeSearchResult struct {
	index int
	hits  []SearchHit
	err   error
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.MaxResults < 1 || options.MaxResults > maxCompositeResults {
		return nil, errors.New("search max results must be between 1 and 20")
	}
	if len(c.providers) == 0 {
		return nil, ErrSearchUnavailable
	}

	results := make(chan compositeSearchResult, len(c.providers))
	for index, provider := range c.providers {
		go func(index int, provider SearchProvider) {
			providerCtx, cancel := context.WithTimeout(ctx, providerSearchLimit)
			defer cancel()
			hits, err := provider.Search(providerCtx, options)
			results <- compositeSearchResult{index: index, hits: hits, err: err}
		}(index, provider)
	}

	providerResults := make([]compositeSearchResult, len(c.providers))
	succeeded := false
	oversized := false
	for range c.providers {
		result := <-results
		providerResults[result.index] = result
		if result.err == nil {
			succeeded = true
		} else if errors.Is(result.err, ErrSearchResponseTooBig) {
			oversized = true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !succeeded {
		if oversized {
			return nil, ErrSearchResponseTooBig
		}
		return nil, ErrSearchUnavailable
	}

	return mergeSearchHits(providerResults, options), nil
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
