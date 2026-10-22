package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestPhase3PinnedOperationRecovery(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			f.submit(t, "Preserve every constraint", nil)
			if _, err := f.store.SaveCheckpoint(ctx, f.task.ID, 1, checkpointState{CompletedSteps: []string{}, ToolResults: map[string]any{}}); err != nil {
				t.Fatal(err)
			}
			w := New(f.store, &researchRunModel{}, tools.NewRegistry(), Options{})
			ctx, snapshot, _, err := w.roleContext(ctx, f.task.ID, "planner_initial", f.task.Description)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"context_snapshot": snapshot, "current_date": "2026-10-22"})
			user, err := w.pinRoleInput(ctx, f.task.ID, string(raw))
			if err != nil {
				t.Fatal(err)
			}
			request := invocationFixtureRequest()
			request.Messages[1].Content = user
			hash, _ := modelInvocationHash(request)
			first, cached, err := prepareInvocation(ctx, f.store, f.task.ID, "llm.complete", "model", "", hash)
			if err != nil || cached {
				t.Fatalf("first: %+v %v", first, err)
			}
			model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) { return `{"ok":true}`, nil }}
			if completed {
				if err := finishInvocation(ctx, f.store, first, llm.ChatResponse{Content: `{"ok":true}`}, nil); err != nil {
					t.Fatal(err)
				}
			}
			f.reopen(t)
			w = New(f.store, model, tools.NewRegistry(), Options{})
			restoredCtx, _, pinned, err := w.roleContext(context.Background(), f.task.ID, "planner_initial", f.task.Description)
			if err != nil || string(pinned) != user {
				t.Fatalf("restored input: %s %v", pinned, err)
			}
			before := researchRunSession(t, f.store, f.task.ID).Counters
			if completed {
				_, err = newInvocationClient(f.store, model, f.task.ID).Complete(restoredCtx, request)
				if err != nil || len(model.calls()) != 0 || researchRunSession(t, f.store, f.task.ID).Counters != before {
					t.Fatalf("completed replay: %v", err)
				}
			} else {
				second, cached, err := prepareInvocation(restoredCtx, f.store, f.task.ID, "llm.complete", "model", "", hash)
				if err != nil || cached || second.OperationID != first.OperationID || second.Attempt != 2 {
					t.Fatalf("second: %+v %v", second, err)
				}
				f.reopen(t)
				if _, _, err := prepareInvocation(restoredCtx, f.store, f.task.ID, "llm.complete", "model", "", hash); !errors.Is(err, ErrRecoveryRequired) {
					t.Fatalf("third attempt: %v", err)
				}
			}
			request.JSONSchema = json.RawMessage(`{"type":"object"}`)
			changed, _ := modelInvocationHash(request)
			if _, _, err := prepareInvocation(restoredCtx, f.store, f.task.ID, "llm.complete", "model", "", changed); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("changed request escaped identity: %v", err)
			}
			if _, _, err := prepareInvocation(roleCallSlot(restoredCtx, "format_repair"), f.store, f.task.ID, "llm.complete", "model", "", changed); err != nil {
				t.Fatalf("repair slot: %v", err)
			}
		})
	}
}

