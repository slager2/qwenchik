package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

func newInterruptedFetch(t *testing.T, ctx context.Context) (*sqlite.Store, *Worker, domain.Task, checkpointState, domain.Artifact) {
	t.Helper()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "fetch-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, domain.Task{ID: "fetch-recovery-task", Title: "Fetch page", Description: "Fetch https://docs.example.com/page"}, "")
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, _, err := store.ClaimNext(ctx); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	plan := domain.Plan{
		SchemaVersion: 1, Goal: "fetch a known source", MaxSteps: 1, FinalDeliverable: "source summary",
		Subtasks: []domain.PlanStep{{ID: "fetch_1", Kind: "fetch", Description: "Fetch the supplied URL", ExpectedOutput: "evidence", SuccessCriteria: []string{"source stored"}}},
	}
	state := checkpointState{PlanVersion: 1, CompletedSteps: []string{}, ToolResults: map[string]any{}}
	if _, err := store.SavePlan(ctx, task.ID, plan, state); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	stage := domain.StageSynthesis
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusRunning, &stage, "", "", nil); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.SetStepStatus(ctx, task.ID, "fetch_1", "running"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, task.ID, "step.started", &stage, "user", map[string]any{"step_id": "fetch_1", "description": "Fetch URL"}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	state.CurrentStep = "fetch_1"
	state.PendingTool = "fetch_url"
	state.PendingToolIdempotent = true
	state.PendingArguments = json.RawMessage(`{"url":"https://docs.example.com/page","original_query":"research"}`)
	if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, task.ID, "tool.started", &stage, "user", map[string]any{"step_id": "fetch_1", "tool": "fetch_url", "summary": "Fetching source"}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	artifact := domain.Artifact{ID: "raw-recovery-artifact", TaskID: task.ID, Name: "source.raw", MIMEType: "text/html", SHA256: "content-hash", SizeBytes: 456}
	if _, err := store.SaveArtifact(ctx, artifact, task.ID+"/"+artifact.ID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	worker := New(store, &scriptedLLM{}, tools.NewRegistry(), Options{MaxSteps: 12, TaskTimeout: time.Minute, MaxOutputTokens: 128})
	return store, worker, task, state, artifact
}

func TestReconcileFetchEvidenceWrittenBeforeToolCompleted(t *testing.T) {
	ctx := context.Background()
	store, worker, task, _, artifact := newInterruptedFetch(t, ctx)
	defer store.Close()
	evidence, err := store.SaveEvidence(ctx, domain.Evidence{
		TaskID: task.ID, Provider: "direct_http", OriginalURL: "https://docs.example.com/page", CanonicalURL: "https://docs.example.com/page",
		Title: "Official page", ContentSHA256: "content-hash", ExtractionMethod: "local_html_parser", RawArtifactID: artifact.ID,
		Excerpt: "Project terms.", QuoteSpans: []domain.QuoteSpan{{Start: 0, End: 14, Text: "Project terms."}}, Taint: research.TaintExternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := worker.currentCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, changed, err := worker.reconcileResearchState(ctx, task.ID, state)
	if err != nil || !changed {
		t.Fatalf("reconcile changed=%t err=%v", changed, err)
	}
	if recovered.CurrentStep != "" || recovered.PendingTool != "" || recovered.FetchedBytes != artifact.SizeBytes || !contains(recovered.CompletedSteps, "fetch_1") {
		t.Fatalf("fetch recovery state = %#v", recovered)
	}
	result, ok := recovered.ToolResults["fetch_1"].(map[string]any)
	if !ok || result["evidence_id"] != evidence.ID || result["requested_url"] != "https://docs.example.com/page" {
		t.Fatalf("recovered fetch result = %#v", recovered.ToolResults["fetch_1"])
	}
	if _, err := worker.repo.SaveCheckpoint(ctx, task.ID, 1, recovered); err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var recoveredTool, completedStep bool
	for _, event := range events {
		if event.Type == "tool.completed" && string(event.Payload) != "" && containsJSONString(event.Payload, `"recovered":true`) {
			recoveredTool = true
		}
		if event.Type == "step.completed" && containsJSONString(event.Payload, `"step_id":"fetch_1"`) {
			completedStep = true
		}
	}
	if !recoveredTool || !completedStep {
		t.Fatalf("recovery events missing: tool.completed=%t step.completed=%t events=%#v", recoveredTool, completedStep, events)
	}
}

func TestReconcileFetchWithoutEvidenceFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, worker, task, _, _ := newInterruptedFetch(t, ctx)
	defer store.Close()
	state, err := worker.currentCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := worker.reconcileResearchState(ctx, task.ID, state); !errors.Is(err, errFetchOutcomeUnknown) {
		t.Fatalf("interrupted fetch without evidence error = %v, want errFetchOutcomeUnknown", err)
	}
	if _, canReplace, err := worker.canReplacePlan(ctx, task.ID); !errors.Is(err, errFetchOutcomeUnknown) || canReplace {
		t.Fatalf("unknown fetch outcome allowed plan replacement: canReplace=%t err=%v", canReplace, err)
	}
}

func TestReconcileToolCompletedEventWithoutCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, worker, task, _, _ := newInterruptedFetch(t, ctx)
	defer store.Close()
	if _, err := store.RecordEvent(ctx, task.ID, "tool.completed", nil, "user", map[string]any{
		"step_id": "fetch_1", "tool": "fetch_url", "result": map[string]any{
			"evidence_id": "persisted-evidence", "url": "https://docs.example.com/page", "requested_url": "https://docs.example.com/page",
			"title": "Source", "excerpt": "Excerpt", "content_sha256": "hash", "taint": research.TaintExternal, "fetched_bytes": int64(321),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, task.ID, "tool.started", nil, "user", map[string]any{"step_id": "search_1", "tool": "search"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, task.ID, "tool.completed", nil, "user", map[string]any{"step_id": "search_1", "tool": "search", "result": []any{}}); err != nil {
		t.Fatal(err)
	}
	state, err := worker.currentCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, changed, err := worker.reconcileResearchState(ctx, task.ID, state)
	if err != nil || !changed {
		t.Fatalf("reconcile changed=%t err=%v", changed, err)
	}
	if recovered.CurrentStep != "" || recovered.PendingTool != "" || recovered.FetchedBytes != 321 || recovered.SearchQueries != 1 || !contains(recovered.CompletedSteps, "fetch_1") {
		t.Fatalf("completed event recovery state = %#v", recovered)
	}
	if result, ok := recovered.ToolResults["fetch_1"].(map[string]any); !ok || result["evidence_id"] != "persisted-evidence" {
		t.Fatalf("completed tool result was not restored: %#v", recovered.ToolResults)
	}
	if _, err := store.SaveCheckpoint(ctx, task.ID, 1, recovered); err != nil {
		t.Fatal(err)
	}
	second, changed, err := worker.reconcileResearchState(ctx, task.ID, recovered)
	if err != nil || changed || second.FetchedBytes != 321 || second.SearchQueries != 1 {
		t.Fatalf("second reconciliation double-counted budgets: state=%#v changed=%t err=%v", second, changed, err)
	}
}

func containsJSONString(raw json.RawMessage, expected string) bool {
	return strings.Contains(string(raw), expected)
}

func TestReconcileFailedFetchByteBoundary(t *testing.T) {
	for _, boundary := range []string{"before_failure", "after_failure", "legacy_checkpoint"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			store, worker, task, state, _ := newInterruptedFetch(t, ctx)
			defer store.Close()
			const failedBytes = int64(3 << 20)
			const successBytes = int64(4 << 20)
			worker.options.MaxFetchBytes = 7 << 20
			if boundary == "after_failure" {
				if err := worker.recordFetchCandidateFailure(ctx, task.ID, nil, "fetch_1", "https://docs.example.com/page", "source_unavailable", false, &state, failedBytes); err != nil {
					t.Fatal(err)
				}
			} else if boundary == "legacy_checkpoint" {
				state.FetchedBytes = failedBytes
				if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := store.RecordEvent(ctx, task.ID, "tool.failed", nil, "audit", map[string]any{"step_id": "fetch_1", "tool": "fetch_url", "target_url": "https://docs.example.com/page", "fetched_bytes": failedBytes}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.RecordEvent(ctx, task.ID, "tool.started", nil, "user", map[string]any{"step_id": "fetch_1", "tool": "fetch_url"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RecordEvent(ctx, task.ID, "tool.completed", nil, "user", map[string]any{"step_id": "fetch_1", "tool": "fetch_url", "result": map[string]any{"evidence_id": "success", "fetched_bytes": successBytes}}); err != nil {
				t.Fatal(err)
			}
			state, err := worker.currentCheckpoint(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			recovered, _, err := worker.reconcileResearchState(ctx, task.ID, state)
			if err != nil || recovered.FetchedBytes != failedBytes+successBytes || recovered.FetchedBytes != worker.options.MaxFetchBytes || !contains(recovered.CompletedSteps, "fetch_1") {
				t.Fatalf("recovered=%#v err=%v", recovered, err)
			}
			if _, err := store.SaveCheckpoint(ctx, task.ID, 1, recovered); err != nil {
				t.Fatal(err)
			}
			second, changed, err := worker.reconcileResearchState(ctx, task.ID, recovered)
			if err != nil || changed || second.FetchedBytes != failedBytes+successBytes {
				t.Fatalf("repeated recovery=%#v changed=%t err=%v", second, changed, err)
			}
		})
	}
}

func TestReconcileFailedFetchByteBounds(t *testing.T) {
	for _, count := range []int64{-1, math.MaxInt64} {
		t.Run(strconv.FormatInt(count, 10), func(t *testing.T) {
			ctx := context.Background()
			store, worker, task, state, _ := newInterruptedFetch(t, ctx)
			defer store.Close()
			state.FetchedBytes = 1
			if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RecordEvent(ctx, task.ID, "tool.failed", nil, "audit", map[string]any{"step_id": "fetch_1", "tool": "fetch_url", "fetched_bytes": count}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := worker.reconcileResearchState(ctx, task.ID, state); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("invalid byte ledger accepted: %v", err)
			}
		})
	}
}

func TestReconcileFailedFetchBeforeCheckpointAndOrphanEvidence(t *testing.T) {
	ctx := context.Background()
	store, worker, task, state, artifact := newInterruptedFetch(t, ctx)
	defer store.Close()
	const failedBytes = int64(3 << 20)
	if err := worker.recordFetchCandidateFailure(ctx, task.ID, nil, "fetch_1", "https://unavailable.example.com/page", "source_unavailable", false, &state, failedBytes); err != nil {
		t.Fatal(err)
	}
	state.PendingTool = "fetch_url"
	state.PendingToolIdempotent = true
	state.PendingArguments = json.RawMessage(`{"url":"https://docs.example.com/page"}`)
	if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, task.ID, "tool.started", nil, "user", map[string]any{"step_id": "fetch_1", "tool": "fetch_url"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveEvidence(ctx, domain.Evidence{
		TaskID: task.ID, Provider: "direct_http", OriginalURL: "https://docs.example.com/page", CanonicalURL: "https://docs.example.com/page",
		Title: "Page", ContentSHA256: "content-hash", ExtractionMethod: "local_html_parser", RawArtifactID: artifact.ID,
		Excerpt: "Source text.", QuoteSpans: []domain.QuoteSpan{{Start: 0, End: 12, Text: "Source text."}}, Taint: research.TaintExternal,
	}); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := worker.reconcileResearchState(ctx, task.ID, state)
	if err != nil || recovered.FetchedBytes != failedBytes+artifact.SizeBytes || !contains(recovered.CompletedSteps, "fetch_1") {
		t.Fatalf("orphan recovery=%#v err=%v", recovered, err)
	}
	if _, err := store.SaveCheckpoint(ctx, task.ID, 1, recovered); err != nil {
		t.Fatal(err)
	}
	second, changed, err := worker.reconcileResearchState(ctx, task.ID, recovered)
	if err != nil || changed || second.FetchedBytes != recovered.FetchedBytes {
		t.Fatalf("repeated orphan recovery=%#v changed=%t err=%v", second, changed, err)
	}
}

type recoveryStageExtractor struct {
	store  *sqlite.Store
	taskID string
	calls  int
	t      *testing.T
}

func (e *recoveryStageExtractor) Extract(ctx context.Context, doc research.FetchedDocument) (research.ExtractedDocument, error) {
	e.calls++
	task, err := e.store.GetTask(ctx, e.taskID)
	if err != nil || task.Stage == nil || *task.Stage != domain.StageExtract {
		e.t.Fatalf("extraction stage=%v err=%v", task.Stage, err)
	}
	if e.calls == 1 {
		return research.ExtractedDocument{}, research.ErrUnsupportedMIME
	}
	return researchExtractorStub{}.Extract(ctx, doc)
}

func TestFetchRecoveryStagesAcrossCandidateRetriesAndSequentialSteps(t *testing.T) {
	ctx := context.Background()
	store, worker, task, state, _ := newInterruptedFetch(t, ctx)
	defer store.Close()
	artifactStore, err := artifacts.New(t.TempDir(), 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	extractor := &recoveryStageExtractor{store: store, taskID: task.ID, t: t}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{Fetcher: researchFetcherStub{}, Extractor: extractor, Artifacts: artifactStore, Repository: store, MaxFetchBytes: 100_000}); err != nil {
		t.Fatal(err)
	}
	worker.tools = registry
	urls := []string{"https://docs.example.com/protocol", "https://reference.example.net/protocol", "https://benchmark.example.org/protocol"}
	registry.SeedTaskURLs(task.ID, strings.Join(urls, " "))
	ctx = tools.WithTaskID(ctx, task.ID)
	stage := domain.StageSynthesis
	attempted := make(map[string]struct{})
	for index, candidates := range [][]string{urls[:2], urls[2:]} {
		step := domain.PlanStep{ID: "fetch_1", Kind: "fetch"}
		if index == 1 {
			step.ID = "fetch_2"
		}
		state.CurrentStep = step.ID
		decision := actionDecision{}
		decision.ToolCall = &struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}{Name: "fetch_url", Arguments: json.RawMessage(`{"url":"` + candidates[0] + `"}`)}
		_, _, exhausted, err := worker.executeFetchCandidates(ctx, task.ID, &stage, step, "researcher", "read", candidates, &decision, &state, attempted, "")
		if err != nil || exhausted || stage != domain.StageExtract {
			t.Fatalf("step=%s exhausted=%t stage=%s err=%v", step.ID, exhausted, stage, err)
		}
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var stages []domain.Stage
	for _, event := range events {
		if event.Type == "task.status_changed" && event.Stage != nil && (*event.Stage == domain.StageFetch || *event.Stage == domain.StageExtract) {
			stages = append(stages, *event.Stage)
		}
		if event.Type == "tool.failed" && event.Visibility == "audit" {
			var payload struct {
				FetchedBytes int64 `json:"fetched_bytes"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.FetchedBytes <= 0 || payload.FetchedBytes != state.FetchedBytes {
				t.Fatalf("failed bytes not preserved: %s state=%d err=%v", event.Payload, state.FetchedBytes, err)
			}
		}
	}
	if len(stages) != 6 {
		t.Fatalf("stage transitions=%v", stages)
	}
	for index, stage := range stages {
		want := domain.StageFetch
		if index%2 == 1 {
			want = domain.StageExtract
		}
		if stage != want {
			t.Fatalf("stage transitions=%v", stages)
		}
	}
	state.FetchedBytes = worker.options.MaxFetchBytes
	decision := actionDecision{}
	decision.ToolCall = &struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: "fetch_url", Arguments: json.RawMessage(`{"url":"https://docs.example.com/protocol"}`)}
	_, _, _, err = worker.executeFetchCandidates(ctx, task.ID, &stage, domain.PlanStep{ID: "fetch_3", Kind: "fetch"}, "researcher", "read", urls[:1], &decision, &state, make(map[string]struct{}), "")
	if !errors.Is(err, errFetchBudgetExceeded) || extractor.calls != 3 {
		t.Fatalf("exhausted budget allowed another fetch: calls=%d err=%v", extractor.calls, err)
	}
}
