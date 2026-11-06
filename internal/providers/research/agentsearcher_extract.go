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

// agentSearcher's POST /extract downloads a page with browser TLS
// impersonation, falls back to a headless browser for challenge and scripted
// pages, and extracts HTML and PDF text. Its response is
//
//	{"results": [{"url": ..., "raw_content": ...}],
//	 "failed_results": [{"url": ..., "error": ...}],
//	 "response_time": ..., "request_id": ...}
//
// Without a query raw_content is the whole cleaned page text (cut at the
// service's MAX_RAW_CHARS, 20000 characters by default). With a query it is
// only the query-ranked chunks of the page joined by " [...] ". The client
// therefore asks twice: once for the page text and once, served from the
// service's page cache, for the ranked chunks that locate the excerpt.
const (
	AgentSearcherExtractDepth       = "advanced"
	AgentSearcherExtractFormat      = "text"
	AgentSearcherExtractChunks      = 5
	agentSearcherChunkSeparator     = " [...] "
	maxAgentSearcherExtractResponse = 1 << 20
	// agentSearcherMaxExtractRunes bounds the accepted page text. The
	// service cuts raw_content at 20000 characters by default; a larger
	// local setting is accepted up to this cap.
	agentSearcherMaxExtractRunes = 200_000
	// agentSearcherPageTimeout is the per-page load budget sent to the
	// service (its own limit is 1..60 seconds).
	agentSearcherPageTimeout     = 30 * time.Second
	agentSearcherMaxPageTimeout  = 60 * time.Second
	agentSearcherRequestOverhead = 10 * time.Second
	agentSearcherChunksTimeout   = 20 * time.Second
	// agentSearcherPDFPrefix starts the text of an extracted PDF: the
	// service writes one "## Page N" section per PDF page, and the text
	// format strips the heading marks.
	agentSearcherPDFPrefix = "Page 1\n\n"
)

// AgentSearcherExtractor reads pages through a loopback agentSearcher
// service's /extract endpoint.
type AgentSearcherExtractor struct {
	baseURL     string
	pageTimeout time.Duration
	client      *http.Client
}

// AgentSearcherPage is the text agentSearcher extracted from one URL.
type AgentSearcherPage struct {
	URL string
	// Text is raw_content of the full-page request: the cleaned page text.
	Text string
	// Chunks are the query-ranked chunks of the page, best first; empty
	// without a query or when the ranking request failed.
	Chunks []string
	// PDF reports that the text came from a PDF text layer.
	PDF bool
	// TextResponse and ChunksResponse are the service's JSON responses,
	// kept as the raw provenance record.
	TextResponse   json.RawMessage
	ChunksResponse json.RawMessage
}

// AgentSearcherExtractError classifies a failed extraction. Code is one of
// extract_unavailable (service unreachable, busy or 5xx), extract_timeout,
// extract_rejected (4xx request error), extract_malformed,
// extract_response_too_big, extract_source_unavailable (the site answered
// with an HTTP error), extract_blocked (anti-bot challenge), extract_empty
// (no text) and extract_failed (other per-URL failures).
type AgentSearcherExtractError struct {
	Code       string
	HTTPStatus int
	// Reason is the service's per-URL error text, bounded and single-line.
	Reason string
	Cause  error
}

func (e *AgentSearcherExtractError) Error() string {
	if e.Reason != "" {
		return "agentSearcher extract failed: " + e.Code + ": " + e.Reason
	}
	return "agentSearcher extract failed: " + e.Code
}

func (e *AgentSearcherExtractError) Unwrap() error { return e.Cause }

