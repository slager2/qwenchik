package research

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearXNGSearchNormalizesAndFiltersHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("safesearch") != "1" || r.URL.Query().Get("q") != "Qwen local" {
			t.Errorf("unexpected SearXNG request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"url": "https://docs.example.com/guide", "title": "Official guide", "content": strings.Repeat("summary ", 200), "score": 0.91},
			{"url": "https://notexample.com/guide", "title": "Wrong domain", "content": "ignore allowlist"},
			{"url": "file:///etc/passwd", "title": "Unsafe scheme", "content": "ignore"},
		}})
	}))
	defer server.Close()
	provider, err := NewSearXNG(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	hits, err := provider.Search(context.Background(), SearchOptions{Query: "Qwen local", Language: "en", Domains: []string{"example.com"}, MaxResults: 5, SafeSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "Official guide" || len(hits[0].Snippet) > 1000 || hits[0].Provider != "searxng" {
		t.Fatalf("unexpected search hits: %#v", hits)
	}
}

func TestSearXNGRequiresLoopbackConfiguration(t *testing.T) {
	if _, err := NewSearXNG("https://search.example.com", nil); err == nil {
		t.Fatal("remote SearXNG URL should be rejected")
	}
}

func TestSearXNGClassifiesProviderHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "search is temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	provider, err := NewSearXNG(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), SearchOptions{Query: "technical search", MaxResults: 5})
	if !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("search error = %v, want ErrSearchUnavailable", err)
	}
}

func TestSearXNGDistinguishesEmptyResultsFromMalformedResponse(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "missing results field", body: `{}`, wantErr: true},
		{name: "null results field", body: `{"results":null}`, wantErr: true},
		{name: "valid empty results", body: `{"results":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			provider, err := NewSearXNG(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			hits, err := provider.Search(context.Background(), SearchOptions{Query: "technical search", MaxResults: 3})
			if test.wantErr {
				if !errors.Is(err, ErrSearchUnavailable) {
					t.Fatalf("Search() error=%v, want ErrSearchUnavailable", err)
				}
				return
			}
			if err != nil || len(hits) != 0 {
				t.Fatalf("Search() hits=%#v err=%v, want a successful empty result", hits, err)
			}
		})
	}
}

func TestSearXNGClassifiesOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", maxSearXNGResponse+1))
	}))
	defer server.Close()
	provider, err := NewSearXNG(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), SearchOptions{Query: "technical search", MaxResults: 5})
	if !errors.Is(err, ErrSearchResponseTooBig) {
		t.Fatalf("search error = %v, want ErrSearchResponseTooBig", err)
	}
}
