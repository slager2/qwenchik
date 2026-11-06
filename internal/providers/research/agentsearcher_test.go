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

func newAgentSearcherTestProvider(t *testing.T, handler http.HandlerFunc) *AgentSearcher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := NewAgentSearcher(server.URL, "", 0, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestAgentSearcherSendsTavilyRequestAndKeepsRankedPassages(t *testing.T) {
	passage := strings.Repeat("дорожная карта биоинформатики ", 200) // > 4000 bytes
	provider := newAgentSearcherTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("agentSearcher request must not carry credentials")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["query"] != "дорожная карта биоинформатика" || body["search_depth"] != "advanced" || body["max_results"] != float64(6) || body["chunks_per_source"] != float64(3) {
			t.Errorf("request body = %v", body)
		}
		if body["include_raw_content"] != false || body["include_answer"] != false || body["time_range"] != "year" {
			t.Errorf("request body = %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":"q","answer":null,"results":[
			{"url":"https://example.org/roadmap","title":" Roadmap ","content":` + mustJSON(t, passage) + `,"score":0.93,"published_date":"2025-03-01"},
			{"url":"javascript:alert(1)","title":"bad","content":"x","score":0.5},
			{"url":"https://second.example.com/a","title":"Second","content":"short passage","score":7}
		],"response_time":6.2}`))
	})
	hits, err := provider.Search(context.Background(), SearchOptions{Query: " дорожная карта биоинформатика ", MaxResults: 6, Recency: "year", SafeSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	first := hits[0]
	if first.Provider != "agentsearcher" || first.Taint != TaintExternal || first.URL != "https://example.org/roadmap" || first.Title != "Roadmap" || first.Score != 0.93 {
		t.Fatalf("first hit = %+v", first)
	}
	if len(first.Snippet) < 3000 || len(first.Snippet) > agentSearcherMaxContent || !strings.HasPrefix(first.Snippet, "дорожная карта") {
		t.Fatalf("passage was not preserved within bounds: %d bytes", len(first.Snippet))
	}
	if first.PublishedAt.IsZero() || hits[1].Score != 0 {
		t.Fatalf("date/score normalization failed: %+v", hits)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestAgentSearcherRequiresLoopbackAndValidDepth(t *testing.T) {
	for _, raw := range []string{"http://192.168.1.5:8484", "http://example.com", "ftp://127.0.0.1:8484", "http://user:pw@127.0.0.1:8484", "http://127.0.0.1:8484?x=1", ""} {
		if _, err := NewAgentSearcher(raw, "", 0, nil); err == nil {
			t.Errorf("NewAgentSearcher(%q) accepted a non-loopback or unsafe URL", raw)
		}
	}
	if _, err := NewAgentSearcher("http://127.0.0.1:8484", "deep", 0, nil); err == nil {
		t.Error("invalid depth accepted")
	}
	if _, err := NewAgentSearcher("http://127.0.0.1:8484", "basic", 3*time.Minute, nil); err == nil {
		t.Error("oversized timeout accepted")
	}
	provider, err := NewAgentSearcher("http://localhost:8484/", "basic", 20*time.Second, nil)
	if err != nil || provider.depth != "basic" || provider.SearchBudget() != 20*time.Second || provider.baseURL != "http://localhost:8484" {
		t.Fatalf("provider=%+v err=%v", provider, err)
	}
	if transport, ok := provider.client.Transport.(*http.Transport); !ok || transport.Proxy != nil {
		t.Fatal("agentSearcher client must not use an environment proxy")
	}
}

func TestAgentSearcherClassifiesFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		code    string
	}{
		{"busy", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"detail":{"error":"server busy"}}`, http.StatusServiceUnavailable)
		}, "search_unavailable"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		}, "provider_malformed"},
		{"no results field", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"query":"q"}`))
		}, "provider_malformed"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
		}, "search_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newAgentSearcherTestProvider(t, tc.handler)
			report, err := NewCompositeSearch(provider).SearchWithDiagnostics(context.Background(), SearchOptions{Query: "q", MaxResults: 3})
			if err == nil || len(report.Outcomes) != 1 || report.Outcomes[0].Code != tc.code || report.Outcomes[0].Provider != "agentsearcher" {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestAgentSearcherEmptyResultsAreNotAnOutage(t *testing.T) {
	provider := newAgentSearcherTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	hits, err := provider.Search(context.Background(), SearchOptions{Query: "q", MaxResults: 3})
	if err != nil || len(hits) != 0 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
}

func TestOrderedPrimaryFallsBackWhenAgentSearcherFails(t *testing.T) {
	agent := newAgentSearcherTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	fallbackCalled := false
	fallback := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		fallbackCalled = true
		return strategyHits("https://one.com/a", "https://two.org/b"), nil
	})
	report, err := NewOrderedPrimarySearch(agent, fallback).SearchWithDiagnostics(context.Background(), SearchOptions{Query: "q", MaxResults: 5})
	if err != nil || !fallbackCalled || len(report.Hits) != 2 || len(report.Outcomes) != 2 || report.Outcomes[0].Provider != "agentsearcher" || report.Outcomes[0].Code != "search_unavailable" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestOrderedPrimaryFallsBackWhenAgentSearcherIsEmpty(t *testing.T) {
	agent := newAgentSearcherTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	fallback := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		return strategyHits("https://one.com/a", "https://two.org/b"), nil
	})
	report, err := NewOrderedPrimarySearch(agent, fallback).SearchWithDiagnostics(context.Background(), SearchOptions{Query: "q", MaxResults: 5})
	if err != nil || len(report.Hits) != 2 || report.Outcomes[0].Code != "search_no_results" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestOrderedPrimaryPrefersAgentSearcherHits(t *testing.T) {
	agent := newAgentSearcherTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"url":"https://a.example.org/x","title":"A","content":"passage a","score":0.9},{"url":"https://b.example.net/y","title":"B","content":"passage b","score":0.8}]}`))
	})
	fallback := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) {
		t.Error("fallback must not run when agentSearcher covers two sites")
		return nil, errors.New("unexpected")
	})
	report, err := NewOrderedPrimarySearch(agent, fallback).SearchWithDiagnostics(context.Background(), SearchOptions{Query: "q", MaxResults: 5})
	if err != nil || len(report.Hits) != 2 || report.Hits[0].Provider != "agentsearcher" || report.Hits[0].Snippet != "passage a" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestSearchBudgetsAccountForSlowPrimary(t *testing.T) {
	agent, err := NewAgentSearcher("http://127.0.0.1:8484", "advanced", 45*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	fast := strategyProvider(func(context.Context, SearchOptions) ([]SearchHit, error) { return nil, nil })
	if got := NewOrderedPrimarySearch(fast, fast).SearchBudget(); got != providerSearchLimit {
		t.Fatalf("default budget changed: %v", got)
	}
	if got := NewOrderedPrimarySearch(agent, fast).SearchBudget(); got != 45*time.Second+fallbackSearchReserve {
		t.Fatalf("ordered budget = %v", got)
	}
	if got := NewOrderedPrimarySearch(agent).SearchBudget(); got != 45*time.Second {
		t.Fatalf("agent-only budget = %v", got)
	}
	if got := NewCompositeSearch(fast, agent).SearchBudget(); got != 45*time.Second {
		t.Fatalf("composite budget = %v", got)
	}
	if got := SearchTimeBudget(fast); got != providerSearchLimit {
		t.Fatalf("plain provider budget = %v", got)
	}
}
