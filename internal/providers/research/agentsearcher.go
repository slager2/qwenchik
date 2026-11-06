package research

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// agentSearcher is an optional, separately installed local service
// (https://github.com/peakQuick/agentSearcher, AGPL-3.0) that exposes a
// Tavily-compatible HTTP API. It searches public engines, fetches and renders
// the result pages, and returns reranked passages per page. qagent only talks
// to it over loopback HTTP; no agentSearcher code is part of this module.
const (
	agentSearcherMaxQueryRunes  = 400
	agentSearcherMaxResults     = 20
	maxAgentSearcherResponse    = 2 << 20
	agentSearcherMaxContent     = 4000
	AgentSearcherDefaultDepth   = "advanced"
	AgentSearcherDefaultTimeout = 45 * time.Second
	agentSearcherMaxTimeout     = 120 * time.Second
	agentSearcherChunksPerPage  = 3
)

type AgentSearcher struct {
	baseURL string
	depth   string
	timeout time.Duration
	client  *http.Client
}

type agentSearcherRequest struct {
	Query                string   `json:"query"`
	SearchDepth          string   `json:"search_depth"`
	MaxResults           int      `json:"max_results"`
	ChunksPerSource      int      `json:"chunks_per_source"`
	IncludeDomains       []string `json:"include_domains,omitempty"`
	IncludeDomainsMode   string   `json:"include_domains_mode,omitempty"`
	TimeRange            string   `json:"time_range,omitempty"`
	Language             string   `json:"language,omitempty"`
	SafeSearch           bool     `json:"safe_search"`
	IncludePublishedDate bool     `json:"include_published_date"`
	IncludeAnswer        bool     `json:"include_answer"`
	IncludeRawContent    bool     `json:"include_raw_content"`
}

type agentSearcherResponse struct {
	Results []struct {
		URL           string  `json:"url"`
		Title         string  `json:"title"`
		Content       string  `json:"content"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"published_date"`
	} `json:"results"`
}

// ValidAgentSearcherDepth reports whether depth is a search_depth value the
// agentSearcher API accepts.
func ValidAgentSearcherDepth(depth string) bool {
	switch depth {
	case "ultra-fast", "fast", "basic", "advanced":
		return true
	}
	return false
}

// NewAgentSearcher creates a provider for a loopback agentSearcher service.
// The base URL must point to localhost or a loopback IP, like SEARXNG_URL.
// Requests never use an environment proxy and never follow redirects.
func NewAgentSearcher(baseURL, depth string, timeout time.Duration, client *http.Client) (*AgentSearcher, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("AGENTSEARCHER_URL must be an absolute local URL without credentials or query")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("AGENTSEARCHER_URL must use http or https")
	}
	if !loopbackConfiguredHost(parsed.Hostname()) {
		return nil, errors.New("AGENTSEARCHER_URL must point to localhost or a loopback IP")
	}
	if depth = strings.TrimSpace(depth); depth == "" {
		depth = AgentSearcherDefaultDepth
	}
	if !ValidAgentSearcherDepth(depth) {
		return nil, errors.New("AGENTSEARCHER_SEARCH_DEPTH must be ultra-fast, fast, basic, or advanced")
	}
	if timeout <= 0 {
		timeout = AgentSearcherDefaultTimeout
	}
	if timeout > agentSearcherMaxTimeout {
		return nil, errors.New("AGENTSEARCHER_TIMEOUT_SECONDS must not exceed 120")
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	clientCopy := *client
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > timeout {
		clientCopy.Timeout = timeout
	}
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &AgentSearcher{baseURL: baseURL, depth: depth, timeout: timeout, client: &clientCopy}, nil
}

// SearchBudget is the collection time this provider needs. agentSearcher
// fetches and reranks result pages before it answers, so it is slower than a
// snippet-only search engine.
func (a *AgentSearcher) SearchBudget() time.Duration {
	if a == nil || a.timeout <= 0 {
		return providerSearchLimit
	}
	return a.timeout
}

