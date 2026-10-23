package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func invocationFixture(taskID, kind string, attempt int) domain.Invocation {
	return domain.Invocation{TaskID: taskID, StepID: "step_1", Role: "collector", Kind: kind, OperationID: "stable-operation", InputHash: strings.Repeat("a", 64), IdempotencyKey: "attempt-" + string(rune('0'+attempt)), Attempt: attempt}
}

func inferenceSummaryFixture(invocation domain.Invocation, source string) *domain.InferenceSummary {
	at := time.Now().UTC().Truncate(time.Microsecond)
	status := domain.InferenceStatusCompleted
	switch invocation.Status {
	case "failed":
		status = domain.InferenceStatusFailed
	case "unknown":
		status = domain.InferenceStatusCancelled
	}
	unavailableInt := func(unit string) domain.Metric[int64] { return domain.NewUnavailableMetric[int64](unit, at) }
	unavailableFloat := func(unit string) domain.Metric[float64] { return domain.NewUnavailableMetric[float64](unit, at) }
	return &domain.InferenceSummary{
		InvocationID:     invocation.ID,
		InvocationStatus: domain.InvocationStatus(invocation.Status),
		SnapshotAt:       at,
		Inference: domain.InferenceTelemetry{
			Status: status, Source: source, UpdatedAt: at,
			RequestedTokens: unavailableInt(domain.MetricUnitTokens), PromptTokens: unavailableInt(domain.MetricUnitTokens),
			OutputTokens: unavailableInt(domain.MetricUnitTokens), TotalTokens: unavailableInt(domain.MetricUnitTokens),
			CachedPromptTokens:    unavailableInt(domain.MetricUnitTokens),
			PromptTokensPerSecond: unavailableFloat(domain.MetricUnitTokensPerSecond),
			OutputTokensPerSecond: unavailableFloat(domain.MetricUnitTokensPerSecond),
			TTFTMS:                unavailableFloat(domain.MetricUnitMilliseconds), PromptMS: unavailableFloat(domain.MetricUnitMilliseconds),
			GenerationMS:  unavailableFloat(domain.MetricUnitMilliseconds),
			ContextTokens: unavailableInt(domain.MetricUnitTokens), ContextWindow: unavailableInt(domain.MetricUnitTokens),
		},
	}
}

