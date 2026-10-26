package cli

import (
	"context"
	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivacyProviderGatesIgnoreEnabledExternalFallbacks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "local unavailable", 503) }))
	defer server.Close()
	cfg := config.Config{ResearchPrivacyMode: domain.PrivacyLocalSearXNG, SearXNGURL: server.URL, TavilyAPIKey: "ignored", SearchEnableDDG: true, SearchProviderPolicy: "parallel"}
	provider, err := configuredSearchProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*research.SearXNG); !ok {
		t.Fatalf("local mode installed fallback providers: %T", provider)
	}
	if _, err := provider.Search(context.Background(), research.SearchOptions{Query: "protocol", MaxResults: 2}); err == nil {
		t.Fatal("expected gateway failure")
	}
	p := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(p, []byte(`{"documents":[{"url":"https://example.org/source","text":"Protocol architecture and limitations."}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	corpus, err := research.LoadOfflineCorpus(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ResearchPrivacyMode = domain.PrivacyOffline
	cfg.OfflineCorpusPath = p
	cfg.OfflineCorpusSHA256 = corpus.SHA256()
	provider, err = configuredSearchProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*research.OfflineCorpus); !ok {
		t.Fatalf("offline installed network provider %T", provider)
	}
	if err := os.WriteFile(p, []byte(`{"documents":[{"url":"https://example.org/source","text":"Changed protocol corpus."}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredSearchProvider(cfg); err == nil {
		t.Fatal("changed pinned corpus admitted")
	}
}
