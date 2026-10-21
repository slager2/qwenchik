package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type chatTestCall struct {
	ctx     context.Context
	request llm.ChatRequest
	events  chan llm.ChatEvent
}

type chatTestLLM struct {
	t         *testing.T
	calls     chan chatTestCall
	streams   atomic.Int32
	active    atomic.Int32
	overlap   atomic.Bool
	panicNext atomic.Bool
}

func (m *chatTestLLM) Complete(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	m.t.Error("chat called Complete; only plain-text Stream is allowed")
	return llm.ChatResponse{}, errors.New("unexpected Complete")
}

func (m *chatTestLLM) Stream(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	m.streams.Add(1)
	if m.panicNext.Swap(false) {
		panic("chat test model panic")
	}
	if m.active.Add(1) != 1 {
		m.overlap.Store(true)
	}
	input := make(chan llm.ChatEvent)
	output := make(chan llm.ChatEvent)
	go func() {
		defer close(output)
		defer m.active.Add(-1)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-input:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case output <- event:
				}
			}
		}
	}()
	select {
	case m.calls <- chatTestCall{ctx: ctx, request: request, events: input}:
	case <-ctx.Done():
	}
	return output, nil
}

type chatTestHarness struct {
	store  *sqlite.Store
	path   string
	model  *chatTestLLM
	worker *Worker
}

func newChatTestHarness(t *testing.T, options Options) *chatTestHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.db")
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	model := &chatTestLLM{t: t, calls: make(chan chatTestCall, 8)}
	options.PollInterval = 5 * time.Millisecond
	if options.TaskTimeout == 0 {
		options.TaskTimeout = 30 * time.Second
	}
	worker := New(store, model, &tools.Registry{}, options)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	return &chatTestHarness{store: store, path: path, model: model, worker: worker}
}

func (h *chatTestHarness) conversation(t *testing.T, mode string) domain.Conversation {
	t.Helper()
	c, err := h.store.CreateConversation(context.Background(), domain.Conversation{Title: "Chat test", Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (h *chatTestHarness) submit(t *testing.T, in domain.SubmitTurn) domain.SubmitResponse {
	t.Helper()
	r, err := h.store.SubmitTurn(context.Background(), in, "")
	if err != nil {
		t.Fatal(err)
	}
	h.worker.Wake()
	return r
}

func (h *chatTestHarness) call(t *testing.T) chatTestCall {
	t.Helper()
	select {
	case call := <-h.model.calls:
		if call.request.JSONMode || len(call.request.JSONSchema) != 0 {
			t.Fatalf("chat stream requested structured output: %#v", call.request)
		}
		if len(call.request.Messages) < 2 || call.request.Messages[0].Role != "system" {
			t.Fatalf("missing system/user context: %#v", call.request.Messages)
		}
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not call Stream")
		return chatTestCall{}
	}
}

func chatTestSend(t *testing.T, call chatTestCall, event llm.ChatEvent) {
	t.Helper()
	select {
	case call.events <- event:
	case <-call.ctx.Done():
		t.Fatalf("stream ended before event: %v", call.ctx.Err())
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not consume stream event")
	}
}

func chatTestPoll(t *testing.T, description string, check func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-ticker.C:
		}
	}
}

func (h *chatTestHarness) terminal(t *testing.T, id string, status domain.TaskStatus, code string) domain.Run {
	t.Helper()
	var run domain.Run
	chatTestPoll(t, "terminal run "+id, func() bool {
		var err error
		run, err = h.store.GetRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return run.Status.Terminal()
	})
	if run.Status != status || run.ErrorCode != code || run.CompletedAt == nil {
		t.Fatalf("run = %#v, want status %s code %q and completed timestamp", run, status, code)
	}
	return run
}

func chatTestMessage(t *testing.T, store *sqlite.Store, turn domain.SubmitResponse) domain.ConversationMessage {
	t.Helper()
	messages, err := store.ConversationMessages(context.Background(), turn.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == turn.AssistantMessage.ID {
			return message
		}
	}
	t.Fatalf("assistant %s missing from %#v", turn.AssistantMessage.ID, messages)
	return domain.ConversationMessage{}
}

func (h *chatTestHarness) partial(t *testing.T, turn domain.SubmitResponse, text string) {
	t.Helper()
	chatTestPoll(t, "persisted assistant delta", func() bool {
		return chatTestMessage(t, h.store, turn).Content == text
	})
}

func (h *chatTestHarness) reload(t *testing.T, turn domain.SubmitResponse, text, status string) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), h.path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	message := chatTestMessage(t, store, turn)
	if message.Content != text || message.Status != status || message.Role != "assistant" || message.LastEventSeq == 0 {
		t.Fatalf("reloaded assistant = %#v, want %q / %s", message, text, status)
	}
}

