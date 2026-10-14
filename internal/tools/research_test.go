package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type searchStub struct{ url string }

func (s searchStub) Search(_ context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	return []research.SearchHit{{Provider: "searxng", Query: options.Query, URL: s.url, Title: "A source", Snippet: "snippet", Taint: research.TaintExternal}}, nil
}

type fetchStub struct{ calls int }

func (*fetchStub) ValidateURL(_ context.Context, rawURL string) error {
	if strings.HasPrefix(rawURL, "https://") {
		return nil
	}
	return research.ErrUnsafeURL
}

func (f *fetchStub) Fetch(_ context.Context, rawURL string, _ research.FetchOptions) (research.FetchedDocument, error) {
	f.calls++
	return research.FetchedDocument{
		Provider: "direct_http", OriginalURL: rawURL, FinalURL: rawURL, ContentType: "text/html",
		HTTPStatus: 200, RetrievedAt: time.Now().UTC(), ContentSHA256: "test-content-hash", Body: []byte("<html>raw</html>"),
	}, nil
}

type extractorStub struct{}

func (extractorStub) Extract(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
	return research.ExtractedDocument{Title: "Fetched source", Text: "Verified excerpt from the public page.", Method: "test_html", Taint: research.TaintExternal}, nil
}

func TestResearchToolsRequireKnownURLAndPersistEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateTask(ctx, domain.Task{ID: "research-task", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fetchStub{}
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{
		SearchProvider: searchStub{url: "https://docs.example.com/guide"},
		Fetcher:        fetcher, Extractor: extractorStub{}, Artifacts: artifactStore,
		Repository: store, MaxFetchBytes: 1024, AllowedDomains: []string{"example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	toolContext := WithTaskID(ctx, "research-task")
	searchResult, err := registry.Execute(toolContext, "researcher", "search", json.RawMessage(`{"query":"local Qwen","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(searchResult.([]research.SearchHit)) != 1 {
		t.Fatalf("search result = %#v", searchResult)
	}
	if _, err := registry.Execute(WithFetchByteLimit(toolContext, 2), "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/guide","original_query":"local Qwen"}`)); !errors.Is(err, research.ErrResponseTooBig) {
		t.Fatalf("task fetch byte budget error = %v", err)
	}
	fetched, err := registry.Execute(toolContext, "researcher", "fetch_url", json.RawMessage(`{"url":"https://docs.example.com/guide","original_query":"local Qwen"}`))
	if err != nil {
		t.Fatal(err)
	}
	metadata := fetched.(map[string]any)
	if metadata["evidence_id"] == "" || metadata["taint"] != research.TaintExternal || fetcher.calls != 2 {
		t.Fatalf("unexpected fetch metadata %#v; fetch calls=%d", metadata, fetcher.calls)
	}
	evidence, err := store.Evidence(ctx, "research-task")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Title != "Fetched source" || evidence[0].RawArtifactID == "" || evidence[0].TextArtifactID == "" {
		t.Fatalf("evidence not persisted with artifacts: %#v", evidence)
	}
	if _, err := registry.Execute(toolContext, "researcher", "fetch_url", json.RawMessage(`{"url":"https://unknown.example/secret"}`)); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("unsearched URL error = %v, want ErrUnknownTarget", err)
	}
	if fetcher.calls != 2 {
		t.Fatalf("unsearched URL reached fetcher, calls=%d", fetcher.calls)
	}
}
