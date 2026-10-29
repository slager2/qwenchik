package sqlite

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestCheckpointStorageCompressesLargeStatesAndKeepsLegacyJSONReadable(t *testing.T) {
	small := []byte(`{"state":"small"}`)
	stored, err := encodeCheckpointStorage(small)
	if err != nil {
		t.Fatal(err)
	}
	if stored != string(small) {
		t.Fatalf("small state was unexpectedly encoded: %q", stored[:min(len(stored), 20)])
	}
	decoded, err := decodeCheckpointStorage(stored)
	if err != nil || !bytes.Equal(decoded, small) {
		t.Fatalf("small round trip: %q %v", decoded, err)
	}

	large := []byte(`{"role_inputs":{"writer":{"input":"` + strings.Repeat("repeated evidence text ", 20_000) + `"}}}`)
	stored, err = encodeCheckpointStorage(large)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, checkpointStoragePrefix) || len(stored) >= len(large) {
		t.Fatalf("large state was not compressed: stored=%d raw=%d", len(stored), len(large))
	}
	decoded, err = decodeCheckpointStorage(stored)
	if err != nil || !bytes.Equal(decoded, large) {
		t.Fatalf("large round trip: decoded=%d want=%d err=%v", len(decoded), len(large), err)
	}
}

func TestCheckpointStorageRejectsCorruptCompressedState(t *testing.T) {
	if _, err := decodeCheckpointStorage(checkpointStoragePrefix + "not-base64"); err == nil {
		t.Fatal("corrupt compressed state was accepted")
	}
}

func TestStorePersistsLargeCheckpointCompressedAndReadsItBack(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "checkpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "compressed-checkpoint", Title: "large checkpoint"}, "")
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"payload": strings.Repeat("evidence ", 20_000)}
	if _, err := store.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := store.db.QueryRow(`SELECT state_json FROM checkpoints WHERE task_id=?`, task.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, checkpointStoragePrefix) {
		t.Fatalf("large checkpoint was stored without compression: %d bytes", len(stored))
	}
	checkpoint, err := store.LatestCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.State == nil {
		t.Fatal("compressed checkpoint did not decode")
	}
}
