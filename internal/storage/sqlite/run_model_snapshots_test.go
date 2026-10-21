package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func chatModelSnapshotFixture(t *testing.T) (*Store, domain.SubmitResponse, domain.ModelProfileSnapshot, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat-model-snapshots.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c, err := s.CreateConversation(ctx, domain.Conversation{Title: "Chat snapshots", Mode: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, model := standardResearchSessionDefaults()
	model.Model, model.BaseURL = "chat-model", "http://127.0.0.1:8000/v1"
	model.ContextLength, model.MaxOutputTokens = 8192, 512
	model.Temperature, model.TopP = 0.7, 0.9
	model.StopSequences, model.PromptVersion = []string{"chat-stop"}, "local-chat-v1"
	turn, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "A model-specific chat question", ModelSnapshot: &model, DeadlineSeconds: 75}, "chat-snapshot-first")
	if err != nil {
		t.Fatal(err)
	}
	return s, turn, model, path
}

func TestChatRunModelSnapshotInitialCheckpointAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s, turn, model, _ := chatModelSnapshotFixture(t)
	snapshot, err := s.GetRunModelSnapshot(ctx, turn.Run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, model) {
		t.Fatalf("run model snapshot: %#v %v", snapshot, err)
	}
	checkpoints, err := s.Checkpoints(ctx, turn.Run.ID)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("initial checkpoints: %#v %v", checkpoints, err)
	}
	values := checkpoints[0].State.(map[string]any)
	if len(values) != 2 || values["model_snapshot_schema_version"] != float64(1) || values["model_snapshot"].(map[string]any)["model"] != model.Model || checkpoints[0].PlanVersion != 1 {
		t.Fatalf("initial chat snapshot payload: %#v", checkpoints[0])
	}
	model.StopSequences[0] = "changed-input"
	snapshot.StopSequences[0] = "changed-read-result"
	readBack, err := s.GetRunModelSnapshot(ctx, turn.Run.ID)
	if err != nil || readBack.StopSequences[0] != "chat-stop" {
		t.Fatalf("snapshot aliased caller memory: %#v %v", readBack, err)
	}
	replay, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Content: turn.UserMessage.Content, ModelSnapshot: &model}, "chat-snapshot-first")
	if err != nil || replay.Run.ID != turn.Run.ID {
		t.Fatalf("snapshot change broke request idempotency: %#v %v", replay, err)
	}
	checkpoints, err = s.Checkpoints(ctx, turn.Run.ID)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("idempotency duplicated snapshot checkpoint: %#v %v", checkpoints, err)
	}
	if _, err := s.GetResearchSession(ctx, turn.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chat acquired a research session: %v", err)
	}
	if _, err := s.ReserveResearchLLMCall(ctx, turn.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chat acquired research call counters: %v", err)
	}
	var sessions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM research_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("chat created research session rows: %d %v", sessions, err)
	}
}

