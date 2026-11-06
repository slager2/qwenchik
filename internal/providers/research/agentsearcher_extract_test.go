package research

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newExtractTestClient(t *testing.T, handler http.HandlerFunc) *AgentSearcherExtractor {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewAgentSearcherExtractor(server.URL, 0, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeExtractJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestAgentSearcherExtractorRequestsPageTextThenRankedChunks(t *testing.T) {
	const pageURL = "https://example.org/report"
	pageText := "Intro line.\n\nThe Transformer uses eight attention heads.\n\nOther text."
	var bodies []map[string]any
	client := newExtractTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/extract" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		raw := pageText
		if _, ranked := body["query"]; ranked {
			raw = "The Transformer uses eight attention heads. [...] Intro line."
		}
		writeExtractJSON(w, map[string]any{"results": []map[string]any{{"url": pageURL, "raw_content": raw}}, "failed_results": []any{}, "response_time": 1.2, "request_id": "r"})
	})
	page, err := client.Extract(context.Background(), pageURL, " attention heads ")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want page text and ranked chunks", len(bodies))
	}
	first, second := bodies[0], bodies[1]
	if _, ok := first["query"]; ok || first["extract_depth"] != "advanced" || first["format"] != "text" || first["timeout"] != float64(30) {
		t.Fatalf("page request = %v", first)
	}
	if second["query"] != "attention heads" || second["chunks_per_source"] != float64(5) {
		t.Fatalf("chunk request = %v", second)
	}
	urls, _ := first["urls"].([]any)
	if len(urls) != 1 || urls[0] != pageURL {
		t.Fatalf("urls = %v", first["urls"])
	}
	if page.Text != pageText || page.PDF || len(page.Chunks) != 2 || page.Chunks[0] != "The Transformer uses eight attention heads." || len(page.TextResponse) == 0 || len(page.ChunksResponse) == 0 {
		t.Fatalf("page = %+v", page)
	}
}

func TestAgentSearcherExtractorDetectsPDFAndSkipsChunksWithoutQuery(t *testing.T) {
	calls := 0
	client := newExtractTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeExtractJSON(w, map[string]any{"results": []map[string]any{{"url": "https://arxiv.org/pdf/1706.03762", "raw_content": "Page 1\n\nAttention Is All You Need\n\nPage 2\n\nMore"}}, "failed_results": []any{}})
	})
	page, err := client.Extract(context.Background(), "https://arxiv.org/pdf/1706.03762", "")
	if err != nil || !page.PDF || calls != 1 || page.Chunks != nil {
		t.Fatalf("page=%+v calls=%d err=%v", page, calls, err)
	}
}

func TestAgentSearcherExtractorKeepsTextWhenChunkRankingFails(t *testing.T) {
	calls := 0
	client := newExtractTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeExtractJSON(w, map[string]any{"results": []map[string]any{{"url": "https://example.org/a", "raw_content": "Body text."}}, "failed_results": []any{}})
	})
	page, err := client.Extract(context.Background(), "https://example.org/a", "body")
	if err != nil || page.Text != "Body text." || page.Chunks != nil || page.ChunksResponse != nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestAgentSearcherExtractorClassifiesFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		code    string
	}{
		{"site 403", func(w http.ResponseWriter, _ *http.Request) {
			writeExtractJSON(w, map[string]any{"results": []any{}, "failed_results": []map[string]any{{"url": "https://example.org/a", "error": "HTTP 403"}}})
		}, "extract_source_unavailable"},
		{"challenge", func(w http.ResponseWriter, _ *http.Request) {
			writeExtractJSON(w, map[string]any{"results": []any{}, "failed_results": []map[string]any{{"url": "https://example.org/a", "error": "blocked by anti-bot challenge"}}})
		}, "extract_blocked"},
		{"page timeout", func(w http.ResponseWriter, _ *http.Request) {
			writeExtractJSON(w, map[string]any{"results": []any{}, "failed_results": []map[string]any{{"url": "https://example.org/a", "error": "timed out"}}})
		}, "extract_timeout"},
		{"no text layer", func(w http.ResponseWriter, _ *http.Request) {
			writeExtractJSON(w, map[string]any{"results": []any{}, "failed_results": []map[string]any{{"url": "https://example.org/a", "error": "PDF has no text layer"}}})
		}, "extract_empty"},
		{"blank text", func(w http.ResponseWriter, _ *http.Request) {
			writeExtractJSON(w, map[string]any{"results": []map[string]any{{"url": "https://example.org/a", "raw_content": "  \n"}}, "failed_results": []any{}})
		}, "extract_empty"},
		{"other URL", func(w http.ResponseWriter, _ *http.Request) {
			writeExtractJSON(w, map[string]any{"results": []map[string]any{{"url": "https://other.example/", "raw_content": "x"}}, "failed_results": []any{}})
		}, "extract_malformed"},
		{"busy", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, "extract_unavailable"},
		{"bad request", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }, "extract_rejected"},
		{"html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		}, "extract_malformed"},
		{"too big", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"url":"https://example.org/a","raw_content":"` + strings.Repeat("a", maxAgentSearcherExtractResponse) + `"}]}`))
		}, "extract_response_too_big"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newExtractTestClient(t, tc.handler)
			_, err := client.Extract(context.Background(), "https://example.org/a", "")
			var failure *AgentSearcherExtractError
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestAgentSearcherExtractorTimesOutAndHonoursCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	client, err := NewAgentSearcherExtractor(server.URL, time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Extract(ctx, "https://example.org/a", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled extract err = %v", err)
	}
}

func TestNewAgentSearcherExtractorRequiresLoopback(t *testing.T) {
	for _, raw := range []string{"http://example.org:8484", "ftp://127.0.0.1:8484", "http://user:pw@127.0.0.1:8484", "http://127.0.0.1:8484/?q=1", ""} {
		if _, err := NewAgentSearcherExtractor(raw, 0, nil); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
	client, err := NewAgentSearcherExtractor("http://127.0.0.1:8484/", 5*time.Minute, nil)
	if err != nil || client.pageTimeout != agentSearcherMaxPageTimeout || client.baseURL != "http://127.0.0.1:8484" {
		t.Fatalf("client=%+v err=%v", client, err)
	}
	if _, err := client.Extract(context.Background(), "file:///etc/passwd", ""); err == nil {
		t.Fatal("non-HTTP target accepted")
	}
}
