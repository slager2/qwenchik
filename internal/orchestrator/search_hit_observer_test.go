package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestObservedSearchPersistsSafeHitLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/hits.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateTask(ctx, domain.Task{ID: "hits", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, "hits", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext(ctx); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if _, err := store.TransitionTask(ctx, "hits", domain.StatusRunning, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	provider := &diagnosticSearchStub{report: research.SearchReport{
		Hits: []research.SearchHit{
			{Provider: "searxng", URL: "https://example.org/source", Title: "Source", Snippet: "Useful lead"},
			{Provider: "searxng", URL: "https://example.org/source?access_token=PRIVATE", Title: "PRIVATE", Snippet: "PRIVATE"},
		},
		Outcomes: []research.ProviderOutcome{{Provider: "searxng", State: "healthy", Code: "search_ok", ResultCount: 2}},
	}}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider, Fetcher: unavailableResearchFetcher{}}); err != nil {
		t.Fatal(err)
	}
	worker := New(store, nil, registry, Options{})
	stage := domain.StageSearch
	result, err := worker.executeObservedTool(tools.WithTaskID(ctx, "hits"), "hits", &stage, "researcher", "search", json.RawMessage(`{"query":"private question"}`), "", "search_1")
	if err != nil || len(result.([]research.SearchHit)) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	events, err := store.EventsAfter(ctx, "hits", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{}
	for _, event := range events {
		if event.Type != "search.hit" {
			continue
		}
		if strings.Contains(string(event.Payload), "PRIVATE") || strings.Contains(string(event.Payload), "private question") || event.Visibility != "user" {
			t.Fatalf("unsafe event: %s", event.Payload)
		}
		var payload struct {
			StepID string                        `json:"step_id"`
			Hit    research.SearchHitObservation `json:"hit"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.StepID != "search_1" || payload.Hit.Fingerprint == "" || payload.Hit.QueryFingerprint == "" {
			t.Fatalf("incomplete event: %s", event.Payload)
		}
		statuses[payload.Hit.Status]++
	}
	if statuses["discovered"] != 2 || statuses["validated"] != 1 || statuses["rejected"] != 1 {
		t.Fatalf("persisted lifecycle=%v", statuses)
	}
}
