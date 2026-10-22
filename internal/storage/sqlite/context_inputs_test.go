package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func roleSlot(name string) string { return roleInputHash([]byte(name)) }

func TestCompactedRolePinRollsBackWithEventOrCheckpointFailure(t *testing.T) {
	for _, boundary := range []string{"event", "checkpoint"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			id, slot := turn.Run.ID, roleSlot("compacted")
			if _, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{DoneCriteria: []string{"Preserved"}}, "initial"); err != nil {
				t.Fatal(err)
			}
			before, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER reject_compaction BEFORE INSERT ON events WHEN NEW.type='context.compacted' BEGIN SELECT RAISE(ABORT,'injected'); END`
			if boundary == "checkpoint" {
				trigger = `CREATE TRIGGER reject_compaction BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'injected'); END`
			}
			if _, err := s.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PinCompactedRoleContext(ctx, id, slot, json.RawMessage(`{"text":"bounded"}`), 10000, 200); err == nil {
				t.Fatal("injected failure ignored")
			}
			if _, err := s.RoleContext(ctx, id, slot); !errors.Is(err, ErrNotFound) {
				t.Fatalf("pin escaped rollback: %v", err)
			}
			after, err := s.LatestCheckpoint(ctx, id)
			if err != nil || after.ID != before.ID {
				t.Fatalf("checkpoint escaped rollback: %+v %v", after, err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type IN ('context.compacted','context.role_pinned')`, id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("event escaped rollback: %d %v", count, err)
			}
		})
	}
}

func assertRolePin(t *testing.T, s *Store, taskID, slot string, expected []byte) {
	t.Helper()
	got, err := s.RoleContext(context.Background(), taskID, slot)
	if err != nil || !bytes.Equal(got, expected) {
		t.Fatalf("role input: %s %v, want %s", got, err, expected)
	}
}

func TestRoleContextCanonicalImmutableReopen(t *testing.T) {
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	id, slot := turn.Run.ID, roleSlot("planner:1")
	input := json.RawMessage(` { "z": [null, true, {"n":9007199254740993}], "a": "user source" } `)
	want := []byte(`{"a":"user source","z":[null,true,{"n":9007199254740993}]}`)
	got, err := s.PinRoleContext(ctx, id, slot, input)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("canonical input: %s %v", got, err)
	}
	first, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []json.RawMessage{json.RawMessage(`{"date":"tomorrow","counters":999}`), json.RawMessage(`invalid`)} {
		got, err := s.PinRoleContext(ctx, id, slot, changed)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("immutable input: %s %v", got, err)
		}
	}
	latest, err := s.LatestCheckpoint(ctx, id)
	if err != nil || latest.ID != first.ID {
		t.Fatalf("repeat appended checkpoint: %#v %v", latest, err)
	}
	got[0] = 'x'
	input[0] = 'x'
	assertRolePin(t, s, id, slot, want)
	var count int
	var payload, visibility string
	if err := s.db.QueryRow(`SELECT COUNT(*),payload_json,visibility FROM events WHERE task_id=? AND type='context.role_pinned'`, id).Scan(&count, &payload, &visibility); err != nil {
		t.Fatal(err)
	}
	if count != 1 || visibility != "internal" || strings.Contains(payload, "user source") || strings.Contains(payload, "9007199254740993") || len(payload) > 512 {
		t.Fatalf("event leaked input: %s %s count=%d", visibility, payload, count)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertRolePin(t, reopened, id, slot, want)
	if _, err := reopened.RoleContext(ctx, id, roleSlot("absent")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing slot: %v", err)
	}
	if _, err := reopened.RoleContext(ctx, "foreign-task", slot); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign task: %v", err)
	}
}

