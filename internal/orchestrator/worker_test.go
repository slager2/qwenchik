package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type scriptedLLM struct {
	mu        sync.Mutex
	responses []string
	requests  int
	seen      []llm.ChatRequest
}

func (s *scriptedLLM) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, request)
	if s.requests >= len(s.responses) {
		return llm.ChatResponse{}, errors.New("no scripted response")
	}
	response := s.responses[s.requests]
	s.requests++
	return llm.ChatResponse{Content: response}, nil
}

func (s *scriptedLLM) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, errors.New("stream is not used in worker test")
}

func TestWorkerCompletesOneSafeToolStep(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/worker.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "worker-task", Title: "Count words", Description: "count three words here"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, domain.Message{ID: "clarification-1", TaskID: task.ID, Role: "user", Content: "Keep the final answer concise."}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim task: ok=%t err=%v", ok, err)
	}
	model := &scriptedLLM{responses: []string{
		`{"schema_version":1,"goal":"count words","subtasks":[{"id":"step_1","kind":"analysis","description":"Inspect the text","dependencies":[],"expected_output":"word count","success_criteria":["count returned"]}],"max_steps":1,"final_deliverable":"short answer"}`,
		`{"status":"continue","summary":"Counted the words in the supplied text.","tool_call":{"name":"text.inspect","arguments":{"text":"count three words here"}}}`,
		`{"complete":true,"summary":"The text was inspected and the tool result is available.","missing":[]}`,
		`The supplied text contains four words.`,
	}}
	worker := New(store, model, tools.NewRegistry(), Options{MaxSteps: 12, TaskTimeout: time.Minute, MaxOutputTokens: 512})
	worker.runTask(ctx, claimed)

	completed, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.StatusCompleted {
		t.Fatalf("task status = %q, want completed", completed.Status)
	}
	messages, err := store.Messages(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[1].Content != "The supplied text contains four words." {
		t.Fatalf("unexpected final messages: %#v", messages)
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawTool, sawFinal bool
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatalf("event sequence gap: index %d, seq %d", i, event.Seq)
		}
		sawTool = sawTool || event.Type == "tool.completed"
		sawFinal = sawFinal || event.Type == "task.completed"
	}
	if !sawTool || !sawFinal {
		t.Fatalf("missing tool/final events: %#v", events)
	}
	steps, err := store.Steps(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != "completed" || steps[0].Attempts != 1 {
		t.Fatalf("unexpected durable step state: %#v", steps)
	}
	if len(model.seen) != 4 || !strings.Contains(model.seen[0].Messages[1].Content, "count three words here") || !strings.Contains(model.seen[0].Messages[1].Content, "Keep the final answer concise.") {
		t.Fatalf("planner context did not preserve the original goal and follow-up: %#v", model.seen)
	}
}

func TestWorkerRejectsUnknownToolWithoutHostExecution(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/denied.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "denied-task", Title: "No shell", Description: "do not run shell"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := store.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir() + "/host-executed"
	model := &scriptedLLM{responses: []string{
		`{"schema_version":1,"goal":"inspect","subtasks":[{"id":"step_1","kind":"analysis","description":"inspect","dependencies":[],"expected_output":"result","success_criteria":[]}],"max_steps":1,"final_deliverable":"answer"}`,
		fmt.Sprintf(`{"status":"continue","summary":"Trying an unavailable command.","tool_call":{"name":"run_command_sandbox","arguments":{"command":"touch %s"}}}`, marker),
	}}
	worker := New(store, model, tools.NewRegistry(), Options{MaxSteps: 12, TaskTimeout: time.Minute, MaxOutputTokens: 512})
	worker.runTask(ctx, claimed)
	failed, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != domain.StatusFailed {
		t.Fatalf("task status = %q, want failed", failed.Status)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unregistered command created a host file, stat err=%v", err)
	}
	if _, err := store.GetTask(ctx, "unrelated"); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("unexpected storage error: %v", err)
	}
}

func TestWorkerRejectsInputThatExceedsContextBudget(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/budget.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "budget-task", Title: "Too long", Description: "This request is longer than the configured context allowance."}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := store.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedLLM{}
	worker := New(store, model, tools.NewRegistry(), Options{MaxSteps: 12, MaxInputBytes: 12, TaskTimeout: time.Minute})
	worker.runTask(ctx, claimed)
	failed, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != domain.StatusFailed || model.requests != 0 {
		t.Fatalf("over-budget task status=%q model requests=%d", failed.Status, model.requests)
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawBudgetError bool
	for _, event := range events {
		if event.Type == "task.failed" && strings.Contains(string(event.Payload), "context_budget_exceeded") {
			sawBudgetError = true
		}
	}
	if !sawBudgetError {
		t.Fatalf("missing context budget failure event: %#v", events)
	}
}
