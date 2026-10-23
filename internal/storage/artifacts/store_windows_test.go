//go:build windows

package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsArtifactStorePrivateBoundedAndRangeReadable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := New(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	record, reference, err := store.Put(context.Background(), "task_01", "page.txt", "text/plain", []byte("evidence text"))
	if err != nil {
		t.Fatal(err)
	}
	if reference != filepath.Join("task_01", record.ID) {
		t.Fatalf("reference %q", reference)
	}
	got, err := store.Read(context.Background(), record.TaskID, record.ID)
	if err != nil || string(got) != "evidence text" {
		t.Fatalf("read %q, %v", got, err)
	}
	rangeBytes, err := store.ReadRange(context.Background(), record.TaskID, record.ID, 9, 5)
	if err != nil || string(rangeBytes) != "text" {
		t.Fatalf("range %q, %v", rangeBytes, err)
	}
	if _, _, err := store.Put(context.Background(), "task_01", "large", "text/plain", []byte(strings.Repeat("x", 65))); err == nil {
		t.Fatal("oversize artifact was accepted")
	}
	directory, err := store.taskDirectory("task_01", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateACL(windows.Handle(directory.Fd())); err != nil {
		directory.Close()
		t.Fatal(err)
	}
	directory.Close()
	entries, err := os.ReadDir(filepath.Join(root, "task_01"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".artifact-") {
			t.Fatalf("temporary artifact leaked: %s", entry.Name())
		}
	}
}

func TestWindowsArtifactStoreCancellationLeavesNoTemporaryFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := New(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.PutReader(ctx, "task", "cancel", "text/plain", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "task")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled write created task directory: %v", err)
	}
}

func TestWindowsArtifactRejectsReparsePoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := New(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, _, err := store.Put(context.Background(), "linked", "file", "text/plain", []byte("secret")); err == nil {
		t.Fatal("task directory reparse point was accepted")
	}
}
