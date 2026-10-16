package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestSkippedStepStatusMigrationPreservesExistingSteps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-steps.db")
	legacyDB, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.ExecContext(ctx, `CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" || entry.Name() == "007_step_skipped_status.sql" {
			continue
		}
		migration, err := migrationFiles.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := legacyDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(migration)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply legacy migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name,applied_at) VALUES(?,?)`, entry.Name(), stamp(time.Now().UTC())); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	created := stamp(time.Now().UTC())
	if _, err := legacyDB.ExecContext(ctx, `INSERT INTO tasks(id,title,description,status,priority,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		"legacy-step-task", "Legacy task", "Preserve the existing step", "inbox", 0, 1, created, created); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.ExecContext(ctx, `INSERT INTO task_steps(id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,attempts,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"legacy-step-id", "legacy-step-task", "step_1", 0, "analysis", "Existing completed step", "[]", "result", `["result exists"]`, "completed", 1, created, created); err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	steps, err := store.Steps(ctx, "legacy-step-task")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != "completed" || steps[0].StepID != "step_1" {
		t.Fatalf("existing task step was not preserved through migration: %#v", steps)
	}
	if err := store.SetStepStatus(ctx, "legacy-step-task", "step_1", "skipped"); err != nil {
		t.Fatal(err)
	}
	steps, err = store.Steps(ctx, "legacy-step-task")
	if err != nil || len(steps) != 1 || steps[0].Status != "skipped" {
		t.Fatalf("skipped step status was not persisted: steps=%#v err=%v", steps, err)
	}
}

func TestTaskTransitionsAreDurableAndEventsAreAtomic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, domain.Task{ID: "task-1", Title: "local research", Description: "Compare two options"}, "create-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusInbox {
		t.Fatalf("created task status = %q, want inbox", task.Status)
	}
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusCompleted, nil, "", "", nil); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("invalid transition error = %v, want ErrInvalidTransition", err)
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "task.created" {
		t.Fatalf("failed transition must not append event; got %#v", events)
	}
	if _, err := store.QueueTask(ctx, task.ID, "run-1"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.Status != domain.StatusPlanning {
		t.Fatalf("claimed task = %#v, ok=%t", claimed, ok)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusPlanning {
		t.Fatalf("reopened task status = %q, want planning", got.Status)
	}
	events, err = store.EventsAfter(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatalf("event seq at %d = %d, want %d", i, event.Seq, i+1)
		}
	}
}

func TestCreateTaskIdempotency(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.CreateTask(ctx, domain.Task{ID: "one", Title: "first"}, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateTask(ctx, domain.Task{ID: "two", Title: "ignored"}, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Title != "first" {
		t.Fatalf("idempotent create returned %#v after %#v", second, first)
	}
}

func TestInterruptedTaskIsRequeuedWithRecoveryEvent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "recover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "recover-task", Title: "Recover"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext(ctx); err != nil || !ok {
		t.Fatalf("claim next: ok=%t err=%v", ok, err)
	}
	runningStage := domain.StageSynthesis
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusRunning, &runningStage, "", "", nil); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered count = %d, want 1", recovered)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusQueued || got.Stage != nil {
		t.Fatalf("recovered task = %#v", got)
	}
	events, err := store.EventsAfter(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Type != "task.recovered" {
		t.Fatalf("last event = %q, want task.recovered", events[len(events)-1].Type)
	}
	var stageStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM task_stages WHERE task_id=? AND stage=? ORDER BY started_at DESC LIMIT 1`, task.ID, runningStage).Scan(&stageStatus); err != nil {
		t.Fatal(err)
	}
	if stageStatus != "paused" {
		t.Fatalf("interrupted stage status = %q, want paused", stageStatus)
	}
}
