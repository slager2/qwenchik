package research

import (
	"context"
	"encoding/json"
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
