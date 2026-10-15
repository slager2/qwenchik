package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestMemoryRetrievalRequiresAcceptanceAndHonorsScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, task := range []domain.Task{
		{ID: "memory-task-a", Title: "Task A"},
		{ID: "memory-task-b", Title: "Task B"},
	} {
		if _, err := store.CreateTask(ctx, task, ""); err != nil {
			t.Fatal(err)
		}
	}

	candidate, err := store.CreateMemory(ctx, domain.Memory{Kind: "preference", Text: "Keep answers concise and use Russian.", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != domain.MemoryCandidate {
		t.Fatalf("new memory status = %q, want candidate", candidate.Status)
	}
	if matches, err := store.SearchMemories(ctx, "concise Russian answers", "memory-task-a", 5); err != nil || len(matches) != 0 {
		t.Fatalf("candidate memory leaked into retrieval: matches=%#v err=%v", matches, err)
	}
	if _, err := store.SetMemoryStatus(ctx, candidate.ID, domain.MemoryAccepted); err != nil {
		t.Fatal(err)
	}

	taskMemory, err := store.CreateMemory(ctx, domain.Memory{Kind: "workflow", Text: "For this task, compare the two supplied options.", Scope: "task", SourceTaskID: "memory-task-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMemoryStatus(ctx, taskMemory.ID, domain.MemoryAccepted); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		taskID string
		want   int
	}{
		{taskID: "memory-task-a", want: 2},
		{taskID: "memory-task-b", want: 1},
	} {
		matches, err := store.SearchMemories(ctx, "options concise Russian", test.taskID, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != test.want {
			t.Fatalf("task %s retrieved %d memories, want %d: %#v", test.taskID, len(matches), test.want, matches)
		}
		if matches[0].Relevance < 1 {
			t.Fatalf("memory match has no relevance score: %#v", matches[0])
		}
	}

	if _, err := store.SetMemoryStatus(ctx, candidate.ID, domain.MemoryStale); err != nil {
		t.Fatal(err)
	}
	matches, err := store.SearchMemories(ctx, "concise Russian answers", "memory-task-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("stale memory remained retrievable: %#v", matches)
	}
}

func TestMemoryValidatesScopeAndEvidenceProvenance(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "memory-provenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateTask(ctx, domain.Task{ID: "memory-source", Title: "Source task"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(ctx, domain.Memory{Kind: "fact", Text: "Task-scoped memory without a task.", Scope: "task"}); !errors.Is(err, domain.ErrInvalidMemory) {
		t.Fatalf("task memory without source task error = %v", err)
	}
	if _, err := store.CreateMemory(ctx, domain.Memory{Kind: "fact", Text: "Evidence-backed memory.", Scope: "global", SourceTaskID: "memory-source", SourceEvidenceID: "missing-evidence"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing evidence error = %v", err)
	}
}