func chatTestContext(t *testing.T, call chatTestCall, want ...llm.ChatMessage) {
	t.Helper()
	if !reflect.DeepEqual(call.request.Messages[1:], want) {
		t.Fatalf("chat context = %#v, want %#v", call.request.Messages[1:], want)
	}
}

func (h *chatTestHarness) noTools(t *testing.T, id string) {
	t.Helper()
	events, err := h.store.EventsAfter(context.Background(), id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	started, delta := false, false
	for _, event := range events {
		if strings.HasPrefix(event.Type, "tool.") || strings.HasPrefix(event.Type, "plan.") || strings.HasPrefix(event.Type, "search.") {
			t.Errorf("direct chat emitted %s", event.Type)
		}
		started = started || event.Type == "chat.started"
		delta = delta || event.Type == "message.delta"
	}
	if !started || !delta {
		t.Fatalf("missing chat.started/message.delta: %#v", events)
	}
	steps, err := h.store.Steps(context.Background(), id)
	if err != nil || len(steps) != 0 {
		t.Fatalf("chat created plan steps: %#v, %v", steps, err)
	}
}

func TestChatWorkerMultiTurnDirectWithoutTools(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	c := h.conversation(t, "chat")
	first := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Remember the word amber."})
	call := h.call(t)
	chatTestContext(t, call, llm.ChatMessage{Role: "user", Content: first.UserMessage.Content})
	chatTestSend(t, call, llm.ChatEvent{Text: "I remember "})
	chatTestSend(t, call, llm.ChatEvent{Text: "amber."})
	close(call.events)
	h.terminal(t, first.Run.ID, domain.StatusCompleted, "")
	h.reload(t, first, "I remember amber.", "completed")
	second := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Which word?"})
	call = h.call(t)
	chatTestContext(t, call,
		llm.ChatMessage{Role: "user", Content: first.UserMessage.Content},
		llm.ChatMessage{Role: "assistant", Content: "I remember amber."},
		llm.ChatMessage{Role: "user", Content: "Which word?"})
	chatTestSend(t, call, llm.ChatEvent{Text: "amber"})
	close(call.events)
	h.terminal(t, second.Run.ID, domain.StatusCompleted, "")
	h.reload(t, second, "amber", "completed")
	h.noTools(t, first.Run.ID)
	h.noTools(t, second.Run.ID)
	if h.model.streams.Load() != 2 {
		t.Fatalf("Stream calls = %d, want 2", h.model.streams.Load())
	}
}

func TestChatWorkerStopRetainsPersistedDelta(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	c := h.conversation(t, "chat")
	turn := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Write a long story."})
	call := h.call(t)
	chatTestSend(t, call, llm.ChatEvent{Text: "The story begins here."})
	h.partial(t, turn, "The story begins here.")
	if _, err := h.store.TransitionTask(context.Background(), turn.Run.ID, domain.StatusCancelled, nil, "task.cancelled", "user", nil); err != nil {
		t.Fatal(err)
	}
	h.worker.Interrupt(turn.Run.ID)
	select {
	case <-call.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel model context")
	}
	chatTestPoll(t, "stopped worker", func() bool { return !h.worker.Busy() })
	run := h.terminal(t, turn.Run.ID, domain.StatusCancelled, "")
	if !run.CancelRequested {
		t.Fatal("cancel request was not persisted")
	}
	h.reload(t, turn, "The story begins here.", "cancelled")
}

func TestChatWorkerStreamErrorRetainsPartialAndClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"interrupted", llm.ErrStreamInterrupted, "model_stream_interrupted"},
		{"timeout", llm.ErrModelTimeout, "model_timeout"},
		{"truncated", llm.ErrOutputTruncated, "model_output_truncated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newChatTestHarness(t, Options{})
			c := h.conversation(t, "chat")
			turn := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Explain a topic."})
			call := h.call(t)
			chatTestSend(t, call, llm.ChatEvent{Text: "A useful partial answer"})
			h.partial(t, turn, "A useful partial answer")
			chatTestSend(t, call, llm.ChatEvent{Err: tc.err})
			h.terminal(t, turn.Run.ID, domain.StatusFailed, tc.code)
			h.reload(t, turn, "A useful partial answer", "failed")
		})
	}
}