func (a *AgentSearcher) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	if a == nil || a.client == nil || a.baseURL == "" {
		return nil, classified("provider_misconfigured", "misconfigured", 0, false)
	}
	query := strings.TrimSpace(options.Query)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > agentSearcherMaxQueryRunes {
		return nil, errors.New("agentSearcher query must contain 1 to 400 characters")
	}
	if options.MaxResults < 1 || options.MaxResults > agentSearcherMaxResults {
		return nil, errors.New("agentSearcher max results must be between 1 and 20")
	}
	timeRange, err := tavilyTimeRange(options.Recency)
	if err != nil {
		return nil, err
	}
	if len(options.Language) > 20 {
		return nil, errors.New("search language exceeds 20 characters")
	}
	domains := make([]string, 0, len(options.Domains)+len(options.PreferredDomains))
	domainMode := ""
	domainSource := options.Domains
	if len(domainSource) > 0 {
		domainMode = "restrict"
	} else if len(options.PreferredDomains) > 0 {
		domainSource = options.PreferredDomains
		domainMode = "prefer"
	}
	for _, domain := range domainSource {
		if domain = strings.TrimSpace(domain); domain != "" {
			domains = append(domains, domain)
		}
	}
	requestBody, err := json.Marshal(agentSearcherRequest{
		Query: query, SearchDepth: a.depth, MaxResults: options.MaxResults,
		ChunksPerSource: agentSearcherChunksPerPage,
		IncludeDomains:  domains, IncludeDomainsMode: domainMode,
		TimeRange: timeRange, Language: strings.TrimSpace(options.Language),
		SafeSearch: options.SafeSearch, IncludePublishedDate: true,
	})
	if err != nil {
		return nil, errors.New("agentSearcher request could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/search", bytes.NewReader(requestBody))
	if err != nil {
		return nil, errors.New("agentSearcher request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "QwenchikLocalResearch/1.0")
	response, err := a.client.Do(request)
	if err != nil {
		cause := ctx.Err()
		if cause == nil && errors.Is(err, context.DeadlineExceeded) {
			cause = context.DeadlineExceeded
		}
		return nil, &ClassifiedError{Code: "search_unavailable", State: "unavailable", Retryable: true, Cause: cause}
	}
	defer response.Body.Close()
	recordResponseMetadata(ctx, response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classifyHTTP(response.StatusCode, false)
	}
	if response.ContentLength > maxAgentSearcherResponse {
		return nil, classified("search_response_too_big", "malformed", response.StatusCode, false)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAgentSearcherResponse+1))
	recordResponseBodySize(ctx, len(body))
	if err != nil {
		return nil, classified("search_unavailable", "unavailable", response.StatusCode, true)
	}
	if len(body) > maxAgentSearcherResponse {
		return nil, classified("search_response_too_big", "malformed", response.StatusCode, false)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	var result agentSearcherResponse
	if err := json.Unmarshal(body, &result); err != nil || result.Results == nil {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	hits := make([]SearchHit, 0, options.MaxResults)
	validResults := 0
	for _, item := range result.Results {
		if len(hits) >= options.MaxResults {
			break
		}
		parsedURL, err := parseTavilyResultURL(item.URL)
		if err != nil {
			continue
		}
		validResults++
		if !matchesDomainAllowlist(parsedURL.Hostname(), options.Domains) {
			continue
		}
		score := item.Score
		if score < 0 || score > 1 {
			score = 0
		}
		hits = append(hits, SearchHit{
			Provider:    "agentsearcher",
			Query:       query,
			URL:         parsedURL.String(),
			Title:       truncateTavilyText(strings.TrimSpace(item.Title), 500),
			Snippet:     truncateTavilyText(strings.TrimSpace(item.Content), agentSearcherMaxContent),
			Score:       score,
			PublishedAt: parseTavilyDate(item.PublishedDate),
			Taint:       TaintExternal,
		})
	}
	if len(hits) == 0 && validResults > 0 {
		return hits, classified("search_filtered_by_policy", "degraded", response.StatusCode, false)
	}
	if len(hits) == 0 && len(result.Results) > 0 {
		return hits, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	return hits, nil
}
