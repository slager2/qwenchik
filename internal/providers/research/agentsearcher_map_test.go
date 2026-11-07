package research

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAgentSearcherMapSendsBoundedWalkAndKeepsSiteURLs(t *testing.T) {
	var got agentSearcherMapRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/map" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base_url":"https://stipendiumhungaricum.hu","results":["https://stipendiumhungaricum.hu/","https://stipendiumhungaricum.hu/news/application-is-on-for-the-2026-2027-stipendium-hungaricum-scholarship/","https://stipendiumhungaricum.hu/","https://evil.example/x","ftp://stipendiumhungaricum.hu/a","https://apply.stipendiumhungaricum.hu/call.pdf"],"failed_results":[]}`))
	}))
	defer server.Close()
	mapper, err := NewAgentSearcherMapper(server.URL, 0, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	urls, err := mapper.Map(context.Background(), "https://stipendiumhungaricum.hu", "  2026 call for applications   deadline "+strings.Repeat("x", 400))
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxDepth != 2 || got.Limit != 30 || got.AllowExternal || got.Timeout != 60 || !strings.HasPrefix(got.Instructions, "2026 call for applications deadline x") || len([]rune(got.Instructions)) != 300 {
		t.Fatalf("request = %#v", got)
	}
	if strings.Join(urls, " ") != "https://stipendiumhungaricum.hu/ https://stipendiumhungaricum.hu/news/application-is-on-for-the-2026-2027-stipendium-hungaricum-scholarship/ https://apply.stipendiumhungaricum.hu/call.pdf" {
		t.Fatalf("urls = %q", urls)
	}
	if mapper.Budget() != 70*time.Second {
		t.Fatal(mapper.Budget())
	}
}

func TestAgentSearcherMapClassifiesFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	mapper, err := NewAgentSearcherMapper(server.URL, 5*time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Map(context.Background(), "https://example.org", ""); err == nil {
		t.Fatal("5xx accepted")
	}
	if _, err := mapper.Map(context.Background(), "file:///etc/passwd", ""); err == nil {
		t.Fatal("non-http URL accepted")
	}
	if _, err := NewAgentSearcherMapper("http://192.0.2.1:8484", 0, nil); err == nil {
		t.Fatal("non-loopback service accepted")
	}
}
