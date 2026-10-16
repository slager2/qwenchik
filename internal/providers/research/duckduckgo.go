package research

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	duckDuckGoEndpoint  = "https://html.duckduckgo.com/html/"
	duckDuckGoHost      = "html.duckduckgo.com"
	duckDuckGoMaxHits   = 8
	duckDuckGoMaxBody   = 2 << 20
	duckDuckGoMaxQuery  = 1000
	duckDuckGoTimeout   = 8 * time.Second
	duckDuckGoUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// DuckDuckGo is the built-in HTML search fallback. Its endpoint is fixed and
// all outbound connections use SafeFetcher URL validation and transport rules.
type DuckDuckGo struct {
	fetcher *SafeFetcher
}

func NewDuckDuckGo() *DuckDuckGo {
	fetcher := NewSafeFetcherWithPolicy(duckDuckGoMaxBody, duckDuckGoTimeout, nil, []string{duckDuckGoHost})
	provider, _ := NewDuckDuckGoWithFetcher(fetcher)
	return provider
}

// NewDuckDuckGoWithFetcher uses an injected SafeFetcher while preserving the
// fixed DuckDuckGo endpoint, bounded timeout, cookie jar, and no-redirect policy.
func NewDuckDuckGoWithFetcher(fetcher *SafeFetcher) (*DuckDuckGo, error) {
	if fetcher == nil || fetcher.client == nil {
		return nil, errors.New("DuckDuckGo requires a SafeFetcher with an HTTP client")
	}
	fetcherCopy := *fetcher
	clientCopy := *fetcher.client
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > duckDuckGoTimeout {
		clientCopy.Timeout = duckDuckGoTimeout
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, errors.New("DuckDuckGo cookie jar could not be initialized")
	}
	clientCopy.Jar = jar
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	fetcherCopy.client = &clientCopy
	return &DuckDuckGo{fetcher: &fetcherCopy}, nil
}

func (d *DuckDuckGo) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	query := strings.TrimSpace(options.Query)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > duckDuckGoMaxQuery {
		return nil, errors.New("DuckDuckGo query must contain 1 to 1000 characters")
	}
	if len(options.Language) > 20 {
		return nil, errors.New("search language exceeds 20 characters")
	}
	for _, char := range options.Language {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-') {
			return nil, errors.New("search language contains unsupported characters")
		}
	}
	recency, err := duckDuckGoRecency(options.Recency)
	if err != nil {
		return nil, err
	}
	maxResults := options.MaxResults
	if maxResults < 1 || maxResults > duckDuckGoMaxHits {
		maxResults = duckDuckGoMaxHits
	}
	if d == nil || d.fetcher == nil || d.fetcher.client == nil {
		return nil, fmt.Errorf("%w: DuckDuckGo is not initialized", ErrSearchUnavailable)
	}

	requestURL, _ := url.Parse(duckDuckGoEndpoint)
	values := requestURL.Query()
	values.Set("q", query)
	if options.SafeSearch {
		values.Set("kp", "1")
	} else {
		values.Set("kp", "-2")
	}
	if options.Language != "" {
		values.Set("kl", strings.ToLower(options.Language))
	}
	if recency != "" {
		values.Set("df", recency)
	}
	requestURL.RawQuery = values.Encode()

	requestCtx, cancel := context.WithTimeout(ctx, duckDuckGoTimeout)
	defer cancel()
	if err := d.fetcher.ValidateURL(requestCtx, requestURL.String()); err != nil {
		return nil, errors.Join(ErrSearchUnavailable, err)
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, errors.New("DuckDuckGo request could not be created")
	}
	request.Header.Set("User-Agent", duckDuckGoUserAgent)
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("Pragma", "no-cache")

	response, err := d.fetcher.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: DuckDuckGo request failed", ErrSearchUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: DuckDuckGo returned HTTP %d", ErrSearchUnavailable, response.StatusCode)
	}
	if response.ContentLength > duckDuckGoMaxBody {
		return nil, ErrSearchResponseTooBig
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, duckDuckGoMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: DuckDuckGo response could not be read", ErrSearchUnavailable)
	}
	if len(body) > duckDuckGoMaxBody {
		return nil, ErrSearchResponseTooBig
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return nil, fmt.Errorf("%w: DuckDuckGo returned a non-HTML response", ErrSearchUnavailable)
	}
	return parseDuckDuckGoResults(body, query, options.Domains, maxResults), nil
}

