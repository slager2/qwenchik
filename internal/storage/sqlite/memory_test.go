package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
		if test.taskID == "memory-task-a" {
			if matches[0].Relevance != 2 || matches[1].Relevance != 1 {
				t.Fatalf("memory matches were not ranked by existing token relevance: %#v", matches)
			}
			limited, err := store.SearchMemories(ctx, "options concise Russian", test.taskID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(limited) != 1 || limited[0].Memory.ID != matches[0].Memory.ID {
				t.Fatalf("bounded memory results = %#v, want top-ranked result %s", limited, matches[0].Memory.ID)
			}
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

func TestMemoryFTS5FreshSchemaTriggerSynchronizationAndSafeQueries(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "memory-fts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var ftsTableCount, triggerCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='memory_fts' AND sql LIKE '%fts5%'`).Scan(&ftsTableCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'memories_fts_%'`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if ftsTableCount != 1 || triggerCount != 3 {
		t.Fatalf("FTS schema has table count %d and trigger count %d, want 1 and 3", ftsTableCount, triggerCount)
	}

	memory, err := store.CreateMemory(ctx, domain.Memory{
		Kind: "fact", Text: "Literal OR AND NOT: alpha+beta, (punctuation)!", Scope: "global",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMemoryStatus(ctx, memory.ID, domain.MemoryAccepted); err != nil {
		t.Fatal(err)
	}
	// OR and NOT are ordinary search terms here, and punctuation is tokenized
	// before each term is quoted into the FTS expression.
	matches, err := store.SearchMemories(ctx, `OR NOT "alpha" OR beta* : (AND)`, "", 5)
	if err != nil {
		t.Fatalf("search operator and punctuation query: %v", err)
	}
	if len(matches) != 1 || matches[0].Memory.ID != memory.ID {
		t.Fatalf("operator/punctuation query matches = %#v, want memory %s", matches, memory.ID)
	}

	if _, err := store.UpdateMemory(ctx, memory.ID, "fact", "Fresh clementine content only.", nil); err != nil {
		t.Fatal(err)
	}
	if matches, err := store.SearchMemories(ctx, "alpha beta", "", 5); err != nil || len(matches) != 0 {
		t.Fatalf("updated memory remained indexed under old text: matches=%#v err=%v", matches, err)
	}
	if matches, err := store.SearchMemories(ctx, "clementine", "", 5); err != nil || len(matches) != 1 {
		t.Fatalf("updated memory was not indexed under new text: matches=%#v err=%v", matches, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM memories WHERE id=?`, memory.ID); err != nil {
		t.Fatal(err)
	}
	if matches, err := store.SearchMemories(ctx, "clementine", "", 5); err != nil || len(matches) != 0 {
		t.Fatalf("deleted memory remained indexed: matches=%#v err=%v", matches, err)
	}
}

func TestMemoryFTS5MigrationBackfillsExistingMemories(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-memory.db")
	legacyDB, err := openSQLite(sqliteDSN(path))
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
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" || entry.Name() == "006_memory_fts5.sql" {
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
	if _, err := legacyDB.ExecContext(ctx, `INSERT INTO memories(id,kind,status,text,metadata_json,scope,confidence,source_evidence_id,source_task_id,user_editable,retention_policy,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"legacy-memory", "fact", domain.MemoryAccepted, "Legacy searchable migration phrase", `{}`, "global", nil, nil, nil, 1, "until_deleted", stamp(time.Now().UTC()), stamp(time.Now().UTC())); err != nil {
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
	matches, err := store.SearchMemories(ctx, "searchable migration", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Memory.ID != "legacy-memory" {
		t.Fatalf("legacy memory backfill matches = %#v, want legacy-memory", matches)
	}
}

func TestSQLitePoolAllowsReadsWhileWriterIsOpen(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 10 {
		t.Fatalf("MaxOpenConnections = %d, want 10", got)
	}
	task, err := store.CreateTask(ctx, domain.Task{ID: "pool-task", Title: "pool read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	writeTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writeTx.Rollback()
	if _, err := writeTx.ExecContext(ctx, `UPDATE tasks SET title=? WHERE id=?`, "uncommitted title", task.ID); err != nil {
		t.Fatal(err)
	}

	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := store.GetTask(readCtx, task.ID)
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("concurrent read while a writer is open: %v", err)
		}
	case <-readCtx.Done():
		t.Fatalf("read did not complete while a writer was open: %v", readCtx.Err())
	}
}