func TestRoleContextStrictJSONAndLimits(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	for i, input := range []string{`[]`, `null`, `true`, `{}`, `{"x":1,"x":2}`, `{"a":[{"x":1,"\u0078":2}]}`, `{"x":1} {"y":2}`, `{"x":`, `{"x":NaN}`} {
		if input == `{}` {
			continue
		}
		if _, err := s.PinRoleContext(ctx, turn.Run.ID, roleSlot(fmt.Sprint(i)), json.RawMessage(input)); !errors.Is(err, ErrCheckpointInvalid) {
			t.Fatalf("accepted malformed input %q: %v", input, err)
		}
	}
	for _, slot := range []string{"", "planner", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := s.PinRoleContext(ctx, turn.Run.ID, slot, json.RawMessage(`{}`)); !errors.Is(err, ErrCheckpointInvalid) {
			t.Fatalf("accepted slot %q: %v", slot, err)
		}
		if _, err := s.RoleContext(ctx, turn.Run.ID, slot); !errors.Is(err, ErrCheckpointInvalid) {
			t.Fatalf("getter accepted slot %q: %v", slot, err)
		}
	}
	large := json.RawMessage(`{"x":"` + strings.Repeat("x", MaxRoleContextInputBytes) + `"}`)
	if _, err := s.PinRoleContext(ctx, turn.Run.ID, roleSlot("large"), large); !errors.Is(err, ErrContextSnapshotTooLarge) {
		t.Fatalf("oversized input: %v", err)
	}
	pins := make(map[string]pinnedRoleInput)
	for i := 0; i < MaxRoleContextPins; i++ {
		pins[roleSlot(fmt.Sprint(i))] = pinnedRoleInput{Version: 1, InputHash: roleInputHash([]byte(`{}`)), Input: json.RawMessage(`{}`)}
	}
	encoded, _ := json.Marshal(pins)
	if _, err := decodeRoleInputs(encoded); err != nil {
		t.Fatalf("max pins rejected: %v", err)
	}
	pins[roleSlot("overflow")] = pinnedRoleInput{Version: 1, InputHash: roleInputHash([]byte(`{}`)), Input: json.RawMessage(`{}`)}
	encoded, _ = json.Marshal(pins)
	if _, err := decodeRoleInputs(encoded); !errors.Is(err, ErrContextSnapshotTooLarge) {
		t.Fatalf("pin count overflow: %v", err)
	}
	input := json.RawMessage(`{"x":"` + strings.Repeat("x", MaxRoleContextInputBytes-8) + `"}`)
	for i := 0; i < 7; i++ {
		if _, err := s.PinRoleContext(ctx, turn.Run.ID, roleSlot(fmt.Sprintf("large-%d", i)), input); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinRoleContext(ctx, turn.Run.ID, roleSlot("overflow-total"), input); !errors.Is(err, ErrContextSnapshotTooLarge) {
		t.Fatalf("total overflow: %v", err)
	}
	after, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || after.ID != before.ID {
		t.Fatalf("overflow persisted: %#v %v", after, err)
	}
}

func TestRoleContextSurvivesAllCheckpointWriters(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	id, slot := turn.Run.ID, roleSlot("planner")
	snapshot, err := s.SaveContextSnapshot(ctx, id, domain.ContextSnapshot{Constraints: []string{"preserve"}}, "initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpoint(ctx, id, 1, map[string]json.RawMessage{
		"future":            json.RawMessage(`{"integer":9007199254740993}`),
		"pending_approval":  json.RawMessage(`{"id":"approval","integer":9007199254740993}`),
		"pending_arguments": json.RawMessage(`{"integer":9007199254740993}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinRoleContext(ctx, id, roleSlot("before-invocation"), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	started, err := s.BeginInvocation(ctx, invocationFixture(id, "tool.text.inspect", 1))
	if err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, roleSlot("before-invocation"), []byte(`{}`))
	input := json.RawMessage(`{"goal":"user","integer":9007199254740993}`)
	if _, err := s.PinRoleContext(ctx, id, slot, input); err != nil {
		t.Fatal(err)
	}
	cp, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	raw := mustCheckpointJSON(t, s, id, cp.ID)
	if !strings.Contains(raw, `"integer":9007199254740993`) || !strings.Contains(raw, started.ID) || !strings.Contains(raw, `"pending_approval"`) || cp.ContextSnapshotHash != snapshot.Hash {
		t.Fatalf("pin lost state: %s", raw)
	}
	fields := checkpointObject([]byte(raw))
	if string(fields["future"]) != `{"integer":9007199254740993}` || string(fields["pending_arguments"]) != `{"integer":9007199254740993}` || !bytes.Contains(fields["pending_approval"], []byte(`9007199254740993`)) {
		t.Fatalf("pin rounded prior fields: %s", raw)
	}
	done := started
	done.Status = "completed"
	if err := s.FinishInvocation(ctx, done, map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, input)
	workerState := struct {
		Summary string `json:"summary"`
	}{Summary: "worker state"}
	plan := domain.Plan{SchemaVersion: 1, Goal: "Plan", MaxSteps: 1, FinalDeliverable: "Answer", Subtasks: []domain.PlanStep{{ID: "step_1", Kind: "analysis", Description: "Analyze", SuccessCriteria: []string{"done"}}}}
	if _, err := s.SavePlan(ctx, id, plan, workerState); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, input)
	if _, err := s.SaveCheckpoint(ctx, id, 1, workerState); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, input)
	snapshot.Summary = "updated snapshot"
	snapshot, err = s.SaveContextSnapshot(ctx, id, snapshot, "refresh")
	if err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, input)
	if _, err := s.TransitionTask(ctx, id, domain.StatusRunning, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepStatus(ctx, id, "step_1", "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitStepResult(ctx, id, "step_1", "text.inspect", "done", map[string]any{"ok": true}, workerState); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, input)
	if _, err := s.TransitionTask(ctx, id, domain.StatusVerifying, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTask(ctx, id, "Answer", workerState); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, input)
	latest, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ContextSnapshotHash != snapshot.Hash {
		t.Fatal("context snapshot hash changed")
	}
	final := checkpointObject([]byte(mustCheckpointJSON(t, s, id, latest.ID)))
	if string(final["future"]) != `{"integer":9007199254740993}` {
		t.Fatal("future integer rounded")
	}
}

func TestRoleContextRejectsCheckpointOverwrite(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	id, slot := turn.Run.ID, roleSlot("planner")
	input := json.RawMessage(`{"a":1}`)
	if _, err := s.PinRoleContext(ctx, id, slot, input); err != nil {
		t.Fatal(err)
	}
	for _, state := range []any{
		map[string]any{"role_inputs": nil},
		map[string]any{"role_inputs": map[string]any{slot: map[string]any{"version": 1, "input_hash": roleInputHash([]byte(`{"a":2}`)), "input": json.RawMessage(`{"a":2}`)}}},
		map[string]any{"role_inputs": map[string]any{slot: map[string]any{"input": json.RawMessage(`{"a":2}`)}}},
		map[string]any{"role_inputs": map[string]any{roleSlot("injected"): pinnedRoleInput{Version: 1, InputHash: roleInputHash(input), Input: input}}},
	} {
		if _, err := s.SaveCheckpoint(ctx, id, 1, state); !errors.Is(err, ErrCheckpointInvalid) {
			t.Fatalf("overwrite accepted: %v", err)
		}
		assertRolePin(t, s, id, slot, input)
	}
	if _, err := s.SaveCheckpoint(ctx, id, 1, map[string]any{"role_inputs": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, input)
}

func TestRoleContextPinRollback(t *testing.T) {
	for _, boundary := range []string{"event", "checkpoint"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			id, slot := turn.Run.ID, roleSlot("retained")
			input := json.RawMessage(`{"a":1}`)
			if _, err := s.PinRoleContext(ctx, id, slot, input); err != nil {
				t.Fatal(err)
			}
			before, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER reject_role_pin BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'injected'); END`
			if boundary == "event" {
				trigger = `CREATE TRIGGER reject_role_pin BEFORE INSERT ON events WHEN NEW.type='context.role_pinned' BEGIN SELECT RAISE(ABORT,'injected'); END`
			}
			if _, err := s.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PinRoleContext(ctx, id, roleSlot("new"), input); err == nil {
				t.Fatal("failure ignored")
			}
			after, err := s.LatestCheckpoint(ctx, id)
			if err != nil || after.ID != before.ID {
				t.Fatalf("old checkpoint lost: %#v %v", after, err)
			}
			if _, err := s.RoleContext(ctx, id, roleSlot("new")); !errors.Is(err, ErrNotFound) {
				t.Fatalf("new pin survived: %v", err)
			}
			assertRolePin(t, s, id, slot, input)
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND type='context.role_pinned'`, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("event survived: %d %v", count, err)
			}
		})
	}
}

func TestRoleContextCorruptionFailsClosed(t *testing.T) {
	for _, mutation := range []string{"hash", "version", "slot", "input", "malformed", "foreign_event", "drop_old"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			id, slot := turn.Run.ID, roleSlot("planner")
			if _, err := s.PinRoleContext(ctx, id, slot, json.RawMessage(`{"a":1}`)); err != nil {
				t.Fatal(err)
			}
			if mutation == "drop_old" {
				if _, err := s.SaveCheckpoint(ctx, id, 1, map[string]any{}); err != nil {
					t.Fatal(err)
				}
			}
			cp, err := s.LatestCheckpoint(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			state := checkpointObject([]byte(mustCheckpointJSON(t, s, id, cp.ID)))
			var pins map[string]pinnedRoleInput
			if err := json.Unmarshal(state["role_inputs"], &pins); err != nil {
				t.Fatal(err)
			}
			pin := pins[slot]
			switch mutation {
			case "hash":
				pin.InputHash = strings.Repeat("0", 64)
			case "version":
				pin.Version = 2
			case "slot":
				delete(pins, slot)
				slot = "foreign-id"
			case "input":
				pin.Input = json.RawMessage(`{"changed":true}`)
			case "foreign_event":
				if _, err := s.db.Exec(`UPDATE events SET type='foreign.role_pinned' WHERE task_id=? AND seq=?`, id, cp.LastEventSeq); err != nil {
					t.Fatal(err)
				}
			}
			pins[slot] = pin
			state["role_inputs"], _ = json.Marshal(pins)
			if mutation == "malformed" {
				state["role_inputs"] = json.RawMessage(`{"` + slot + `":{"version":1,"version":1,"input_hash":"` + pin.InputHash + `","input":{"a":1}}}`)
			}
			if mutation == "drop_old" {
				delete(state, "role_inputs")
			}
			encoded, _ := json.Marshal(state)
			if _, err := s.db.Exec(`UPDATE checkpoints SET state_json=?,state_hash=? WHERE id=?`, string(encoded), roleInputHash(encoded), cp.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RoleContext(ctx, id, roleSlot("planner")); !errors.Is(err, ErrCheckpointInvalid) {
				t.Fatalf("corrupt read: %v", err)
			}
			if _, err := s.PinRoleContext(ctx, id, roleSlot("new"), json.RawMessage(`{}`)); !errors.Is(err, ErrCheckpointInvalid) {
				t.Fatalf("corrupt write: %v", err)
			}
		})
	}
}

func TestRoleContextLeaseCancellationDeadlineFence(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	id, slot := turn.Run.ID, roleSlot("planner")
	if _, err := s.PinRoleContext(ctx, id, slot, json.RawMessage(`{}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued pin: %v", err)
	}
	if err := s.EnableWorkerLeases("pin-worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	if _, err := s.PinRoleContext(ctx, id, slot, json.RawMessage(`{}`)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("unowned pin: %v", err)
	}
	if _, err := s.PinRoleContext(leased, id, slot, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, []byte(`{}`))
	cancelled, cancel := context.WithCancel(leased)
	cancel()
	if _, err := s.PinRoleContext(cancelled, id, slot, json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET deadline_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinRoleContext(leased, id, slot, json.RawMessage(`{}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired pin: %v", err)
	}
	if _, err := s.TransitionTask(ctx, id, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, id, slot, []byte(`{}`))
	if _, err := s.PinRoleContext(leased, id, slot, json.RawMessage(`{}`)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale actor: %v", err)
	}
	if _, err := s.PinRoleContext(ctx, id, slot, json.RawMessage(`{}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal pin: %v", err)
	}
}

func TestRoleContextLegacyCarryForwardAndForeignCopy(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	slot, input := roleSlot("planner"), json.RawMessage(`{"integer":9007199254740993}`)
	if _, err := s.PinRoleContext(ctx, turn.Run.ID, slot, input); err != nil {
		t.Fatal(err)
	}
	source, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceState := checkpointObject([]byte(mustCheckpointJSON(t, s, turn.Run.ID, source.ID)))
	legacy, err := s.CreateTask(ctx, domain.Task{ID: "legacy-role-inputs", Title: "legacy", Description: "legacy"}, "legacy-role-inputs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, legacy.ID, domain.StatusQueued, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	claimResearchFixture(t, s)
	if _, err := s.PinRoleContext(ctx, legacy.ID, roleSlot("owned"), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpoint(ctx, legacy.ID, 1, struct {
		Summary string `json:"summary"`
	}{Summary: "typed worker"}); err != nil {
		t.Fatal(err)
	}
	assertRolePin(t, s, legacy.ID, roleSlot("owned"), []byte(`{}`))
	cp, err := s.LatestCheckpoint(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := checkpointObject([]byte(mustCheckpointJSON(t, s, legacy.ID, cp.ID)))
	state["role_inputs"] = sourceState["role_inputs"]
	encoded, _ := json.Marshal(state)
	if _, err := s.db.Exec(`UPDATE checkpoints SET state_json=?,state_hash=? WHERE id=?`, string(encoded), roleInputHash(encoded), cp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RoleContext(ctx, legacy.ID, slot); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("foreign pin accepted: %v", err)
	}
	assertRolePin(t, s, turn.Run.ID, slot, input)
}
