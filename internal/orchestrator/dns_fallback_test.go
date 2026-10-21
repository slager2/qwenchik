package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
)

type fallbackDNSResolver func(context.Context, string) ([]net.IPAddr, error)

func (f fallbackDNSResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type authorizationDNSFetcher struct {
	safe *research.SafeFetcher
	base extractionFallbackFetcher
}

func (f *authorizationDNSFetcher) ValidateURL(ctx context.Context, raw string) error {
	return f.safe.ValidateURL(ctx, raw)
}

func (f *authorizationDNSFetcher) Fetch(ctx context.Context, raw string, options research.FetchOptions) (research.FetchedDocument, error) {
	return f.base.Fetch(ctx, raw, options)
}

func TestFetchAuthorizationDNSFailureUsesNextSource(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "discovery_alternative", true: "required_url"}[required], func(t *testing.T) {
			ctx := context.Background()
			store, worker, task, state, _ := newInterruptedFetch(t, ctx)
			defer store.Close()
			const bad = "https://docs.example.com/page"
			const good = "https://reference.example.net/page"
			dnsCause := &net.DNSError{Name: "docs.example.com", Err: "temporary DNS failure", IsTemporary: true}
			resolver := fallbackDNSResolver(func(_ context.Context, host string) ([]net.IPAddr, error) {
				if host == "docs.example.com" {
					return nil, dnsCause
				}
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			})
			fetchPolicy := research.DefaultFetchPolicy()
			fetchPolicy.TempRoot = t.TempDir()
			safe, err := research.NewSafeFetcherWithFetchPolicy(fetchPolicy, resolver, nil)
			if err != nil {
				t.Fatal(err)
			}
			fetcher := &authorizationDNSFetcher{safe: safe, base: extractionFallbackFetcher{bodies: map[string]string{good: "<main>Verified reference about protocol compatibility and framing.</main>"}, calls: map[string]int{}}}
			artifactStore, err := artifacts.New(t.TempDir(), 4096)
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{Fetcher: fetcher, Extractor: research.NewHTMLExtractorWithPDF(10000, nil), Artifacts: artifactStore, Repository: store, MaxFetchBytes: 4096}); err != nil {
				t.Fatal(err)
			}
			registry.SeedTaskURLs(task.ID, bad+" "+good)
			worker.tools, worker.options.MaxFetchBytes = registry, 4096
			ctx = tools.WithTaskID(ctx, task.ID)
			var decision actionDecision
			if err := json.Unmarshal([]byte(`{"tool_call":{"name":"fetch_url","arguments":{"url":"https://docs.example.com/page"}}}`), &decision); err != nil {
				t.Fatal(err)
			}
			stage := domain.StageFetch
			result, _, exhausted, err := worker.executeFetchCandidates(ctx, task.ID, &stage, domain.PlanStep{ID: "fetch_1", Kind: "fetch"}, "researcher", "read", []string{bad, good}, &decision, &state, map[string]struct{}{}, "", required)
			if required {
				if err == nil || result != nil || fetcher.base.calls[good] != 0 || !errors.Is(err, dnsCause) {
					t.Fatalf("required URL fallback: result=%v calls=%v err=%v", result, fetcher.base.calls, err)
				}
			} else if err != nil || exhausted || result == nil || fetcher.base.calls[good] != 1 {
				t.Fatalf("DNS failure stopped approved alternatives: result=%v exhausted=%v calls=%v err=%v", result, exhausted, fetcher.base.calls, err)
			}
			if len(state.SourceFailures) != 1 || state.SourceFailures[0].Code != "fetch_dns_failed" || state.SourceFailures[0].FetchedBytes != 0 || state.SourceFailures[0].TryNextSource == required {
				t.Fatalf("incorrect DNS source failure: %+v", state.SourceFailures)
			}
			if !contains(state.FailedFetchURLs, bad) {
				t.Fatal("failed DNS URL was not checkpointed")
			}
			evidence, err := store.Evidence(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(evidence) != fetcher.base.calls[good] {
				t.Fatalf("invalid evidence count %d", len(evidence))
			}
			for _, item := range evidence {
				if item.CanonicalURL != good {
					t.Fatal("DNS-failed source became evidence")
				}
			}
		})
	}
}

func TestWorkerCompletesAfterDiscoveredSourceDNSFailure(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	var mu sync.Mutex
	validations := 0
	resolver := fallbackDNSResolver(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "docs.example.com" {
			mu.Lock()
			validations++
			n := validations
			mu.Unlock()
			if n > 2 {
				return nil, &net.DNSError{Name: host, Err: "lookup failed after discovery", IsTemporary: true}
			}
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	policy := research.DefaultFetchPolicy()
	policy.TempRoot = t.TempDir()
	safe, err := research.NewSafeFetcherWithFetchPolicy(policy, resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &authorizationDNSFetcher{safe: safe, base: extractionFallbackFetcher{bodies: map[string]string{
		"https://reference.example.net/protocol": "<main>The protocol uses framed messages and independent implementations.</main>",
		"https://benchmark.example.org/protocol": "<main>Compatible framing and protocol versions are required for interoperability.</main>",
	}, calls: map[string]int{}}}
	artifactStore, err := artifacts.New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: extractionFallbackProvider{}, Fetcher: fetcher, Extractor: research.NewHTMLExtractorWithPDF(10000, nil), Artifacts: artifactStore, Repository: f.store, MaxFetchBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	task, err := f.store.CreateTask(ctx, domain.Task{ID: "discovered-dns-fallback", Title: "Protocol review", Description: "Research protocol architecture and limitations."}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := f.store.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	model := integrationResearchModel()
	respond := model.respond
	model.respond = func(request llm.ChatRequest, index int) (string, error) {
		if index == 4 {
			return `{"status":"continue","summary":"Read independent compatibility reference","tool_call":{"name":"fetch_url","arguments":{"url":"https://benchmark.example.org/protocol","original_query":"protocol compatibility"}}}`, nil
		}
		return respond(request, index)
	}
	worker := New(f.store, model, registry, Options{TaskTimeout: time.Minute, MaxSteps: 8, MaxFetchBytes: 4096})
	worker.runTask(ctx, claimed)
	current, err := f.store.GetTask(ctx, task.ID)
	if err != nil || current.Status != domain.StatusCompleted {
		t.Fatalf("status=%v err=%v", current.Status, err)
	}
	state, err := worker.currentCheckpoint(ctx, task.ID)
	if err != nil || len(state.SourceFailures) != 1 || state.SourceFailures[0].Code != "fetch_dns_failed" || !state.SourceFailures[0].TryNextSource || !state.SourceFailures[0].FallbackAttempted || !state.Partial {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	evidence, err := f.store.Evidence(ctx, task.ID)
	if err != nil || len(evidence) != 2 {
		t.Fatalf("evidence=%v err=%v", evidence, err)
	}
	events, err := f.store.EventsAfter(ctx, task.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	publicDNS := 0
	for _, event := range events {
		if event.Type == "task.failed" {
			t.Fatalf("unexpected terminal failure: %s", event.Payload)
		}
		if event.Type == "tool.failed" && event.Visibility == "user" {
			var failure sourceFailure
			if json.Unmarshal(event.Payload, &failure) != nil || failure.Code != "fetch_dns_failed" || failure.FetchedBytes != 0 {
				t.Fatalf("incorrect DNS failure event: %s", event.Payload)
			}
			publicDNS++
		}
	}
	if publicDNS != 1 || fetcher.base.calls["https://docs.example.com/protocol"] != 0 {
		t.Fatal("DNS-failed source was fetched or its failure was not persisted")
	}
}
