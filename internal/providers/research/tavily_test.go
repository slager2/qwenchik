package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type tavilyRewriteTransport struct {
	target string
	t      *testing.T
}

func (transport tavilyRewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if got := request.URL.String(); got != tavilyEndpoint {
		transport.t.Errorf("request endpoint = %q, want %q", got, tavilyEndpoint)
		return nil, errors.New("unexpected Tavily endpoint")
	}
	target, err := url.Parse(strings.TrimRight(transport.target, "/") + request.URL.EscapedPath())
	if err != nil {
		return nil, err
	}
	forwarded := request.Clone(request.Context())
	forwarded.URL = target
	forwarded.Host = target.Host
	return http.DefaultTransport.RoundTrip(forwarded)
}

func newTavilyTestProvider(t *testing.T, handler http.Handler) *Tavily {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: tavilyRewriteTransport{target: server.URL, t: t}}
	provider, err := NewTavily("test-api-key", client)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestTavilySearchUsesBearerAuthAndNormalizesResults(t *testing.T) {
	provider := newTavilyTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("Authorization header = %q, want Tavily bearer auth", got)
		}
		var body struct {
			Query                string   `json:"query"`
			SearchDepth          string   `json:"search_depth"`
			MaxResults           int      `json:"max_results"`
			IncludeDomains       []string `json:"include_domains"`
			IncludeDomainsMode   string   `json:"include_domains_mode"`
			TimeRange            string   `json:"time_range"`
			Language             string   `json:"language"`
			SafeSearch           bool     `json:"safe_search"`
			IncludePublishedDate bool     `json:"include_published_date"`
			IncludeAnswer        bool     `json:"include_answer"`
			IncludeRawContent    bool     `json:"include_raw_content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body.Query != "Qwen local" || body.SearchDepth != "basic" || body.MaxResults != 2 || body.TimeRange != "week" || body.Language != "en" || !body.SafeSearch || !body.IncludePublishedDate || body.IncludeAnswer || body.IncludeRawContent || body.IncludeDomainsMode != "restrict" || len(body.IncludeDomains) != 1 || body.IncludeDomains[0] != "example.com" {
			t.Errorf("unexpected Tavily request body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"url": "https://docs.example.com/guide", "title": "  Official guide  ", "content": strings.Repeat("summary ", 200), "score": 0.91, "published_date": "2025-02-03"},
			{"url": "https://notexample.com/guide", "title": "Wrong domain", "content": "excluded"},
			{"url": "https://www.example.com/next", "title": "Second", "content": "second result", "score": 0.4, "published_date": "2025-02-03T12:30:00Z"},
			{"url": "file:///etc/passwd", "title": "Unsafe scheme", "content": "excluded"},
		}})
	}))

	hits, err := provider.Search(context.Background(), SearchOptions{
		Query: " Qwen local ", Domains: []string{"example.com"}, PreferredDomains: []string{"official.example"}, Recency: "week", MaxResults: 2,
		Language: "en", SafeSearch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2: %#v", len(hits), hits)
	}
	if hits[0].Provider != "tavily" || hits[0].Query != "Qwen local" || hits[0].Title != "Official guide" || len(hits[0].Snippet) > 1000 || hits[0].Taint != TaintExternal {
		t.Fatalf("first hit was not normalized: %#v", hits[0])
	}
	if hits[0].PublishedAt.Format("2006-01-02") != "2025-02-03" || hits[0].PublishedAt.Location() != time.UTC {
		t.Errorf("published date = %v, want 2025-02-03 UTC", hits[0].PublishedAt)
	}
	if hits[1].Title != "Second" || hits[1].URL != "https://www.example.com/next" {
		t.Errorf("second hit = %#v", hits[1])
	}
}

func TestTavilyPrefersOfficialDomainsWithoutHardFiltering(t *testing.T) {
	provider := newTavilyTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IncludeDomains     []string `json:"include_domains"`
			IncludeDomainsMode string   `json:"include_domains_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if len(body.IncludeDomains) != 1 || body.IncludeDomains[0] != "official.example.org" || body.IncludeDomainsMode != "prefer" {
			t.Errorf("Tavily domain preference = %#v mode=%q", body.IncludeDomains, body.IncludeDomainsMode)
		}
		_, _ = io.WriteString(w, `{"results":[{"url":"https://other.example.net/protocol","title":"Additional result","content":"not hard-filtered"}]}`)
	}))

	hits, err := provider.Search(context.Background(), SearchOptions{
		Query: "protocol reference", PreferredDomains: []string{"official.example.org"}, MaxResults: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].URL != "https://other.example.net/protocol" {
		t.Fatalf("preferred domains unexpectedly hard-filtered results: %#v", hits)
	}
}

