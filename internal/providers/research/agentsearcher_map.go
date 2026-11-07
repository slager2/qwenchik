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

// agentSearcher's POST /map walks a site from a root URL (llms.txt, sitemap
// and links, breadth first) and returns the discovered URLs, ranked by BM25
// relevance of their link text and path to the instructions:
//
//	{"base_url": ..., "results": ["https://...", ...], "failed_results": [...]}
//
// qagent uses it to find the key pages and PDFs (calls for applications,
// calendars, guidelines) of official sites that search hits only touched.
const (
	AgentSearcherMapDepth = 2
	AgentSearcherMapLimit = 30
	// AgentSearcherMapTimeout is the walk budget sent to the service (its
	// own bounds are 10..150 seconds); the HTTP request gets a little more.
	AgentSearcherMapTimeout    = 60 * time.Second
	agentSearcherMapOverhead   = 10 * time.Second
	maxAgentSearcherMapBody    = 1 << 20
	maxAgentSearcherMapResults = 200
	agentSearcherMaxInstrRunes = 300
)

// AgentSearcherMapper discovers site URLs through a loopback agentSearcher
// service's /map endpoint.
type AgentSearcherMapper struct {
	baseURL string
	timeout time.Duration
	client  *http.Client
}

// NewAgentSearcherMapper creates a /map client for a loopback agentSearcher
// service, validated like NewAgentSearcher. timeout is the walk budget
// (default AgentSearcherMapTimeout, 10..90 seconds).
func NewAgentSearcherMapper(baseURL string, timeout time.Duration, client *http.Client) (*AgentSearcherMapper, error) {
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
	if timeout <= 0 {
		timeout = AgentSearcherMapTimeout
	}
	timeout = min(max(timeout, 10*time.Second), 90*time.Second)
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	clientCopy := *client
	clientCopy.Timeout = 0
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &AgentSearcherMapper{baseURL: baseURL, timeout: timeout, client: &clientCopy}, nil
}

// Budget is the longest time Map may take.
func (a *AgentSearcherMapper) Budget() time.Duration {
	if a == nil {
		return 0
	}
	return a.timeout + agentSearcherMapOverhead
}

type agentSearcherMapRequest struct {
	URL           string  `json:"url"`
	Instructions  string  `json:"instructions,omitempty"`
	MaxDepth      int     `json:"max_depth"`
	Limit         int     `json:"limit"`
	AllowExternal bool    `json:"allow_external"`
	Timeout       float64 `json:"timeout"`
}

// Map returns the URLs agentSearcher discovered on the site of rawURL (same
// site only), most relevant to instructions first. The caller validates
// rawURL and every returned URL against its own URL policy.
func (a *AgentSearcherMapper) Map(ctx context.Context, rawURL, instructions string) ([]string, error) {
	if a == nil || a.client == nil || a.baseURL == "" {
		return nil, classified("provider_misconfigured", "misconfigured", 0, false)
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || len(rawURL) > 2048 {
		return nil, errors.New("agentSearcher map URL must be a public http(s) URL")
	}
	instructions = strings.Join(strings.Fields(instructions), " ")
	if !utf8.ValidString(instructions) {
		instructions = ""
	}
	if utf8.RuneCountInString(instructions) > agentSearcherMaxInstrRunes {
		instructions = string([]rune(instructions)[:agentSearcherMaxInstrRunes])
	}
	body, err := json.Marshal(agentSearcherMapRequest{
		URL: parsed.String(), Instructions: instructions, MaxDepth: AgentSearcherMapDepth,
		Limit: AgentSearcherMapLimit, AllowExternal: false, Timeout: a.timeout.Seconds(),
	})
	if err != nil {
		return nil, errors.New("agentSearcher map request could not be encoded")
	}
	requestCtx, cancel := context.WithTimeout(ctx, a.Budget())
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, a.baseURL+"/map", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("agentSearcher map request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "QwenchikLocalResearch/1.0")
	response, err := a.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cause := requestCtx.Err()
		return nil, &ClassifiedError{Code: "map_unavailable", State: "unavailable", Retryable: true, Cause: cause}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classified("map_unavailable", "unavailable", response.StatusCode, response.StatusCode >= 500)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAgentSearcherMapBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classified("map_unavailable", "unavailable", response.StatusCode, true)
	}
	if len(raw) > maxAgentSearcherMapBody {
		return nil, classified("search_response_too_big", "malformed", response.StatusCode, false)
	}
	var result struct {
		Results []string `json:"results"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Results == nil {
		return nil, classified("provider_malformed", "malformed", response.StatusCode, false)
	}
	root := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	urls := []string{}
	seen := map[string]bool{}
	for _, item := range result.Results {
		if len(urls) == maxAgentSearcherMapResults {
			break
		}
		candidate, err := parseTavilyResultURL(item)
		if err != nil {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(candidate.Hostname()), "www.")
		// allow_external=false keeps the walk on the site; check it anyway.
		if host != root && !strings.HasSuffix(host, "."+root) && !strings.HasSuffix(root, "."+host) {
			continue
		}
		if value := candidate.String(); !seen[value] {
			seen[value] = true
			urls = append(urls, value)
		}
	}
	return urls, nil
}
