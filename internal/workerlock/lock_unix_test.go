//go:build unix

package workerlock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestOnlyOneWorkerMayOwnDatabaseLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second lock error = %v, want ErrAlreadyRunning", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	_ = second.Close()
}
