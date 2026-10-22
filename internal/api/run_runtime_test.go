package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestRuntimeSnapshotResyncAndPublicHarnessProjection(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "runtime-snapshot", Title: "PRIVATE_PROMPT", Description: "PRIVATE_PROMPT"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext(ctx); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := store.SaveContextSnapshot(ctx, task.ID, domain.ContextSnapshot{Goal: "PRIVATE_PROMPT", WorkingSet: map[string]string{"note": "PRIVATE_PROMPT"}, Contradictions: []string{"Unresolved observation conflict"}}, "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, task.ID, "context.compacted", nil, "user", map[string]any{"reduced_bytes": 100, "original_tool_result_bytes": 300, "retained_tool_result_bytes": 200, "raw_prompt": "PRIVATE_PROMPT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusFailed, nil, "task.failed", "user", map[string]any{"error_code": "completion_blocked"}); err != nil {
		t.Fatal(err)
	}
	h := server.Handler()
	runtime := conversationRequest(t, h, "GET", "/api/runs/"+task.ID+"/runtime", "", "", 200).Body.String()
	if strings.Contains(runtime, "PRIVATE_PROMPT") || !strings.Contains(runtime, "Unresolved observation conflict") || !strings.Contains(runtime, `"terminal_outcome":"blocked"`) {
		t.Fatalf("unsafe snapshot: %s", runtime)
	}
	stream := conversationRequest(t, h, "GET", "/api/tasks/"+task.ID+"/events/stream?after=999999", "", "", 200).Body.String()
	if !strings.Contains(stream, "event: snapshot") || !strings.Contains(stream, "event: resync") || strings.Contains(stream, "PRIVATE_PROMPT") {
		t.Fatalf("bad resync: %s", stream)
	}
	replay := conversationRequest(t, h, "GET", "/api/tasks/"+task.ID+"/events", "", "", 200).Body.String()
	if !strings.Contains(replay, "context.compacted") || strings.Contains(replay, "raw_prompt") {
		t.Fatalf("unsafe projection: %s", replay)
	}
}

func TestPublicReplayTraversesInternalOnlyPages(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "internal-pages", Title: "Task", Description: "Goal"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 501; index++ {
		if _, err := store.RecordEvent(ctx, task.ID, "trace.private", nil, "internal", map[string]any{"index": index}); err != nil {
			t.Fatal(err)
		}
	}
	last, err := store.RecordEvent(ctx, task.ID, "continuation.requested", nil, "user", map[string]any{"attempt": 1, "max_continuations": 1})
	if err != nil {
		t.Fatal(err)
	}
	body := conversationRequest(t, server.Handler(), "GET", fmt.Sprintf("/api/tasks/%s/events?after=1", task.ID), "", "", 200).Body.Bytes()
	var events []domain.Event
	if json.Unmarshal(body, &events) != nil || len(events) != 1 || events[0].Seq != last.Seq {
		t.Fatalf("lost event after internal-only page: %s", body)
	}
}