// NewAgentSearcherExtractor creates an extract client for a loopback
// agentSearcher service, validated like NewAgentSearcher. pageTimeout is the
// per-page load budget the service receives (default 30 s, at most 60 s).
func NewAgentSearcherExtractor(baseURL string, pageTimeout time.Duration, client *http.Client) (*AgentSearcherExtractor, error) {
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
	if pageTimeout <= 0 {
		pageTimeout = agentSearcherPageTimeout
	}
	pageTimeout = min(pageTimeout, agentSearcherMaxPageTimeout)
	if pageTimeout < time.Second {
		pageTimeout = time.Second
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	clientCopy := *client
	clientCopy.Timeout = 0 // each request carries its own deadline
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &AgentSearcherExtractor{baseURL: baseURL, pageTimeout: pageTimeout, client: &clientCopy}, nil
}

// Budget is the longest time Extract may take: the page request and the
// ranked-chunk request.
func (a *AgentSearcherExtractor) Budget() time.Duration {
	if a == nil {
		return 0
	}
	return a.pageTimeout + agentSearcherRequestOverhead + agentSearcherChunksTimeout
}

type agentSearcherExtractRequest struct {
	URLs            []string `json:"urls"`
	Query           string   `json:"query,omitempty"`
	ChunksPerSource int      `json:"chunks_per_source"`
	ExtractDepth    string   `json:"extract_depth"`
	Format          string   `json:"format"`
	Timeout         float64  `json:"timeout"`
}

type agentSearcherExtractResponse struct {
	Results []struct {
		URL        string  `json:"url"`
		RawContent *string `json:"raw_content"`
	} `json:"results"`
	FailedResults []struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	} `json:"failed_results"`
}

// Extract reads one public page or PDF through agentSearcher. The caller
// must already have validated rawURL against its own URL policy (SSRF,
// allow-lists): agentSearcher is asked only for URLs the backend accepts.
// query, when not empty, ranks up to five chunks of the page.
func (a *AgentSearcherExtractor) Extract(ctx context.Context, rawURL, query string) (AgentSearcherPage, error) {
	if a == nil || a.client == nil || a.baseURL == "" {
		return AgentSearcherPage{}, &AgentSearcherExtractError{Code: "extract_unavailable"}
	}
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || len(rawURL) > 4096 {
		return AgentSearcherPage{}, &AgentSearcherExtractError{Code: "extract_rejected", Reason: "unsupported URL"}
	}
	query = strings.TrimSpace(query)
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > agentSearcherMaxQueryRunes {
		query = ""
	}
	body, err := a.post(ctx, agentSearcherExtractRequest{
		URLs: []string{rawURL}, ChunksPerSource: AgentSearcherExtractChunks,
		ExtractDepth: AgentSearcherExtractDepth, Format: AgentSearcherExtractFormat,
		Timeout: a.pageTimeout.Seconds(),
	}, a.pageTimeout+agentSearcherRequestOverhead)
	if err != nil {
		return AgentSearcherPage{}, err
	}
	text, err := agentSearcherExtractText(body, rawURL)
	if err != nil {
		return AgentSearcherPage{}, err
	}
	page := AgentSearcherPage{URL: rawURL, Text: text, PDF: strings.HasPrefix(text, agentSearcherPDFPrefix), TextResponse: body}
	if query == "" {
		return page, nil
	}
	// The page is in the service's cache now, so the ranked-chunk request
	// only reranks. Its failure leaves the page text usable.
	chunksBody, err := a.post(ctx, agentSearcherExtractRequest{
		URLs: []string{rawURL}, Query: query, ChunksPerSource: AgentSearcherExtractChunks,
		ExtractDepth: AgentSearcherExtractDepth, Format: AgentSearcherExtractFormat,
		Timeout: a.pageTimeout.Seconds(),
	}, agentSearcherChunksTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return AgentSearcherPage{}, err
		}
		return page, nil
	}
	if ranked, err := agentSearcherExtractText(chunksBody, rawURL); err == nil {
		for _, chunk := range strings.Split(ranked, agentSearcherChunkSeparator) {
			if chunk = strings.TrimSpace(chunk); chunk != "" && len(page.Chunks) < AgentSearcherExtractChunks {
				page.Chunks = append(page.Chunks, chunk)
			}
		}
		page.ChunksResponse = chunksBody
	}
	return page, nil
}