func TestTavilyValidatesQueryAndResultLimitBeforeRequest(t *testing.T) {
	called := false
	provider := newTavilyTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	for _, options := range []SearchOptions{
		{Query: "  ", MaxResults: 1},
		{Query: strings.Repeat("q", tavilyMaxQueryRunes+1), MaxResults: 1},
		{Query: "valid", MaxResults: 0},
		{Query: "valid", MaxResults: tavilyMaxResults + 1},
	} {
		if _, err := provider.Search(context.Background(), options); err == nil {
			t.Errorf("Search(%#v) succeeded, want validation error", options)
		}
	}
	if called {
		t.Fatal("invalid search options reached the HTTP server")
	}
}

func TestTavilyBoundsClientTimeout(t *testing.T) {
	defaultClient, err := NewTavily("key", nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaultClient.client.Timeout != tavilyMaxTimeout {
		t.Errorf("default timeout = %v, want %v", defaultClient.client.Timeout, tavilyMaxTimeout)
	}
	shortClient, err := NewTavily("key", &http.Client{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if shortClient.client.Timeout != 2*time.Second {
		t.Errorf("short supplied timeout = %v, want 2s", shortClient.client.Timeout)
	}
	longClient, err := NewTavily("key", &http.Client{Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if longClient.client.Timeout != tavilyMaxTimeout {
		t.Errorf("long supplied timeout = %v, want cap %v", longClient.client.Timeout, tavilyMaxTimeout)
	}
}

func TestTavilyDoesNotExposeAPIKeyInErrors(t *testing.T) {
	const apiKey = "test-secret-api-key"
	t.Run("non-success response", func(t *testing.T) {
		provider := newTavilyTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, apiKey)
		}))
		_, err := provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 1})
		if !errors.Is(err, ErrSearchUnavailable) {
			t.Fatalf("error = %v, want ErrSearchUnavailable", err)
		}
		if err == nil || strings.Contains(err.Error(), apiKey) {
			t.Fatalf("error exposed API key: %v", err)
		}
	})
	t.Run("transport failure", func(t *testing.T) {
		provider, err := NewTavily(apiKey, &http.Client{Transport: tavilyErrorTransport{message: apiKey}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 1})
		if !errors.Is(err, ErrSearchUnavailable) || strings.Contains(err.Error(), apiKey) {
			t.Fatalf("unexpected error (or API key exposure): %v", err)
		}
	})
}

type tavilyErrorTransport struct{ message string }

func (transport tavilyErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(transport.message)
}

func TestTavilyHandlesMalformedAndOversizedResponses(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		provider := newTavilyTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"results":"not-an-array"}`)
		}))
		_, err := provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 1})
		if !errors.Is(err, ErrSearchUnavailable) || strings.Contains(fmt.Sprint(err), "not-an-array") {
			t.Fatalf("unexpected malformed-response error: %v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		provider := newTavilyTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxTavilyResponse+1))
		}))
		_, err := provider.Search(context.Background(), SearchOptions{Query: "query", MaxResults: 1})
		if !errors.Is(err, ErrTavilyResponseTooBig) || !errors.Is(err, ErrSearchResponseTooBig) {
			t.Fatalf("error = %v, want Tavily and common response size errors", err)
		}
	})
}

func TestNewTavilyRequiresKey(t *testing.T) {
	if _, err := NewTavily(" \t ", nil); err == nil {
		t.Fatal("empty API key should be rejected")
	}
}
