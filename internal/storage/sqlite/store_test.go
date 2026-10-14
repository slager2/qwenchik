package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

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