func (a *AgentSearcherExtractor) post(ctx context.Context, payload agentSearcherExtractRequest, timeout time.Duration) ([]byte, error) {
	requestBody, err := json.Marshal(payload)
	if err != nil {
		return nil, &AgentSearcherExtractError{Code: "extract_rejected", Cause: err}
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, a.baseURL+"/extract", bytes.NewReader(requestBody))
	if err != nil {
		return nil, &AgentSearcherExtractError{Code: "extract_rejected", Cause: err}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "QwenchikLocalResearch/1.0")
	response, err := a.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if requestCtx.Err() != nil {
			return nil, &AgentSearcherExtractError{Code: "extract_timeout", Cause: context.DeadlineExceeded}
		}
		return nil, &AgentSearcherExtractError{Code: "extract_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "extract_unavailable"
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
			code = "extract_rejected"
		}
		return nil, &AgentSearcherExtractError{Code: code, HTTPStatus: response.StatusCode}
	}
	if response.ContentLength > maxAgentSearcherExtractResponse {
		return nil, &AgentSearcherExtractError{Code: "extract_response_too_big", HTTPStatus: response.StatusCode}
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return nil, &AgentSearcherExtractError{Code: "extract_malformed", HTTPStatus: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAgentSearcherExtractResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if requestCtx.Err() != nil {
			return nil, &AgentSearcherExtractError{Code: "extract_timeout", Cause: context.DeadlineExceeded}
		}
		return nil, &AgentSearcherExtractError{Code: "extract_unavailable", HTTPStatus: response.StatusCode}
	}
	if len(body) > maxAgentSearcherExtractResponse {
		return nil, &AgentSearcherExtractError{Code: "extract_response_too_big", HTTPStatus: response.StatusCode}
	}
	return body, nil
}

// agentSearcherExtractText returns raw_content of the result for rawURL, or
// the classified per-URL failure.
func agentSearcherExtractText(body []byte, rawURL string) (string, error) {
	var result agentSearcherExtractResponse
	if err := json.Unmarshal(body, &result); err != nil || (result.Results == nil && result.FailedResults == nil) {
		return "", &AgentSearcherExtractError{Code: "extract_malformed"}
	}
	for _, item := range result.Results {
		if item.URL != rawURL {
			continue
		}
		if item.RawContent == nil || !utf8.ValidString(*item.RawContent) {
			return "", &AgentSearcherExtractError{Code: "extract_empty"}
		}
		text := strings.ReplaceAll(*item.RawContent, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\x00", "")
		if strings.TrimSpace(text) == "" {
			return "", &AgentSearcherExtractError{Code: "extract_empty"}
		}
		if utf8.RuneCountInString(text) > agentSearcherMaxExtractRunes {
			return "", &AgentSearcherExtractError{Code: "extract_response_too_big"}
		}
		return text, nil
	}
	for _, item := range result.FailedResults {
		if item.URL == rawURL {
			return "", classifyAgentSearcherExtractFailure(item.Error)
		}
	}
	return "", &AgentSearcherExtractError{Code: "extract_malformed"}
}

func classifyAgentSearcherExtractFailure(reason string) error {
	reason = strings.Join(strings.Fields(reason), " ")
	if utf8.RuneCountInString(reason) > 120 {
		reason = string([]rune(reason)[:120])
	}
	lower := strings.ToLower(reason)
	code := "extract_failed"
	switch {
	case strings.HasPrefix(lower, "http "):
		code = "extract_source_unavailable"
	case strings.Contains(lower, "anti-bot") || strings.Contains(lower, "challenge"):
		code = "extract_blocked"
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout"):
		code = "extract_timeout"
	case strings.Contains(lower, "no content") || strings.Contains(lower, "no text layer"):
		code = "extract_empty"
	}
	return &AgentSearcherExtractError{Code: code, Reason: reason}
}
