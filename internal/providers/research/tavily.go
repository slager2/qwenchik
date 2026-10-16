package research

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	tavilyEndpoint      = "https://api.tavily.com/search"
	tavilyMaxQueryRunes = 400
	tavilyMaxResults    = 20
	maxTavilyResponse   = 2 << 20
	tavilyMaxTimeout    = 10 * time.Second
)

var ErrTavilyResponseTooBig = errors.New("Tavily response exceeds size limit")

type tavilyResponseTooBigError struct{}

func (tavilyResponseTooBigError) Error() string { return ErrTavilyResponseTooBig.Error() }
func (tavilyResponseTooBigError) Is(target error) bool {
	return target == ErrTavilyResponseTooBig || target == ErrSearchResponseTooBig
}

var errTavilyResponseTooBig error = tavilyResponseTooBigError{}

type Tavily struct {
	apiKey string
	client *http.Client
}

type tavilySearchRequest struct {
	Query                string   `json:"query"`
	SearchDepth          string   `json:"search_depth"`
	MaxResults           int      `json:"max_results"`
	IncludeDomains       []string `json:"include_domains,omitempty"`
	IncludeDomainsMode   string   `json:"include_domains_mode,omitempty"`
	TimeRange            string   `json:"time_range,omitempty"`
	Language             string   `json:"language,omitempty"`
	SafeSearch           bool     `json:"safe_search"`
	IncludePublishedDate bool     `json:"include_published_date"`
	IncludeAnswer        bool     `json:"include_answer"`
	IncludeRawContent    bool     `json:"include_raw_content"`
}

type tavilySearchResponse struct {
	Results []struct {
		URL           string  `json:"url"`
		Title         string  `json:"title"`
		Content       string  `json:"content"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"published_date"`
	} `json:"results"`
}

// NewTavily creates a search provider using the fixed Tavily HTTPS endpoint.
// A supplied client's transport and other settings are preserved, while its
// timeout is capped to keep searches bounded.
func NewTavily(apiKey string, client *http.Client) (*Tavily, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("Tavily API key is required")
	}
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > tavilyMaxTimeout {
		clientCopy.Timeout = tavilyMaxTimeout
	}
	// Do not forward the authorization header to a redirect.
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Tavily{apiKey: apiKey, client: &clientCopy}, nil
}

func (t *Tavily) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	query := strings.TrimSpace(options.Query)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > tavilyMaxQueryRunes {
		return nil, errors.New("Tavily query must contain 1 to 400 characters")
	}
	if options.MaxResults < 1 || options.MaxResults > tavilyMaxResults {
		return nil, errors.New("Tavily max results must be between 1 and 20")
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
	requestBody, err := json.Marshal(tavilySearchRequest{
		Query: query, SearchDepth: "basic", MaxResults: options.MaxResults,
		IncludeDomains: domains, IncludeDomainsMode: domainMode,
		TimeRange: timeRange, Language: strings.TrimSpace(options.Language),
		SafeSearch: options.SafeSearch, IncludePublishedDate: true,
		IncludeAnswer: false, IncludeRawContent: false,
	})
	if err != nil {
		return nil, errors.New("Tavily request could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyEndpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, errors.New("Tavily request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "QwenchikLocalResearch/1.0")
	request.Header.Set("Authorization", "Bearer "+t.apiKey)

	response, err := t.client.Do(request)
	if err != nil {
		// Transport errors can include request details; keep the API key and
		// other potentially sensitive data out of returned errors.
		return nil, tavilyUnavailable("request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, tavilyUnavailable("returned a non-success HTTP status")
	}
	if response.ContentLength > maxTavilyResponse {
		return nil, errTavilyResponseTooBig
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTavilyResponse+1))
	if err != nil {
		return nil, tavilyUnavailable("response could not be read")
	}
	if len(body) > maxTavilyResponse {
		return nil, errTavilyResponseTooBig
	}
	var result tavilySearchResponse
	if err := json.Unmarshal(body, &result); err != nil || result.Results == nil {
		return nil, tavilyUnavailable("returned a malformed response")
	}

	hits := make([]SearchHit, 0, options.MaxResults)
	for _, item := range result.Results {
		if len(hits) >= options.MaxResults {
			break
		}
		parsedURL, err := parseTavilyResultURL(item.URL)
		if err != nil || !matchesDomainAllowlist(parsedURL.Hostname(), options.Domains) {
			continue
		}
		score := item.Score
		if score < 0 || score > 1 {
			score = 0
		}
		hits = append(hits, SearchHit{
			Provider:    "tavily",
			Query:       query,
			URL:         parsedURL.String(),
			Title:       truncateTavilyText(strings.TrimSpace(item.Title), 500),
			Snippet:     truncateTavilyText(strings.TrimSpace(item.Content), 1000),
			Score:       score,
			PublishedAt: parseTavilyDate(item.PublishedDate),
			Taint:       TaintExternal,
		})
	}
	return hits, nil
}

func tavilyUnavailable(reason string) error {
	return fmt.Errorf("%w: Tavily search %s", ErrSearchUnavailable, reason)
}

func tavilyTimeRange(recency string) (string, error) {
	switch recency {
	case "":
		return "", nil
	case "day", "week", "month", "year":
		return recency, nil
	default:
		return "", errors.New("recency must be day, week, month, or year")
	}
}

func parseTavilyResultURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("invalid Tavily result URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("invalid Tavily result URL")
	}
	return parsed, nil
}

func parseTavilyDate(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02", time.RFC1123, time.RFC1123Z} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func truncateTavilyText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}