func TestChatWorkerRecoversModelPanicAndProcessesNextRun(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	h.model.panicNext.Store(true)
	c := h.conversation(t, "chat")
	turn := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Trigger the test panic."})
	h.terminal(t, turn.Run.ID, domain.StatusFailed, "internal_error")
	h.reload(t, turn, "", "failed")
	next := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Are you still here?"})
	call := h.call(t)
	chatTestSend(t, call, llm.ChatEvent{Text: "Still here."})
	close(call.events)
	h.terminal(t, next.Run.ID, domain.StatusCompleted, "")
	h.reload(t, next, "Still here.", "completed")
}

func TestChatWorkerRetryExcludesOldAssistantSibling(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	c := h.conversation(t, "chat")
	first := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Give a fresh answer."})
	call := h.call(t)
	chatTestSend(t, call, llm.ChatEvent{Text: "Discard this old sibling."})
	chatTestSend(t, call, llm.ChatEvent{Err: llm.ErrStreamInterrupted})
	h.terminal(t, first.Run.ID, domain.StatusFailed, "model_stream_interrupted")
	retry := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Operation: "retry", SourceRunID: first.Run.ID})
	if retry.Run.ID == first.Run.ID || retry.UserMessage.ID != first.UserMessage.ID || retry.AssistantMessage.ID == first.AssistantMessage.ID {
		t.Fatalf("retry did not create a new assistant run: %#v", retry)
	}
	call = h.call(t)
	chatTestContext(t, call, llm.ChatMessage{Role: "user", Content: first.UserMessage.Content})
	chatTestSend(t, call, llm.ChatEvent{Text: "Fresh answer."})
	close(call.events)
	h.terminal(t, retry.Run.ID, domain.StatusCompleted, "")
	h.reload(t, first, "Discard this old sibling.", "failed")
	h.reload(t, retry, "Fresh answer.", "completed")
}

func TestChatWorkerContinueIncludesOldSegmentAndCurrentUser(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	c := h.conversation(t, "chat")
	first := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Remember blue."})
	call := h.call(t)
	chatTestSend(t, call, llm.ChatEvent{Text: "Blue remembered."})
	close(call.events)
	h.terminal(t, first.Run.ID, domain.StatusCompleted, "")
	current := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Now write a two-part story."})
	call = h.call(t)
	chatTestSend(t, call, llm.ChatEvent{Text: "Part one: a blue door."})
	chatTestSend(t, call, llm.ChatEvent{Err: llm.ErrOutputTruncated})
	h.terminal(t, current.Run.ID, domain.StatusFailed, "model_output_truncated")
	continued := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Operation: "continue", SourceRunID: current.Run.ID})
	if continued.Run.UserMessageID != current.UserMessage.ID || continued.Run.ContinueFromMessageID != current.AssistantMessage.ID {
		t.Fatalf("incorrect continuation branch: %#v", continued)
	}
	call = h.call(t)
	messages := call.request.Messages[1:]
	if len(messages) != 5 {
		t.Fatalf("continuation context = %#v, want four historical messages and continuation instruction", messages)
	}
	want := []llm.ChatMessage{
		{Role: "user", Content: first.UserMessage.Content},
		{Role: "assistant", Content: "Blue remembered."},
		{Role: "user", Content: current.UserMessage.Content},
		{Role: "assistant", Content: "Part one: a blue door."},
	}
	if !reflect.DeepEqual(messages[:4], want) || messages[4].Role != "user" || !strings.Contains(strings.ToLower(messages[4].Content), "continue") {
		t.Fatalf("incorrect continuation context: %#v", messages)
	}
	chatTestSend(t, call, llm.ChatEvent{Text: "Part two: it opens."})
	close(call.events)
	h.terminal(t, continued.Run.ID, domain.StatusCompleted, "")
	h.reload(t, current, "Part one: a blue door.", "failed")
	h.reload(t, continued, "Part two: it opens.", "completed")
}