func TestInvocationIntentReservationAndCheckpointRollback(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	prior, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"pending_tool": "search", "pending_arguments": map[string]any{"query": "retained", "large_id": json.Number("9007199254740993")}, "partial": "saved"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_intent_checkpoint BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'intent fixture'); END`); err != nil {
		t.Fatal(err)
	}
	in := invocationFixture(turn.Run.ID, "tool.search", 1)
	if _, err := s.BeginInvocation(ctx, in); err == nil {
		t.Fatal("intent survived checkpoint failure")
	}
	session, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || session.Counters.SearchQueries != 0 {
		t.Fatalf("reservation escaped rollback: %#v %v", session, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type IN ('invocation.started','research.operation_reserved')`, turn.Run.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("intent events escaped rollback: %d %v", count, err)
	}
	latest, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || latest.ID != prior.ID {
		t.Fatalf("prior lost: %#v %v", latest, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_intent_checkpoint`); err != nil {
		t.Fatal(err)
	}
	started, err := s.BeginInvocation(ctx, in)
	if err != nil || started.Status != "started" || started.StartedAt == nil {
		t.Fatalf("intent: %#v %v", started, err)
	}
	latest, err = s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || latest.ParentID != prior.ID || latest.StateHash == "" {
		t.Fatalf("checkpoint: %#v %v", latest, err)
	}
	state := latest.State.(map[string]any)
	if !strings.Contains(mustCheckpointJSON(t, s, turn.Run.ID, latest.ID), `"large_id":9007199254740993`) {
		t.Fatal("intent changed exact pending arguments")
	}
	if state["pending_invocation_id"] != started.ID || state["pending_tool"] != "search" || state["partial"] != "saved" || state["pending_arguments"] == nil {
		t.Fatalf("intent lost worker state: %#v", state)
	}
	if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"search_queries": 0}); err != nil {
		t.Fatal(err)
	}
	latest, err = s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || latest.State.(map[string]any)["pending_invocation_id"] != started.ID {
		t.Fatalf("worker checkpoint lost active intent: %#v %v", latest, err)
	}
	session, err = s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || session.Counters.SearchQueries != 1 {
		t.Fatalf("counter reset: %#v %v", session, err)
	}
}

func TestInvocationDuplicateStartsAndAbandonedAttempts(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	in := invocationFixture(turn.Run.ID, "llm.complete", 1)
	var wg sync.WaitGroup
	var ids sync.Map
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started, err := s.BeginInvocation(ctx, in)
			if err != nil {
				t.Error(err)
				return
			}
			ids.Store(started.ID, true)
		}()
	}
	wg.Wait()
	unique := 0
	ids.Range(func(_, _ any) bool { unique++; return true })
	if unique != 1 {
		t.Fatalf("duplicate intents: %d", unique)
	}
	changed := in
	changed.InputHash = strings.Repeat("b", 64)
	if _, err := s.BeginInvocation(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed hash accepted: %v", err)
	}
	changed = in
	changed.IdempotencyKey = "other-key"
	if _, err := s.BeginInvocation(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate attempt accepted: %v", err)
	}
	for attempt := 2; attempt <= 4; attempt++ {
		if _, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", attempt)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.InvocationsForOperation(ctx, turn.Run.ID, in.OperationID)
	if err != nil || len(items) != 4 {
		t.Fatalf("abandoned attempts: %#v %v", items, err)
	}
	for i, item := range items {
		if item.Status != "started" || item.Attempt != i+1 || item.FinishedAt != nil {
			t.Fatalf("abandoned result invented: %#v", item)
		}
	}
	session, err := s.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || session.Counters.LLMCalls != 4 || session.Counters.HTTPAttempts != 4 {
		t.Fatalf("attempt budget: %#v %v", session, err)
	}
	if _, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", 5)); err == nil {
		t.Fatal("unbounded operation history")
	}
}

func TestInvocationFinishRollbackCachedReopenAndConflicts(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"pending_tool": "text.inspect", "pending_arguments": map[string]any{"text": "retained"}}); err != nil {
		t.Fatal(err)
	}
	started, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "tool.text.inspect", 1))
	if err != nil {
		t.Fatal(err)
	}
	done := started
	done.Status = "completed"
	result := map[string]any{"answer": "cached"}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_finish_checkpoint BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'finish fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocation(ctx, done, result); err == nil {
		t.Fatal("finish survived checkpoint failure")
	}
	items, err := s.InvocationsForOperation(ctx, turn.Run.ID, started.OperationID)
	if err != nil || len(items) != 1 || items[0].Status != "started" {
		t.Fatalf("partial finish: %#v %v", items, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_finish_checkpoint`); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocation(ctx, done, result); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocation(ctx, done, result); err != nil {
		t.Fatalf("duplicate finish: %v", err)
	}
	var summaries int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='inference.summary'`, turn.Run.ID).Scan(&summaries); err != nil || summaries != 0 {
		t.Fatalf("nil summary appended %d events: %v", summaries, err)
	}
	runtime, err := s.RunRuntime(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := runtime["inference_summary"]; present {
		t.Fatalf("runtime exposed absent inference summary: %#v", runtime["inference_summary"])
	}
	if err := s.FinishInvocation(ctx, done, map[string]any{"answer": "changed"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed result: %v", err)
	}
	wrong := done
	wrong.Attempt = 2
	if err := s.FinishInvocation(ctx, wrong, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed identity: %v", err)
	}
	wrong = done
	wrong.Status = "failed"
	if err := s.FinishInvocation(ctx, wrong, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed outcome: %v", err)
	}
	if err := s.FinishInvocation(ctx, done, strings.Repeat("x", 128<<10)); !errors.Is(err, ErrInvocationInvalid) {
		t.Fatalf("oversized output: %v", err)
	}
	cp, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := cp.State.(map[string]any)
	if state["pending_invocation_id"] != nil || state["pending_invocation"] != nil || state["pending_tool"] != "text.inspect" || state["pending_arguments"] == nil {
		t.Fatalf("finish changed pending tool: %#v", state)
	}
	var public int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type LIKE 'invocation.%' AND visibility<>'internal'`, turn.Run.ID).Scan(&public); err != nil || public != 0 {
		t.Fatalf("public output: %d %v", public, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, err = s.InvocationsForOperation(ctx, turn.Run.ID, started.OperationID)
	if err != nil || len(items) != 1 || items[0].Status != "completed" || len(items[0].OutputHash) != 64 || string(items[0].Output) != `{"answer":"cached"}` || items[0].FinishedAt == nil {
		t.Fatalf("cached result: %#v %v", items, err)
	}
	again, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "tool.text.inspect", 1))
	if err != nil || again.Status != "completed" || string(again.Output) != string(items[0].Output) {
		t.Fatalf("cached begin: %#v %v", again, err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT payload_json FROM events WHERE task_id=? AND type='invocation.completed'`, turn.Run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE events SET payload_json=json_set(payload_json,'$.result.answer','corrupt') WHERE task_id=? AND type='invocation.completed'`, turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InvocationsForOperation(ctx, turn.Run.ID, started.OperationID); !errors.Is(err, ErrInvocationInvalid) {
		t.Fatalf("corrupt output read: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE events SET payload_json=? WHERE task_id=? AND type='invocation.completed'`, raw, turn.Run.ID); err != nil {
		t.Fatal(err)
	}
}

func TestInvocationInferenceSummaryPersistsSeparatelyAndOnlyOncePerAttempt(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	first, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", 1))
	if err != nil {
		t.Fatal(err)
	}
	first.Status, first.ErrorCode = "failed", "model_timeout"
	firstSummary := inferenceSummaryFixture(first, "untrusted-provider-value")
	if err := s.FinishInvocationWithTelemetry(ctx, first, nil, firstSummary); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocationWithTelemetry(ctx, first, nil, firstSummary); err != nil {
		t.Fatalf("duplicate finish: %v", err)
	}

	second, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", 2))
	if err != nil {
		t.Fatal(err)
	}
	second.Status = "completed"
	result := map[string]any{"answer": "retry result"}
	secondSummary := inferenceSummaryFixture(second, "llama.cpp")
	if err := s.FinishInvocationWithTelemetry(ctx, second, result, secondSummary); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocationWithTelemetry(ctx, second, result, secondSummary); err != nil {
		t.Fatalf("duplicate retry finish: %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='inference.summary' AND visibility='internal'`, turn.Run.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("internal summary event count = %d, want 2: %v", count, err)
	}
	for _, item := range []struct {
		invocation domain.Invocation
		wantSource string
	}{{first, ""}, {second, "llama.cpp"}} {
		var summarySeq, finishSeq int64
		var raw, visibility string
		if err := s.db.QueryRow(`SELECT seq,payload_json,visibility FROM events WHERE task_id=? AND type='inference.summary' AND json_extract(payload_json,'$.invocation_id')=?`, turn.Run.ID, item.invocation.ID).Scan(&summarySeq, &raw, &visibility); err != nil {
			t.Fatal(err)
		}
		finishType := "invocation.failed"
		if item.invocation.Status == "completed" {
			finishType = "invocation.completed"
		}
		if err := s.db.QueryRow(`SELECT seq FROM events WHERE task_id=? AND type=? AND json_extract(payload_json,'$.invocation.id')=?`, turn.Run.ID, finishType, item.invocation.ID).Scan(&finishSeq); err != nil {
			t.Fatal(err)
		}
		if visibility != "internal" || summarySeq >= finishSeq || len(raw) > domain.InferenceSummaryMaxBytes {
			t.Fatalf("summary order/visibility/size invalid: seq=%d finish=%d visibility=%q bytes=%d", summarySeq, finishSeq, visibility, len(raw))
		}
		var persisted domain.InferenceSummary
		if err := json.Unmarshal([]byte(raw), &persisted); err != nil || persisted.Validate() != nil || persisted.Inference.Source != item.wantSource {
			t.Fatalf("persisted summary = %#v, unmarshal error=%v, validation=%v", persisted, err, persisted.Validate())
		}
		var payload map[string]json.RawMessage
		if err := s.db.QueryRow(`SELECT payload_json FROM events WHERE task_id=? AND type=? AND json_extract(payload_json,'$.invocation.id')=?`, turn.Run.ID, finishType, item.invocation.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		if _, present := payload["inference"]; present {
			t.Fatalf("telemetry was copied into invocation event: %s", raw)
		}
	}
	items, err := s.InvocationsForOperation(ctx, turn.Run.ID, first.OperationID)
	if err != nil || len(items) != 2 || string(items[1].Output) != `{"answer":"retry result"}` {
		t.Fatalf("invocation output contains telemetry or changed: %#v %v", items, err)
	}
	if _, err := s.RecordEvent(ctx, turn.Run.ID, "inference.summary", nil, "user", map[string]any{"malformed": true}); err != nil {
		t.Fatalf("append non-internal fixture event: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := s.RunRuntime(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := runtime["inference_summary"].(domain.InferenceSummary)
	if !ok || persisted.InvocationID != second.ID || persisted.Inference.Source != "llama.cpp" {
		t.Fatalf("reopened runtime summary = %#v", runtime["inference_summary"])
	}
	if _, err := s.db.Exec(`UPDATE events SET payload_json='{}' WHERE task_id=? AND type='inference.summary' AND visibility='internal' AND json_extract(payload_json,'$.invocation_id')=?`, turn.Run.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunRuntime(ctx, turn.Run.ID); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("malformed persisted summary did not fail closed: %v", err)
	}
}

func TestInvocationInferenceSummaryAppendFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	first, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_inference_summary BEFORE INSERT ON events WHEN NEW.type='inference.summary' BEGIN SELECT RAISE(ABORT,'injected summary failure'); END`); err != nil {
		t.Fatal(err)
	}
	first.Status, first.ErrorCode = "failed", "model_timeout"
	if err := s.FinishInvocationWithTelemetry(ctx, first, nil, inferenceSummaryFixture(first, "llama.cpp")); err != nil {
		t.Fatalf("summary append failure failed invocation finish: %v", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_inference_summary`); err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", 2))
	if err != nil {
		t.Fatal(err)
	}
	second.Status = "completed"
	invalid := inferenceSummaryFixture(second, "llama.cpp")
	invalid.InvocationID = "different-invocation"
	if err := s.FinishInvocationWithTelemetry(ctx, second, map[string]bool{"ok": true}, invalid); err != nil {
		t.Fatalf("invalid optional summary failed invocation finish: %v", err)
	}
	items, err := s.InvocationsForOperation(ctx, turn.Run.ID, first.OperationID)
	if err != nil || len(items) != 2 || items[0].Status != "failed" || items[1].Status != "completed" {
		t.Fatalf("invocation finish did not persist: %#v %v", items, err)
	}
	var summaries int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='inference.summary'`, turn.Run.ID).Scan(&summaries); err != nil || summaries != 0 {
		t.Fatalf("failed/invalid summary append left %d events: %v", summaries, err)
	}
}

func TestInvocationCheckpointCorruptionAndLeaseLoss(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if err := s.EnableWorkerLeases("worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	if _, err := s.RecordEvent(ctx, turn.Run.ID, "inference.summary", nil, "internal", map[string]any{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("unleased inference summary event: %v", err)
	}
	in, err := s.BeginInvocation(leased, invocationFixture(turn.Run.ID, "llm.complete", 1))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE checkpoints SET state_hash='corrupt' WHERE id=?`, cp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginInvocation(leased, invocationFixture(turn.Run.ID, "llm.complete", 2)); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("corrupt parent accepted: %v", err)
	}
	in.Status = "failed"
	in.ErrorCode = "model_invalid_output"
	if err := s.FinishInvocation(leased, in, nil); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("corrupt finish parent accepted: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE checkpoints SET state_hash=? WHERE id=?`, cp.StateHash, cp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET tasklease_expires_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocation(leased, in, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired finish: %v", err)
	}
	if _, err := s.BeginInvocation(leased, invocationFixture(turn.Run.ID, "llm.complete", 2)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired begin: %v", err)
	}
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocation(context.WithoutCancel(leased), in, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("cancelled finish: %v", err)
	}
}

func TestChatInvocationFailureAndActiveLifecycleGuards(t *testing.T) {
	ctx := context.Background()
	s, turn := conversationFixture(t, "chat")
	if _, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.stream", 1)); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued begin: %v", err)
	}
	claimResearchFixture(t, s)
	in, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.stream", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET deadline_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.stream", 2)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired begin: %v", err)
	}
	in.Status = "unknown"
	in.ErrorCode = "stream_interrupted"
	if err := s.FinishInvocation(ctx, in, nil); err != nil {
		t.Fatalf("known outcome after deadline: %v", err)
	}
	items, err := s.InvocationsForOperation(ctx, turn.Run.ID, in.OperationID)
	if err != nil || len(items) != 1 || items[0].Status != "unknown" || string(items[0].Output) != "null" {
		t.Fatalf("stream result: %#v %v", items, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='research.operation_reserved'`, turn.Run.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("chat research reservation: %d %v", count, err)
	}
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusPaused, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInvocation(ctx, in, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("paused finish: %v", err)
	}
}

func TestCommitStepResultFinishesPendingInvocationExactlyOnce(t *testing.T) {
	for _, finishFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "helper", true: "wrapper"}[finishFirst], func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			plan := domain.Plan{SchemaVersion: 1, Goal: "Atomic outcome", MaxSteps: 1, FinalDeliverable: "Answer", Subtasks: []domain.PlanStep{{ID: "step_1", Kind: "search", Description: "Search", SuccessCriteria: []string{"Found"}}}}
			if _, err := s.SavePlan(ctx, turn.Run.ID, plan, map[string]any{"pending_tool": "search"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusRunning, nil, "", "user", nil); err != nil {
				t.Fatal(err)
			}
			if err := s.SetStepStatus(ctx, turn.Run.ID, "step_1", "running"); err != nil {
				t.Fatal(err)
			}
			in, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "tool.search", 1))
			if err != nil {
				t.Fatal(err)
			}
			result := map[string]any{"found": true}
			cp, err := s.LatestCheckpoint(ctx, turn.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			state := cp.State.(map[string]any)
			state["completed_steps"] = []string{"step_1"}
			state["tool_results"] = map[string]any{"step_1": result}
			if finishFirst {
				in.Status = "completed"
				if err := s.FinishInvocation(ctx, in, result); err != nil {
					t.Fatal(err)
				}
			}
			cp, err = s.CommitStepResult(ctx, turn.Run.ID, "step_1", "search", "found", result, state)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='invocation.completed'`, turn.Run.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("duplicate completion: %d %v", count, err)
			}
			items, err := s.InvocationsForOperation(ctx, turn.Run.ID, in.OperationID)
			if err != nil || len(items) != 1 || items[0].Status != "completed" {
				t.Fatalf("outcome: %#v %v", items, err)
			}
			if state := cp.State.(map[string]any); state["pending_invocation_id"] != nil {
				t.Fatalf("pending resurrected: %#v", state)
			}
		})
	}
}

