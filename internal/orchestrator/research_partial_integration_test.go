package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type partialIntegrationProvider struct{ usable int }

func (p partialIntegrationProvider) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	report, err := p.SearchWithDiagnostics(ctx, options)
	return report.Hits, err
}

func (p partialIntegrationProvider) SearchWithDiagnostics(_ context.Context, options research.SearchOptions) (research.SearchReport, error) {
	hits := []research.SearchHit{{Provider: "searxng", Query: "internal api_key=PRIVATE", URL: "https://docs.example.com/protocol", Title: "Specification", Snippet: "Protocol architecture"}}
	if p.usable == 2 {
		hits = append(hits, research.SearchHit{Provider: "searxng", URL: "https://reference.example.net/protocol", Title: "Reference", Snippet: "Protocol compatibility"})
	}
	hits = append(hits, research.SearchHit{Provider: "searxng", URL: "https://slow.example.org/protocol", Title: "Slow source"})
	return research.SearchReport{Hits: hits, Outcomes: []research.ProviderOutcome{{Provider: "searxng", State: "healthy", Code: "search_ok", ResultCount: len(hits)}}}, nil
}

type partialIntegrationResolver struct {
	mu    sync.Mutex
	calls map[string]int
}

func (r *partialIntegrationResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	r.calls[host]++
	r.mu.Unlock()
	if host == "slow.example.org" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
}

type partialIntegrationFetcher struct {
	safe    *research.SafeFetcher
	fetched []string
}

func (f *partialIntegrationFetcher) ValidateURL(ctx context.Context, rawURL string) error {
	return f.safe.ValidateURL(ctx, rawURL)
}
func (f *partialIntegrationFetcher) Fetch(ctx context.Context, rawURL string, _ research.FetchOptions) (research.FetchedDocument, error) {
	if err := f.safe.ValidateURL(ctx, rawURL); err != nil {
		return research.FetchedDocument{}, err
	}
	f.fetched = append(f.fetched, rawURL)
	body := []byte("<main>Fresh protocol evidence for " + rawURL + "</main>")
	hash := sha256.Sum256(body)
	return research.FetchedDocument{Provider: "fixture", OriginalURL: rawURL, FinalURL: rawURL, HTTPStatus: 200, ContentType: "text/html", RetrievedAt: time.Now().UTC(), ContentSHA256: hex.EncodeToString(hash[:]), Body: body}, nil
}

func TestResearchPartialValidationContinuesToFreshFetch(t *testing.T) {
	for _, usable := range []int{2, 1} {
		name := "two_sources_complete"
		if usable == 1 {
			name = "one_source_insufficient"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, err := sqlite.Open(ctx, t.TempDir()+"/partial.db")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			artifactStore, err := artifacts.New(t.TempDir(), 1_000_000)
			if err != nil {
				t.Fatal(err)
			}
			resolver := &partialIntegrationResolver{calls: map[string]int{}}
			fetcher := &partialIntegrationFetcher{safe: research.NewSafeFetcherWithResolver(100_000, time.Second, resolver)}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: partialIntegrationProvider{usable: usable}, Fetcher: fetcher, Extractor: researchExtractorStub{}, Artifacts: artifactStore, Repository: store, MaxFetchBytes: 100_000}); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(ctx, domain.Task{ID: "partial", Title: "Protocol review", Description: "Research protocol architecture and limitations."}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := store.ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim=%t err=%v", ok, err)
			}
			worker := New(store, integrationResearchModel(), registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxSearchQueries: 4})
			worker.runTask(ctx, claimed)
			current, err := store.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := store.EventsAfter(ctx, task.ID, 0, 300)
			if err != nil {
				t.Fatal(err)
			}
			completedSearches, rejected, completedTask := 0, 0, false
			foundDiscovery := map[string]bool{}
			for _, event := range events {
				if strings.Contains(string(event.Payload), "PRIVATE") {
					t.Fatalf("provider internals persisted in %s", event.Type)
				}
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				switch event.Type {
				case "search.hit":
					hit := payload["hit"].(map[string]any)
					id := hit["id"].(string)
					if hit["status"] == "discovered" {
						foundDiscovery[id] = true
					} else if !foundDiscovery[id] {
						t.Fatalf("terminal source event preceded discovery: %s", event.Payload)
					}
					if hit["status"] == "rejected" && hit["error_code"] == "search_validation_timeout" {
						rejected++
					}
				case "tool.completed":
					if payload["tool"] == "search" {
						completedSearches++
						if len(payload["result"].([]any)) != usable {
							t.Fatalf("partial hits lost: %s", event.Payload)
						}
					}
				case "task.completed":
					completedTask = true
				case "task.failed":
					if payload["error_code"] == "worker_error" || usable == 2 {
						t.Fatalf("unexpected task failure: %s", event.Payload)
					}
					if payload["error_code"] != "verification_incomplete" {
						t.Fatalf("expected honest insufficient evidence failure: %s", event.Payload)
					}
				}
			}
			if completedSearches != 2 || rejected != 2 || len(fetcher.fetched) != usable {
				t.Fatalf("searches=%d rejected=%d fetched=%v status=%s", completedSearches, rejected, fetcher.fetched, current.Status)
			}
			if usable == 2 && (current.Status != domain.StatusCompleted || !completedTask) || usable == 1 && (current.Status != domain.StatusFailed || completedTask) {
				t.Fatalf("false completion status=%s completed=%t", current.Status, completedTask)
			}
			resolver.mu.Lock()
			defer resolver.mu.Unlock()
			for _, rawURL := range fetcher.fetched {
				if strings.Contains(rawURL, "slow.example.org") {
					t.Fatal("rejected source was fetched")
				}
				host := "docs.example.com"
				if strings.Contains(rawURL, "reference.example.net") {
					host = "reference.example.net"
				}
				if resolver.calls[host] <= 2 {
					t.Fatalf("fetch reused search-only validation for %s: calls=%d", host, resolver.calls[host])
				}
			}
		})
	}
}
