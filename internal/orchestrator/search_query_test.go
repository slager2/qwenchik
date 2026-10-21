package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type observedQueryProvider struct {
	calls  atomic.Int32
	store  *sqlite.Store
	taskID string
	t      *testing.T
}

func (p *observedQueryProvider) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	p.calls.Add(1)
	events, e := p.store.EventsAfter(ctx, p.taskID, 0, 100)
	if e != nil {
		return nil, e
	}
	count := 0
	for _, event := range events {
		if event.Type == "query.updated" {
			count++
		}
	}
	if count != 1 {
		p.t.Errorf("query event was not committed before provider call: %d", count)
	}
	return []research.SearchHit{}, nil
}

type queryPersistenceFailure struct {
	Repository
	failure error
}

func (r queryPersistenceFailure) RecordEvent(ctx context.Context, id, kind string, stage *domain.Stage, visibility string, payload map[string]any) (domain.Event, error) {
	if kind == "query.updated" {
		return domain.Event{}, r.failure
	}
	return r.Repository.RecordEvent(ctx, id, kind, stage, visibility, payload)
}

func TestObservedSearchPersistsSafeExecutedQueryOnce(t *testing.T) {
	for _, query := range []string{"Сравнить локальные модели Qwen и Llama", "https://user:private-fixture@example.org/?token=private-fixture", "tvly-dev-private-fixture", "token = private-fixture"} {
		t.Run(query, func(t *testing.T) {
			ctx := context.Background()
			store, e := sqlite.Open(ctx, t.TempDir()+"/query.db")
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			taskID := "query-task"
			if _, e = store.CreateTask(ctx, domain.Task{ID: taskID, Title: "Search"}, ""); e != nil {
				t.Fatal(e)
			}
			provider := &observedQueryProvider{store: store, taskID: taskID, t: t}
			registry := tools.NewRegistry()
			if e = tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider, Fetcher: unavailableResearchFetcher{}}); e != nil {
				t.Fatal(e)
			}
			worker := New(store, nil, registry, Options{})
			arguments, _ := json.Marshal(map[string]string{"query": query})
			stage := domain.StageSearch
			if _, e = worker.executeObservedTool(tools.WithTaskID(ctx, taskID), taskID, &stage, "researcher", "search", arguments, "", "search_1"); e != nil {
				t.Fatal(e)
			}
			events, e := store.EventsAfter(ctx, taskID, 0, 100)
			if e != nil {
				t.Fatal(e)
			}
			count := 0
			hash := sha256.Sum256([]byte(query))
			for _, event := range events {
				if strings.Contains(string(event.Payload), "private-fixture") {
					t.Fatalf("query leaked to observer event %s", event.Payload)
				}
				if event.Type != "query.updated" {
					continue
				}
				count++
				var payload map[string]any
				if e = json.Unmarshal(event.Payload, &payload); e != nil {
					t.Fatal(e)
				}
				if payload["step_id"] != "search_1" || payload["query_fingerprint"] != hex.EncodeToString(hash[:]) || event.Visibility != "user" {
					t.Fatal(payload)
				}
				safe := tools.SafeSearchQuery(query)
				if safe == "" {
					if _, exists := payload["query"]; exists {
						t.Fatal("unsafe query field persisted")
					}
				} else if payload["query"] != safe {
					t.Fatal(payload)
				}
			}
			if count != 1 || provider.calls.Load() != 1 {
				t.Fatalf("events=%d calls=%d", count, provider.calls.Load())
			}
		})
	}
}

func TestObservedSearchDoesNotRunWhenQueryPersistenceFailsOrParentCancelled(t *testing.T) {
	ctx := context.Background()
	store, e := sqlite.Open(ctx, t.TempDir()+"/query-failure.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if _, e = store.CreateTask(ctx, domain.Task{ID: "query-task", Title: "Search"}, ""); e != nil {
		t.Fatal(e)
	}
	provider := &observedQueryProvider{store: store, taskID: "query-task", t: t}
	registry := tools.NewRegistry()
	if e = tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: provider, Fetcher: unavailableResearchFetcher{}}); e != nil {
		t.Fatal(e)
	}
	failure := errors.New("query persistence fixture")
	worker := New(queryPersistenceFailure{Repository: store, failure: failure}, nil, registry, Options{})
	stage := domain.StageSearch
	arguments := json.RawMessage(`{"query":"safe query"}`)
	if _, e = worker.executeObservedTool(ctx, "query-task", &stage, "researcher", "search", arguments, ""); !errors.Is(e, failure) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	worker = New(store, nil, registry, Options{})
	if _, e = worker.executeObservedTool(cancelled, "query-task", &stage, "researcher", "search", arguments, ""); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if provider.calls.Load() != 0 {
		t.Fatal("provider called after persistence failure/cancellation")
	}
	if _, e = worker.executeObservedTool(ctx, "query-task", &stage, "planner", "search", arguments, ""); e == nil {
		t.Fatal("denied role executed search")
	}
	events, e := store.EventsAfter(ctx, "query-task", 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range events {
		if event.Type == "query.updated" {
			t.Fatal("query event emitted for nonexecuted search")
		}
	}
}