func TestChatRunModelSnapshotIsImmutableThroughCheckpointAndCompletion(t *testing.T) {
	ctx := context.Background()
	s, turn, model, _ := chatModelSnapshotFixture(t)
	initial, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var originalJSON string
	if err := s.db.QueryRow(`SELECT state_json FROM checkpoints WHERE id=?`, initial.ID).Scan(&originalJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER immutable_initial_chat_checkpoint BEFORE UPDATE ON checkpoints BEGIN SELECT RAISE(ABORT,'initial checkpoint must remain immutable'); END`); err != nil {
		t.Fatal(err)
	}
	changed := model
	changed.Model, changed.Temperature = "changed-model", 1.8
	input := map[string]any{"model_snapshot_schema_version": 1, "model_snapshot": changed, "future_chat_state": map[string]any{"opaque": "preserve"}}
	checkpoint, err := s.SaveCheckpoint(ctx, turn.Run.ID, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	if input["model_snapshot"].(domain.ModelProfileSnapshot).Model != "changed-model" {
		t.Fatal("checkpoint preparation mutated caller state")
	}
	values := checkpoint.State.(map[string]any)
	if values["model_snapshot"].(map[string]any)["model"] != model.Model || values["future_chat_state"].(map[string]any)["opaque"] != "preserve" || values["counters"] != nil {
		t.Fatalf("chat checkpoint did not preserve authoritative model: %#v", checkpoint)
	}
	verifyConversationRun(t, s, turn.Run.ID)
	if _, err := s.CompleteTask(ctx, turn.Run.ID, "A model-specific answer", nil); err != nil {
		t.Fatal(err)
	}
	final, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil || final.State.(map[string]any)["model_snapshot_schema_version"] != float64(1) || final.State.(map[string]any)["model_snapshot"].(map[string]any)["model"] != model.Model {
		t.Fatalf("completion lost model snapshot: %#v %v", final, err)
	}
	snapshot, err := s.GetRunModelSnapshot(ctx, turn.Run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, model) {
		t.Fatalf("completion changed model snapshot: %#v %v", snapshot, err)
	}
	var afterJSON string
	if err := s.db.QueryRow(`SELECT state_json FROM checkpoints WHERE id=?`, initial.ID).Scan(&afterJSON); err != nil || afterJSON != originalJSON {
		t.Fatalf("initial checkpoint changed: %s %v", afterJSON, err)
	}
}

func TestChatRunModelSnapshotClonesAcrossRetryContinueAndProfileChanges(t *testing.T) {
	ctx := context.Background()
	s, first, model, _ := chatModelSnapshotFixture(t)
	verifyConversationRun(t, s, first.Run.ID)
	if _, err := s.CompleteTask(ctx, first.Run.ID, "Initial answer", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateConversation(ctx, first.ConversationID, map[string]any{"model_profile": map[string]any{"temperature": 1.9, "max_output_tokens": 2048}}); err != nil {
		t.Fatal(err)
	}
	replacement := model
	replacement.Model, replacement.MaxOutputTokens, replacement.Temperature = "replacement-model", 2048, 1.9
	replacement.StopSequences = []string{"replacement-stop"}
	prior := first
	for _, operation := range []string{"retry", "continue"} {
		turn, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Operation: operation, SourceRunID: prior.Run.ID, ModelSnapshot: &replacement, DeadlineSeconds: 9}, "chat-"+operation)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.GetRunModelSnapshot(ctx, turn.Run.ID)
		if err != nil || !reflect.DeepEqual(snapshot, model) {
			t.Fatalf("%s used changed conversation settings: %#v %v", operation, snapshot, err)
		}
		if turn.Run.DeadlineAt == nil || !turn.Run.DeadlineAt.Equal(turn.Run.CreatedAt.Add(9*time.Second)) {
			t.Fatalf("%s inherited the prior deadline: %#v", operation, turn.Run)
		}
		checkpoints, err := s.Checkpoints(ctx, turn.Run.ID)
		if err != nil || len(checkpoints) != 1 || checkpoints[0].CreatedAt.Before(turn.Run.CreatedAt) {
			t.Fatalf("%s did not create fresh model metadata: %#v %v", operation, checkpoints, err)
		}
		verifyConversationRun(t, s, turn.Run.ID)
		if _, err := s.CompleteTask(ctx, turn.Run.ID, "Another answer", nil); err != nil {
			t.Fatal(err)
		}
		prior = turn
	}
	next, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "A fresh send", ModelSnapshot: &replacement}, "chat-fresh-send")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetRunModelSnapshot(ctx, next.Run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, replacement) {
		t.Fatalf("new send did not use its supplied snapshot: %#v %v", snapshot, err)
	}
	if !next.Run.DeadlineAt.Equal(next.Run.CreatedAt.Add(7200 * time.Second)) {
		t.Fatalf("chat default deadline changed: %#v", next.Run)
	}
	var sessions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM research_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("chat clones created research sessions: %d %v", sessions, err)
	}
}

func TestChatRunModelSnapshotSurvivesReopenAndRecovery(t *testing.T) {
	ctx := context.Background()
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "partial"}[partial], func(t *testing.T) {
			s, turn, model, path := chatModelSnapshotFixture(t)
			verifyConversationRun(t, s, turn.Run.ID)
			if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 2, map[string]any{"chat_progress": "saved"}); err != nil {
				t.Fatal(err)
			}
			if partial {
				if _, err := s.RecordMessageDelta(ctx, turn.Run.ID, "chat-stream", "generated", 0, "Saved partial answer"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.GetRunModelSnapshot(ctx, turn.Run.ID)
			if err != nil || !reflect.DeepEqual(snapshot, model) {
				t.Fatalf("recovery changed model: %#v %v", snapshot, err)
			}
			run, err := s.GetRun(ctx, turn.Run.ID)
			if err != nil || !run.DeadlineAt.Equal(*turn.Run.DeadlineAt) {
				t.Fatalf("recovery changed deadline: %#v %v", run, err)
			}
			want := domain.StatusQueued
			if partial {
				want = domain.StatusFailed
			}
			if run.Status != want {
				t.Fatalf("recovered status %s, want %s", run.Status, want)
			}
		})
	}
}

func TestChatRunModelSnapshotSubmissionRollback(t *testing.T) {
	ctx := context.Background()
	s, first, model, _ := chatModelSnapshotFixture(t)
	if _, err := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetConversation(ctx, first.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	var beforeEvents int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_initial_chat_checkpoint BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT,'snapshot checkpoint fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	for _, input := range []domain.SubmitTurn{
		{ConversationID: first.ConversationID, Content: "Must roll back", ModelSnapshot: &model},
		{ConversationID: first.ConversationID, SourceRunID: first.Run.ID, Operation: "retry", ModelSnapshot: &model},
	} {
		if _, err := s.SubmitTurn(ctx, input, "chat-rollback"); err == nil {
			t.Fatal("expected snapshot insertion failure")
		}
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM tasks`:                                      1,
		`SELECT COUNT(*) FROM conversation_messages`:                      2,
		`SELECT COUNT(*) FROM checkpoints`:                                1,
		`SELECT COUNT(*) FROM idempotency_keys WHERE key='chat-rollback'`: 0,
		`SELECT COUNT(*) FROM events`:                                     beforeEvents,
	} {
		var got int
		if err := s.db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("partial submission for %s: got %d want %d: %v", query, got, want, err)
		}
	}
	after, err := s.GetConversation(ctx, first.ConversationID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("failed submission changed conversation: %#v %v", after, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_initial_chat_checkpoint`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "Must roll back", ModelSnapshot: &model}, "chat-rollback"); err != nil {
		t.Fatalf("rollback consumed idempotency key: %v", err)
	}
}

func TestRunModelSnapshotLegacyAndResearchLookup(t *testing.T) {
	ctx := context.Background()
	s, legacy := conversationFixture(t, "chat")
	if _, err := s.GetRunModelSnapshot(ctx, legacy.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy chat acquired a snapshot: %v", err)
	}
	checkpoints, err := s.Checkpoints(ctx, legacy.Run.ID)
	if err != nil || len(checkpoints) != 0 {
		t.Fatalf("legacy chat acquired an initial checkpoint: %#v %v", checkpoints, err)
	}
	if _, err := s.SaveCheckpoint(ctx, legacy.Run.ID, 1, map[string]any{"legacy": true}); err != nil {
		t.Fatal(err)
	}
	_, _, model := standardResearchSessionDefaults()
	if _, err := s.SaveCheckpoint(ctx, legacy.Run.ID, 1, map[string]any{"model_snapshot_schema_version": 1, "model_snapshot": model}); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy chat snapshot backfill accepted: %v", err)
	}
	verifyConversationRun(t, s, legacy.Run.ID)
	if _, err := s.CompleteTask(ctx, legacy.Run.ID, "Legacy answer", nil); err != nil {
		t.Fatal(err)
	}
	retry, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: legacy.ConversationID, Operation: "retry", SourceRunID: legacy.Run.ID}, "legacy-chat-retry")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{legacy.Run.ID, retry.Run.ID, "missing-run"} {
		if _, err := s.GetRunModelSnapshot(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("legacy lookup for %s: %v", id, err)
		}
	}
	checkpoints, err = s.Checkpoints(ctx, retry.Run.ID)
	if err != nil || len(checkpoints) != 0 {
		t.Fatalf("legacy retry acquired a snapshot checkpoint: %#v %v", checkpoints, err)
	}
	if _, err := s.TransitionTask(ctx, retry.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	modern, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: legacy.ConversationID, Operation: "retry", SourceRunID: retry.Run.ID, ModelSnapshot: &model}, "legacy-chat-retry-with-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, err := s.GetRunModelSnapshot(ctx, modern.Run.ID); err != nil || !reflect.DeepEqual(snapshot, model) {
		t.Fatalf("new legacy retry ignored supplied model snapshot: %#v %v", snapshot, err)
	}
	if _, err := s.GetRunModelSnapshot(ctx, legacy.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("new retry backfilled original legacy run: %v", err)
	}
	research, turn, _ := researchConversationFixture(t, nil)
	session, err := research.GetResearchSession(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := research.GetRunModelSnapshot(ctx, turn.Run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, session.ModelSnapshot) {
		t.Fatalf("research lookup did not use its session: %#v %v", snapshot, err)
	}
}

func TestChatRunModelSnapshotsRejectCorruptOrUnsupportedMetadata(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"future-envelope", func(state map[string]any) { state["model_snapshot_schema_version"] = 2 }},
		{"missing-version", func(state map[string]any) { delete(state, "model_snapshot_schema_version") }},
		{"missing-model", func(state map[string]any) { delete(state, "model_snapshot") }},
		{"missing-all-metadata", func(state map[string]any) {
			delete(state, "model_snapshot")
			delete(state, "model_snapshot_schema_version")
		}},
		{"future-model-version", func(state map[string]any) { state["model_snapshot"].(map[string]any)["version"] = 2 }},
		{"future-model-schema", func(state map[string]any) { state["model_snapshot"].(map[string]any)["schema_version"] = 2 }},
		{"invalid-model-context", func(state map[string]any) { state["model_snapshot"].(map[string]any)["context_length"] = 0 }},
		{"remote-model", func(state map[string]any) {
			state["model_snapshot"].(map[string]any)["base_url"] = "https://example.org/v1"
		}},
		{"unknown-model-field", func(state map[string]any) { state["model_snapshot"].(map[string]any)["unsupported_field"] = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, turn, _, _ := chatModelSnapshotFixture(t)
			checkpoint, err := s.LatestCheckpoint(ctx, turn.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			values := checkpoint.State.(map[string]any)
			test.change(values)
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE checkpoints SET state_json=? WHERE id=?`, string(encoded), checkpoint.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetRunModelSnapshot(ctx, turn.Run.ID); err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("corrupt snapshot accepted or downgraded to legacy: %v", err)
			}
			if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{}); err == nil {
				t.Fatal("corrupt snapshot silently overwritten")
			}
		})
	}
}