func TestPhase3LegacyInvocationCannotAcquireFreshRoleIdentity(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Resume an older execution", nil)
	request := invocationFixtureRequest()
	hash, err := modelInvocationHash(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareInvocation(ctx, f.store, f.task.ID, "llm.complete", "model", "", hash); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	model := &researchRunModel{}
	w := New(f.store, model, tools.NewRegistry(), Options{})
	if _, err := w.createPlan(ctx, f.task, f.task.Description, ""); !errors.Is(err, ErrRecoveryRequired) || len(model.calls()) != 0 {
		t.Fatalf("legacy invocation replay: %v calls=%d", err, len(model.calls()))
	}
	if _, err := f.store.ContextSnapshot(ctx, f.task.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("created a fresh snapshot: %v", err)
	}
}

func TestPhase3GoalKeepsAllUpdatesAndRejectsOverflow(t *testing.T) {
	messages := []domain.Message{}
	for i := 0; i < 6; i++ {
		messages = append(messages, domain.Message{Role: "user", Content: fmt.Sprintf("constraint_%d", i)})
	}
	goal := composeGoal("original", messages)
	for i := 0; i < 6; i++ {
		if !strings.Contains(goal, fmt.Sprintf("constraint_%d", i)) {
			t.Fatal("lost historical constraint")
		}
	}
	if strings.Index(goal, "constraint_0") > strings.Index(goal, "constraint_5") {
		t.Fatal("updates reversed")
	}
	f := newResearchRunStore(t)
	f.submit(t, strings.Repeat("x", (32<<10)+1), nil)
	model := &researchRunModel{}
	w := New(f.store, model, tools.NewRegistry(), Options{})
	if err := w.execute(context.Background(), f.task); !errors.Is(err, errInputBudgetExceeded) || len(model.calls()) != 0 {
		t.Fatalf("overflow: %v calls=%d", err, len(model.calls()))
	}
}

func TestPhase3ChatPinSurvivesResourcePause(t *testing.T) {
	f := newResearchRunStore(t)
	f.submit(t, "Hello", nil)
	opts, monitor, _ := runtimeGuardOptions()
	model, started, _ := runtimeGuardBlockingModel("Retained partial answer")
	w := New(f.store, model, tools.NewRegistry(), opts)
	done := runtimeGuardStart(t, w, f.task)
	runtimeGuardAwaitStream(t, started)
	chatTestPoll(t, "partial", func() bool { return chatTestMessage(t, f.store, f.turn).Content != "" })
	before, err := f.store.LatestCheckpoint(context.Background(), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	monitor.setLowRAM(true)
	runtimeGuardWait(t, done)
	monitor.setLowRAM(false)
	runtimeGuardMakeProbeDue(t, f)
	w.probeResourcePauses(context.Background())
	f.claim(t)
	run, err := f.store.GetRun(context.Background(), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := f.store.ConversationMessages(context.Background(), run.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	constraints, err := chatUserConstraints(run, messages)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, pinned, err := w.roleContextWithConstraints(context.Background(), f.task.ID, "chat", f.task.Description, constraints)
	if err != nil {
		t.Fatal(err)
	}
	if pinned == nil {
		var priorPins map[string]json.RawMessage
		raw, _ := json.Marshal(before.State.(map[string]any)["role_inputs"])
		_ = json.Unmarshal(raw, &priorPins)
		keys := []string{}
		for key := range priorPins {
			keys = append(keys, key)
		}
		t.Fatalf("chat pin changed across pause: pin=%+v prior_slots=%v", ctx.Value(rolePinContextKey{}), keys)
	}
}

func TestPhase3RoleBuildersSendPinnedSnapshot(t *testing.T) {
	for _, role := range []string{"planner_initial", "actor", "verifier", "finalizer_initial", "finalizer_correction", "critic_0"} {
		t.Run(role, func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			if _, err := f.store.CreateTask(ctx, domain.Task{ID: "role-builder", Title: "Count words", Description: "Count the words"}, ""); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 6; i++ {
				if _, err := f.store.AddMessage(ctx, domain.Message{ID: fmt.Sprintf("update_%d", i), TaskID: "role-builder", Role: "user", Content: fmt.Sprintf("retained_constraint_%d", i)}, ""); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.QueueTask(ctx, "role-builder", ""); err != nil {
				t.Fatal(err)
			}
			var err error
			f.task, _, err = f.store.ClaimNext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			state := checkpointState{CompletedSteps: []string{"step_1"}, ToolResults: map[string]any{"step_1": map[string]any{"words": 3, "characters": 15, "lines": 1}}}
			if _, err := f.store.SaveCheckpoint(ctx, f.task.ID, 1, state); err != nil {
				t.Fatal(err)
			}
			stop := errors.New("captured role call")
			model := &scriptedLLM{respond: func(llm.ChatRequest, int) (string, error) { return "", stop }}
			w := New(f.store, model, tools.NewRegistry(), Options{MaxInputBytes: 128 << 10, MaxOutputTokens: 1024})
			step := domain.PlanStep{ID: "step_1", Kind: "analysis", Description: "Count words", SuccessCriteria: []string{"Word count recorded"}}
			plan := domain.Plan{Subtasks: []domain.PlanStep{step}}
			call := func(memory string) error {
				switch role {
				case "planner_initial":
					_, err := w.createPlan(ctx, f.task, "Count the words", memory)
					return err
				case "actor":
					_, err := w.chooseAction(ctx, f.task, "Count the words", memory, step, "analyst", "text.inspect", "read", state, nil)
					return err
				case "verifier":
					_, err := w.verify(ctx, f.task, "Count the words", memory, plan, state)
					return err
				case "critic_0":
					id := "12345678-1234-1234-1234-123456789abc"
					_, err := w.reviewCitationClaims(ctx, f.task.ID, "A supported fact ["+id+"]", []citationEvidence{{EvidenceID: id, Excerpt: "A supported fact from the source."}})
					return err
				default:
					correction := ""
					if role == "finalizer_correction" {
						correction = "Correct citation mapping"
					}
					_, err := w.synthesize(ctx, f.task, "Count the words", memory, state, "ready", nil, correction, "")
					return err
				}
			}
			if err := call("original memory"); !errors.Is(err, stop) {
				t.Fatalf("role failed before model: %v", err)
			}
			if len(model.seen) != 1 {
				t.Fatalf("calls=%d", len(model.seen))
			}
			var input map[string]json.RawMessage
			if err := json.Unmarshal([]byte(model.seen[0].Messages[1].Content), &input); err != nil {
				t.Fatal(err)
			}
			var snapshot domain.ContextSnapshot
			if err := json.Unmarshal(input["context_snapshot"], &snapshot); err != nil || snapshot.Hash == "" || snapshot.Goal == "" {
				t.Fatalf("snapshot=%+v error=%v", snapshot, err)
			}
			if !strings.Contains(string(input["context_snapshot"]), "retained_constraint_0") {
				t.Fatal("oldest constraint absent")
			}
			first := model.seen[0].Messages[1].Content
			state.ToolResults = map[string]any{"oversized": strings.Repeat("x", 256<<10)}
			if err := call("changed memory"); !errors.Is(err, stop) {
				t.Fatalf("pinned call rebuilt optional input: %v", err)
			}
			if len(model.seen) != 2 || model.seen[1].Messages[1].Content != first {
				t.Fatal("role input changed after pin")
			}
		})
	}
}

func TestPhase3FormatRepairUsesSeparateInvocationSlot(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Return a schema object", nil)
	if _, err := f.store.SaveCheckpoint(ctx, f.task.ID, 1, checkpointState{CompletedSteps: []string{}}); err != nil {
		t.Fatal(err)
	}
	model := &researchRunModel{respond: func(ctx context.Context, _ llm.ChatRequest, index int, _ bool) (string, error) {
		if _, ok := ctx.Value(rolePinContextKey{}).(pinnedRole); !ok {
			t.Fatal("role identity missing")
		}
		repair, _ := ctx.Value(roleRepairContextKey{}).(string)
		if index == 0 {
			if repair != "" {
				t.Fatal("initial call uses repair identity")
			}
			return "PRIVATE_INVALID_MODEL_TEXT", nil
		}
		if repair != "format_repair" {
			t.Fatal("repair call reused original identity")
		}
		return `{"ok":true}`, nil
	}}
	w := New(f.store, newInvocationClient(f.store, model, f.task.ID), tools.NewRegistry(), Options{MaxInputBytes: 128 << 10})
	ctx, snapshot, _, err := w.roleContext(ctx, f.task.ID, "planner_initial", f.task.Description)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"context_snapshot": snapshot, "goal": f.task.Description})
	user, err := w.pinRoleInput(ctx, f.task.ID, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := w.completeJSONWithRepair(ctx, "Return a JSON object without reasoning", user, invocationFixtureRequest().JSONSchema, &result); err != nil || !result.OK {
		t.Fatalf("repair: %+v %v", result, err)
	}
	if len(model.calls()) != 2 {
		t.Fatalf("calls=%d", len(model.calls()))
	}
	checkpoint, err := f.store.LatestCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(checkpoint.State)
	if strings.Contains(string(encoded), "PRIVATE_INVALID_MODEL_TEXT") {
		t.Fatal("invalid model text persisted in checkpoint")
	}
	events, err := f.store.EventsAfter(ctx, f.task.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "PRIVATE_INVALID_MODEL_TEXT") {
			t.Fatal("invalid model text persisted in event")
		}
	}
}

func TestPhase3ChatIncludesOneSnapshotAndCurrentRequest(t *testing.T) {
	h := newChatTestHarness(t, Options{})
	c := h.conversation(t, "chat")
	turn := h.submit(t, domain.SubmitTurn{ConversationID: c.ID, Content: "Current request stays primary"})
	call := h.call(t)
	encoded, _ := json.Marshal(call.request.Messages)
	if strings.Count(string(encoded), `\"budget_remaining\"`) != 1 {
		t.Fatalf("snapshot absent or duplicated: %s", encoded)
	}
	if call.request.Messages[len(call.request.Messages)-1].Content != turn.UserMessage.Content || !strings.Contains(call.request.Messages[0].Content, "untrusted historical") {
		t.Fatal("snapshot displaced current request or gained authority")
	}
	chatTestSend(t, call, llm.ChatEvent{Text: "Done"})
	close(call.events)
	h.terminal(t, turn.Run.ID, domain.StatusCompleted, "")
	h.noTools(t, turn.Run.ID)
}

func TestPhase3CorruptRolePayloadFailsClosed(t *testing.T) {
	f := newResearchRunStore(t)
	f.submit(t, "Keep a durable snapshot", nil)
	ctx := context.Background()
	if _, err := f.store.SaveCheckpoint(ctx, f.task.ID, 1, checkpointState{CompletedSteps: []string{}}); err != nil {
		t.Fatal(err)
	}
	model := &researchRunModel{}
	w := New(f.store, model, tools.NewRegistry(), Options{})
	ctx, _, _, err := w.roleContext(ctx, f.task.ID, "planner_initial", f.task.Description)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.pinRoleInput(ctx, f.task.ID, `{"unexpected":"missing snapshot"}`); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	w = New(f.store, model, tools.NewRegistry(), Options{})
	if _, err := w.createPlan(context.Background(), f.task, f.task.Description, ""); !errors.Is(err, sqlite.ErrCheckpointInvalid) || len(model.calls()) != 0 {
		t.Fatalf("corrupt role payload accepted: %v", err)
	}
}

func TestPhase3ChatConstraintsFollowOnlyActiveBranch(t *testing.T) {
	constraints, err := chatUserConstraints(domain.Run{ConversationID: "c", UserMessageID: "current"}, []domain.ConversationMessage{
		{ID: "old", ConversationID: "c", Role: "user", Content: "Keep old constraint"},
		{ID: "answer", ConversationID: "c", ParentMessageID: "old", Role: "assistant", Content: "Historical answer"},
		{ID: "sibling", ConversationID: "c", ParentMessageID: "answer", Role: "user", Content: "Sibling constraint excluded"},
		{ID: "current", ConversationID: "c", ParentMessageID: "answer", Role: "user", Content: "Current request"},
	})
	if err != nil || len(constraints) != 2 || constraints[0] != "Keep old constraint" || constraints[1] != "Current request" {
		t.Fatalf("branch constraints: %v %v", constraints, err)
	}
}

func TestPhase3CompactionPinsProtectedStateAndEmitsOnce(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Inspect text and retain every constraint", nil)
	evidence, err := f.store.SaveEvidence(ctx, domain.Evidence{TaskID: f.task.ID, Provider: "http", OriginalURL: "https://example.org/source", CanonicalURL: "https://example.org/source", ContentSHA256: strings.Repeat("a", 64), HTTPStatus: 200, ExtractionMethod: "text", Excerpt: "An exact source excerpt"})
	if err != nil {
		t.Fatal(err)
	}
	state := checkpointState{CurrentStep: "inspect", PendingTool: "text.inspect", PendingApproval: true,
		PendingArguments: json.RawMessage(`{"text":"original input"}`), CompletedSteps: []string{},
		ToolResults: map[string]any{"previous": map[string]any{"text": strings.Repeat("large external text ", 4000), "raw_artifact_id": "original-artifact", "evidence_id": evidence.ID}},
	}
	plan := domain.Plan{SchemaVersion: 1, Goal: f.task.Description, MaxSteps: 1, FinalDeliverable: "Faithful text result", Subtasks: []domain.PlanStep{{ID: "inspect", Kind: "analysis", Description: "Inspect text", SuccessCriteria: []string{"Count is recorded"}}}}
	if _, err := f.store.SavePlan(ctx, f.task.ID, plan, state); err != nil {
		t.Fatal(err)
	}
	initial, err := f.store.SaveContextSnapshot(ctx, f.task.ID, domain.ContextSnapshot{
		Constraints: []string{"Never drop this"}, Contradictions: []string{"Two incompatible observations"}, OpenQuestions: []string{"Resolve discrepancy"},
		AcceptedEvidenceIDs: []string{evidence.ID},
	}, "initial")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedLLM{respond: func(llm.ChatRequest, int) (string, error) {
		return `{"status":"continue","summary":"Inspect","tool_call":{"name":"text.inspect","arguments":{"text":"test"}}}`, nil
	}}
	registry := tools.NewRegistry()
	w := New(f.store, model, registry, Options{MaxInputBytes: 12 << 10})
	call := func() error {
		_, err := w.chooseAction(ctx, f.task, f.task.Description, "", plan.Subtasks[0], "analyst", "text.inspect", "read", state, nil)
		return err
	}
	if err := call(); err != nil {
		t.Fatal(err)
	}
	var input struct {
		Snapshot domain.ContextSnapshot `json:"context_snapshot"`
		Results  map[string]any         `json:"prior_tool_results_untrusted"`
	}
	if err := json.Unmarshal([]byte(model.seen[0].Messages[1].Content), &input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input.Snapshot.AcceptedEvidenceIDs, initial.AcceptedEvidenceIDs) || !reflect.DeepEqual(input.Snapshot.Constraints, initial.Constraints) || !reflect.DeepEqual(input.Snapshot.Contradictions, initial.Contradictions) || !reflect.DeepEqual(input.Snapshot.OpenQuestions, initial.OpenQuestions) || !reflect.DeepEqual(input.Snapshot.BudgetRemaining, initial.BudgetRemaining) {
		t.Fatalf("protected snapshot changed: %+v", input.Snapshot)
	}
	if !reflect.DeepEqual(input.Snapshot.DoneCriteria, []string{"Faithful text result", "Count is recorded"}) || input.Snapshot.NextAction != "actor:inspect:text.inspect" {
		t.Fatalf("criteria/pending action lost: %+v", input.Snapshot)
	}
	retained, _ := json.Marshal(input.Results)
	if len(retained) > 12<<10 || !strings.Contains(string(retained), "original-artifact") {
		t.Fatalf("bad compacted references: %s", retained)
	}
	f.reopen(t)
	w = New(f.store, model, registry, Options{MaxInputBytes: 12 << 10})
	if err := call(); err != nil {
		t.Fatal(err)
	}
	events, err := f.store.EventsAfter(ctx, f.task.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type != "context.compacted" {
			continue
		}
		count++
		var payload struct {
			Before int `json:"original_tool_result_bytes"`
			After  int `json:"retained_tool_result_bytes"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Before <= payload.After || payload.After != len(retained) {
			t.Fatalf("false compaction: %s %v", event.Payload, err)
		}
		if strings.Contains(string(event.Payload), "large external text") {
			t.Fatal("event leaked content")
		}
	}
	if count != 1 {
		t.Fatalf("compaction events=%d", count)
	}
	restored, err := w.currentCheckpoint(ctx, f.task.ID)
	if err != nil || restored.PendingTool != state.PendingTool || !restored.PendingApproval || string(restored.PendingArguments) != string(state.PendingArguments) {
		t.Fatalf("pending state lost: %+v %v", restored, err)
	}
}
