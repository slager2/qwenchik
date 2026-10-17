package research

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxSearXNGResponse = 2 << 20

var ErrSearchUnavailable = errors.New("configured search provider is unavailable")
var ErrSearchResponseTooBig = errors.New("search provider response exceeds size limit")

type SearXNG struct {
	baseURL string
	client  *http.Client
}

func NewSearXNG(baseURL string, client *http.Client) (*SearXNG, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("SEARXNG_URL must be an absolute local URL without credentials or query")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("SEARXNG_URL must use http or https")
	}
	if !loopbackConfiguredHost(parsed.Hostname()) {
		return nil, errors.New("SEARXNG_URL must point to localhost or a loopback IP")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &SearXNG{baseURL: strings.TrimRight(baseURL, "/"), client: &copyClient}, nil
}

func (s *SearXNG) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	if s == nil || s.client == nil || s.baseURL == "" {
		return nil, classified("provider_misconfigured", "misconfigured", 0, false)
	}
	query := strings.TrimSpace(options.Query)
	if query == "" || len(query) > 1000 {
		return nil, errors.New("search query must contain 1 to 1000 characters")
	}
	maxResults := options.MaxResults
	if maxResults < 1 || maxResults > 20 {
		maxResults = 8
	}
	language := strings.TrimSpace(options.Language)
	if len(language) > 20 {
		return nil, errors.New("search language exceeds 20 characters")
	}
	requestURL, err := url.Parse(s.baseURL + "/search")
	if err != nil {
		return nil, err
	}
	values := requestURL.Query()
	values.Set("q", query)
	values.Set("format", "json")
	values.Set("safesearch", "1")
	if language != "" {
		values.Set("language", language)
	}
	if options.Recency != "" {
		switch options.Recency {
		case "day", "week", "month", "year":
			values.Set("time_range", options.Recency)
		default:
			return nil, errors.New("recency must be day, week, month, or year")
		}
	}
	requestURL.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "QwenchikLocalResearch/1.0")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, &ClassifiedError{Code: "search_unavailable", State: "unavailable", Retryable: true, Cause: ctx.Err()}
	}
	defer response.Body.Close()
	recordResponseMetadata(ctx, response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classifyHTTP(response.StatusCode, false)
	}
	if response.ContentLength > maxSearXNGResponse {
		return nil, classified("search_response_too_big", "malformed", response.StatusCode, false)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSearXNGResponse+1))
	recordResponseBodySize(ctx, len(body))
	if err != nil {
		return nil, classified("search_unavailable", "unavailable", response.StatusCode, true)
	}
	if len(body) > maxSearXNGResponse {
		return nil, classified("search_response_too_big", "malformed", response.StatusCode, false)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	var envelope struct {
		Results json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	if len(envelope.Results) == 0 || strings.EqualFold(strings.TrimSpace(string(envelope.Results)), "null") {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	var result struct {
		UnresponsiveEngines []json.RawMessage `json:"unresponsive_engines"`
		Results             []struct {
			URL           string  `json:"url"`
			Title         string  `json:"title"`
			Content       string  `json:"content"`
			Engine        string  `json:"engine"`
			Score         float64 `json:"score"`
			PublishedDate string  `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	if result.Results == nil {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	if len(result.Results) == 0 && len(result.UnresponsiveEngines) > 0 {
		return nil, classified("search_unavailable", "degraded", response.StatusCode, true)
	}
	hits := make([]SearchHit, 0, maxResults)
	validResults := 0
	for _, item := range result.Results {
		if len(hits) >= maxResults {
			break
		}
		parsed, err := url.Parse(item.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			continue
		}
		validResults++
		if !matchesDomainAllowlist(parsed.Hostname(), options.Domains) {
			continue
		}
		published, _ := time.Parse(time.RFC3339, item.PublishedDate)
		snippet := strings.TrimSpace(item.Content)
		if len(snippet) > 1000 {
			snippet = snippet[:1000]
		}
		hits = append(hits, SearchHit{Provider: "searxng", Query: query, URL: item.URL, Title: item.Title, Snippet: snippet, Score: item.Score, PublishedAt: published, Taint: TaintExternal})
	}
	if len(hits) == 0 && validResults > 0 {
		return hits, classified("search_filtered_by_policy", "degraded", response.StatusCode, false)
	}
	if len(hits) == 0 && len(result.Results) > 0 {
		return hits, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	if len(hits) > 0 && len(result.UnresponsiveEngines) > 0 {
		recordResponseDegraded(ctx)
	}
	return hits, nil
}

func loopbackConfiguredHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func matchesDomainAllowlist(host string, domains []string) bool {
	if len(domains) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, ".")))
		if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
			return true
		}
	}
	return false
}