func TestChatRunModelSnapshotWritesFailClosedForInvalidModels(t *testing.T) {
	ctx := context.Background()
	s, turn, model, _ := chatModelSnapshotFixture(t)
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*domain.ModelProfileSnapshot){
		func(model *domain.ModelProfileSnapshot) { model.BaseURL = "https://example.org/v1" },
		func(model *domain.ModelProfileSnapshot) { model.Version = 2 },
		func(model *domain.ModelProfileSnapshot) { model.ContextLength = model.MaxOutputTokens },
		func(model *domain.ModelProfileSnapshot) { model.Temperature = math.NaN() },
	} {
		invalid := model
		change(&invalid)
		if _, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Content: "Invalid model", ModelSnapshot: &invalid}, "invalid-chat-model"); err == nil {
			t.Fatal("invalid model snapshot accepted")
		}
	}
	var tasks int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("invalid model left task rows: %d %v", tasks, err)
	}
	emptyEndpoint := model
	emptyEndpoint.BaseURL = ""
	if _, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, Content: "Local compatibility model", ModelSnapshot: &emptyEndpoint}, "valid-empty-chat-endpoint"); err != nil {
		t.Fatalf("empty local endpoint compatibility rejected: %v", err)
	}
}

func TestChatRunModelSnapshotCheckpointValidationIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, turn, model, _ := chatModelSnapshotFixture(t)
	modelJSON, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []any{
		map[string]any{"model_snapshot_schema_version": 2, "model_snapshot": model},
		map[string]any{"model_snapshot_schema_version": 1},
		map[string]any{"model_snapshot_schema_version": 1, "model_snapshot": nil},
		json.RawMessage(`{"model_snapshot_schema_version":2,"model_snapshot_schema_version":1,"model_snapshot":` + string(modelJSON) + `}`),
	} {
		if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 3, state); err == nil {
			t.Fatal("invalid chat snapshot checkpoint accepted")
		}
	}
	checkpoints, err := s.Checkpoints(ctx, turn.Run.ID)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("invalid snapshot appended a checkpoint: %#v %v", checkpoints, err)
	}
	events, err := s.EventsAfter(ctx, turn.Run.ID, 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatalf("invalid snapshot appended events: %#v %v", events, err)
	}
	snapshot, err := s.GetRunModelSnapshot(ctx, turn.Run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, model) {
		t.Fatalf("invalid checkpoint changed model: %#v %v", snapshot, err)
	}
}