func TestResourceHealthyCyclesConsumeDurableProbeBudget(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	for cycle := 1; cycle <= 3; cycle++ {
		claimResearchFixture(t, s)
		paused, err := s.PauseTaskForResource(ctx, turn.Run.ID, "oom", time.Now().Add(-time.Second), map[string]any{"partial": "kept"})
		if err != nil || paused.ResourceProbeAttempts != cycle-1 {
			t.Fatalf("cycle reset attempts: %#v %v", paused, err)
		}
		task, err := s.ResolveResourceProbe(ctx, turn.Run.ID, true, "healthy", time.Time{}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if cycle <= 2 {
			if task.Status != domain.StatusQueued || task.ResourceProbeAttempts != cycle {
				t.Fatalf("healthy probe uncounted: %#v", task)
			}
		} else if task.Status != domain.StatusFailed || task.TerminalOutcome != "blocked" || task.ResourceProbeAttempts != 2 {
			t.Fatalf("cycle limit bypassed: %#v", task)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer s.Close()
	cp, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || cp.State.(map[string]any)["partial"] != "kept" {
		t.Fatalf("partial lost: %#v %v", cp, err)
	}
	var attempts []int
	rows, err := s.db.Query(`SELECT json_extract(payload_json,'$.resource_probe_attempts') FROM events WHERE task_id=? AND type='run.paused' ORDER BY seq`, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var count int
		if err := rows.Scan(&count); err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, count)
	}
	raw, _ := json.Marshal(attempts)
	if string(raw) != "[0,1,2]" {
		t.Fatalf("pause event counter reset: %s", raw)
	}
	retry, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Operation: "retry", SourceRunID: turn.Run.ID}, "resource-manual-retry")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.GetTask(ctx, retry.Run.ID)
	if err != nil || fresh.ResourceProbeAttempts != 0 || fresh.PauseReason != "" {
		t.Fatalf("manual retry inherited probe budget: %#v %v", fresh, err)
	}
	prior, err := s.GetTask(ctx, turn.Run.ID)
	if err != nil || prior.ResourceProbeAttempts != 2 {
		t.Fatalf("manual retry reset prior run: %#v %v", prior, err)
	}
}