func TestChatWorkerHonorsAbsoluteSubmitDeadline(t *testing.T) {
	h := newChatTestHarness(t, Options{TaskTimeout: time.Minute})
	c := h.conversation(t, "chat")
	turn := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Block until the deadline.", DeadlineSeconds: 1})
	if turn.Run.DeadlineAt == nil {
		t.Fatal("SubmitTurn did not persist a deadline")
	}
	call := h.call(t)
	deadline, ok := call.ctx.Deadline()
	if !ok || !deadline.Equal(*turn.Run.DeadlineAt) {
		t.Fatalf("model deadline = %v (%t), want persisted %v", deadline, ok, turn.Run.DeadlineAt)
	}
	run := h.terminal(t, turn.Run.ID, domain.StatusFailed, "run_deadline")
	if !run.DeadlineAt.Equal(*turn.Run.DeadlineAt) || !errors.Is(call.ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("absolute deadline changed or model not expired: %#v, %v", run, call.ctx.Err())
	}
}

func TestChatWorkerRejectsOversizedLatestInputBeforeModel(t *testing.T) {
	h := newChatTestHarness(t, Options{MaxInputBytes: 4096})
	c := h.conversation(t, "chat")
	turn := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: strings.Repeat("large latest input ", 300)})
	h.terminal(t, turn.Run.ID, domain.StatusFailed, "context_budget_exceeded")
	if h.model.streams.Load() != 0 {
		t.Fatalf("oversized input reached model: %d calls", h.model.streams.Load())
	}
	h.reload(t, turn, "", "failed")
}

func TestChatWorkerResearchGreetingBypassesSearch(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	c := h.conversation(t, "research")
	turn := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Привет!"})
	call := h.call(t)
	chatTestContext(t, call, llm.ChatMessage{Role: "user", Content: "Привет!"})
	chatTestSend(t, call, llm.ChatEvent{Text: "Здравствуйте!"})
	close(call.events)
	h.terminal(t, turn.Run.ID, domain.StatusCompleted, "")
	h.reload(t, turn, "Здравствуйте!", "completed")
	h.noTools(t, turn.Run.ID)
}

func TestChatWorkerSerializesQueuedConversations(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	firstConversation := h.conversation(t, "chat")
	secondConversation := h.conversation(t, "chat")
	first := h.submit(t, domain.SubmitTurn{ConversationID: firstConversation.ID, Content: "First conversation."})
	firstCall := h.call(t)
	second := h.submit(t, domain.SubmitTurn{ConversationID: secondConversation.ID, Content: "Second conversation."})
	chatTestSend(t, firstCall, llm.ChatEvent{Text: "First partial."})
	h.partial(t, first, "First partial.")
	queued, err := h.store.GetRun(context.Background(), second.Run.ID)
	if err != nil || queued.Status != domain.StatusQueued || h.model.streams.Load() != 1 {
		t.Fatalf("second run started while first was streaming: %#v, %v, calls=%d", queued, err, h.model.streams.Load())
	}
	close(firstCall.events)
	secondCall := h.call(t)
	firstRun := h.terminal(t, first.Run.ID, domain.StatusCompleted, "")
	chatTestContext(t, secondCall, llm.ChatMessage{Role: "user", Content: second.UserMessage.Content})
	chatTestSend(t, secondCall, llm.ChatEvent{Text: "Second answer."})
	close(secondCall.events)
	secondRun := h.terminal(t, second.Run.ID, domain.StatusCompleted, "")
	if h.model.overlap.Load() || h.model.streams.Load() != 2 || secondRun.StartedAt == nil || secondRun.StartedAt.Before(*firstRun.CompletedAt) {
		t.Fatalf("inference was not sequential: first=%#v second=%#v overlap=%t calls=%d", firstRun, secondRun, h.model.overlap.Load(), h.model.streams.Load())
	}
	h.reload(t, first, "First partial.", "completed")
	h.reload(t, second, "Second answer.", "completed")
}