func TestChatRunModelSnapshotRejectsCorruptedMirrorsAndMissingInitialCheckpoint(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"future-mirror", "changed-mirror", "missing-mirror", "missing-initial"} {
		t.Run(scenario, func(t *testing.T) {
			s, turn, _, _ := chatModelSnapshotFixture(t)
			if scenario == "missing-initial" {
				if _, err := s.db.Exec(`DELETE FROM checkpoints WHERE task_id=?`, turn.Run.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				verifyConversationRun(t, s, turn.Run.ID)
				if _, err := s.CompleteTask(ctx, turn.Run.ID, "A finished answer", nil); err != nil {
					t.Fatal(err)
				}
				latest, err := s.LatestCheckpoint(ctx, turn.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				values := latest.State.(map[string]any)
				switch scenario {
				case "future-mirror":
					values["model_snapshot_schema_version"] = 2
				case "changed-mirror":
					values["model_snapshot"].(map[string]any)["model"] = "another-model"
				case "missing-mirror":
					delete(values, "model_snapshot")
					delete(values, "model_snapshot_schema_version")
				}
				encoded, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE checkpoints SET state_json=? WHERE id=?`, string(encoded), latest.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: turn.ConversationID, SourceRunID: turn.Run.ID, Operation: "retry"}, "corrupt-chat-retry"); err == nil {
					t.Fatal("retry cloned incompatible model metadata")
				}
				var tasks int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil || tasks != 1 {
					t.Fatalf("corrupt source left a retry task: %d %v", tasks, err)
				}
			}
			if _, err := s.GetRunModelSnapshot(ctx, turn.Run.ID); err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("corrupt metadata accepted or downgraded to legacy: %v", err)
			}
			if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, nil); err == nil {
				t.Fatal("corrupt metadata overwritten by a later checkpoint")
			}
		})
	}
}