func duckDuckGoRecency(value string) (string, error) {
	switch value {
	case "":
		return "", nil
	case "day":
		return "d", nil
	case "week":
		return "w", nil
	case "month":
		return "m", nil
	case "year":
		return "y", nil
	default:
		return "", errors.New("recency must be day, week, month, or year")
	}
}

func parseDuckDuckGoResults(body []byte, query string, domains []string, maxResults int) []SearchHit {
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return []SearchHit{}
	}
	hits := make([]SearchHit, 0, maxResults)
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if len(hits) >= maxResults {
			return
		}
		if node.Type == html.ElementNode && node.Data == "div" && hasHTMLClass(node, "result") && !isDuckDuckGoAd(node) {
			if hit, ok := parseDuckDuckGoResult(node, query, domains); ok {
				hits = append(hits, hit)
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
			if len(hits) >= maxResults {
				return
			}
		}
	}
	visit(root)
	return hits
}

func parseDuckDuckGoResult(result *html.Node, query string, domains []string) (SearchHit, bool) {
	var resultLink, snippetNode *html.Node
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if resultLink != nil && snippetNode != nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" && hasHTMLClass(node, "result__a") && resultLink == nil {
			resultLink = node
		}
		if node.Type == html.ElementNode && hasHTMLClass(node, "result__snippet") && snippetNode == nil {
			snippetNode = node
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(result)
	if resultLink == nil {
		return SearchHit{}, false
	}
	rawURL := htmlAttribute(resultLink, "href")
	resultURL, err := normalizeDuckDuckGoResultURL(rawURL)
	if err != nil || !matchesDomainAllowlist(resultURL.Hostname(), domains) {
		return SearchHit{}, false
	}
	title := truncateTavilyText(duckDuckGoNodeText(resultLink), 500)
	if title == "" {
		return SearchHit{}, false
	}
	snippet := ""
	if snippetNode != nil {
		snippet = truncateTavilyText(duckDuckGoNodeText(snippetNode), 1000)
	}
	return SearchHit{Provider: "duckduckgo", Query: query, URL: resultURL.String(), Title: title, Snippet: snippet, Taint: TaintExternal}, true
}

func normalizeDuckDuckGoResultURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return nil, errors.New("invalid DuckDuckGo result URL")
	}
	if !parsed.IsAbs() {
		base, _ := url.Parse(duckDuckGoEndpoint)
		parsed = base.ResolveReference(parsed)
	}
	if isDuckDuckGoHost(parsed.Hostname()) && (parsed.Path == "/l" || parsed.Path == "/l/") {
		target := parsed.Query().Get("uddg")
		if target == "" {
			return nil, errors.New("DuckDuckGo result link omitted its target")
		}
		parsed, err = url.Parse(strings.TrimSpace(target))
		if err != nil || parsed == nil || !parsed.IsAbs() {
			return nil, errors.New("invalid DuckDuckGo target URL")
		}
	}
	if !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid DuckDuckGo result URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	return parsed, nil
}

func isDuckDuckGoHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com")
}

func isDuckDuckGoAd(node *html.Node) bool {
	for _, class := range htmlClasses(node) {
		class = strings.ToLower(class)
		if strings.Contains(class, "sponsored") || class == "ad" || strings.Contains(class, "result--ad") || strings.Contains(class, "result__ad") {
			return true
		}
	}
	return false
}

func hasHTMLClass(node *html.Node, wanted string) bool {
	for _, class := range htmlClasses(node) {
		if class == wanted {
			return true
		}
	}
	return false
}

func htmlClasses(node *html.Node) []string {
	for _, attribute := range node.Attr {
		if attribute.Key == "class" {
			return strings.Fields(attribute.Val)
		}
	}
	return nil
}

func htmlAttribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}

func duckDuckGoNodeText(node *html.Node) string {
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return strings.Join(strings.Fields(text.String()), " ")
}
