//go:build unix

package research

import (
	"golang.org/x/sys/unix"
	"path/filepath"
	"testing"
	"time"
)

func TestOfflineCorpusRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := unix.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := LoadOfflineCorpus(p); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("corpus loader blocked on FIFO")
	}
}