func TestInvocationFinishRetainsNewerPendingAttempt(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	first, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", 1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "llm.complete", 2))
	if err != nil {
		t.Fatal(err)
	}
	first.Status = "unknown"
	first.ErrorCode = "completion_interrupted"
	if err := s.FinishInvocation(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	cp, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || cp.State.(map[string]any)["pending_invocation_id"] != second.ID {
		t.Fatalf("newer intent cleared: %#v %v", cp, err)
	}
	items, err := s.InvocationsForOperation(ctx, turn.Run.ID, first.OperationID)
	if err != nil || len(items) != 2 || items[0].Status != "unknown" || items[1].Status != "started" {
		t.Fatalf("attempt history lost: %#v %v", items, err)
	}
}

func TestInvocationFailurePersistsWithIndependentLeasedContext(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if err := s.EnableWorkerLeases("worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	in, err := s.BeginInvocation(leased, invocationFixture(turn.Run.ID, "llm.complete", 1))
	if err != nil {
		t.Fatal(err)
	}
	in.Status = "failed"
	in.ErrorCode = "model_timeout"
	modelCtx, cancel := context.WithCancel(leased)
	cancel()
	if err := s.FinishInvocation(modelCtx, in, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller accepted: %v", err)
	}
	if err := s.FinishInvocation(context.WithoutCancel(modelCtx), in, nil); err != nil {
		t.Fatalf("independent persistence: %v", err)
	}
	items, err := s.InvocationsForOperation(ctx, turn.Run.ID, in.OperationID)
	if err != nil || len(items) != 1 || items[0].Status != "failed" || string(items[0].Output) != "null" {
		t.Fatalf("failure: %#v %v", items, err)
	}
	var payload string
	if err := s.db.QueryRow(`SELECT payload_json FROM events WHERE task_id=? AND type='invocation.failed'`, turn.Run.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Invocation domain.Invocation `json:"invocation"`
		Result     json.RawMessage   `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil || len(event.Invocation.Output) != 0 || string(event.Result) != "null" {
		t.Fatalf("failure payload: %s %v", payload, err)
	}
}
