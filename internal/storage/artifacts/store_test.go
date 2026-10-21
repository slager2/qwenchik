package artifacts

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactStoreWritesAtomicallyAndChecksIdentifiers(t *testing.T) {
	store, err := New(t.TempDir(), 64)
	if err != nil {
		t.Fatal(err)
	}
	record, reference, err := store.Put(context.Background(), "task_01", "page.txt", "text/plain", []byte("evidence text"))
	if err != nil {
		t.Fatal(err)
	}
	if record.ID == "" || record.SHA256 == "" || reference != "task_01/"+record.ID {
		t.Fatalf("unexpected artifact metadata: %#v %q", record, reference)
	}
	content, err := store.Read(context.Background(), record.TaskID, record.ID)
	if err != nil || string(content) != "evidence text" {
		t.Fatalf("read artifact content=%q err=%v", content, err)
	}
	duplicate, _, err := store.Put(context.Background(), "task_01", "renamed.txt", "text/plain", []byte("evidence text"))
	if err != nil || duplicate.ID != record.ID {
		t.Fatalf("duplicate content was not idempotent: record=%#v err=%v", duplicate, err)
	}
	if _, _, err := store.Put(context.Background(), "../outside", "file", "text/plain", []byte("x")); err == nil {
		t.Fatal("path traversal task ID must be rejected")
	}
	if _, _, err := store.Put(context.Background(), "task_01", "large", "text/plain", []byte(strings.Repeat("x", 65))); err == nil {
		t.Fatal("oversize artifact must be rejected")
	}
}

func TestPutReaderBoundedCancelAndDeterministicIdentity(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	reader := strings.NewReader(strings.Repeat("x", 1000))
	if _, _, err := store.PutReader(context.Background(), "task", "large", "text/plain", reader); err == nil {
		t.Fatal("overflow accepted")
	}
	if reader.Len() != 935 {
		t.Fatalf("read %d bytes, want 65", 1000-reader.Len())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.PutReader(ctx, "task", "cancel", "text/plain", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	first, _, err := store.PutReader(context.Background(), "task", "file", "text/plain", strings.NewReader("content"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Put(context.Background(), "task", "renamed", "text/plain", []byte("content"))
	if err != nil || first.ID != second.ID || second.SizeBytes != 7 {
		t.Fatalf("identity %#v %#v %v", first, second, err)
	}
	names, err := os.ReadDir(filepath.Join(root, "task"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(name.Name(), ".artifact-") {
			t.Fatalf("partial file %s", name.Name())
		}
	}
}

func TestArtifactStoreRejectsSymlinksAndCorruptDuplicates(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := store.Put(context.Background(), "task", "file", "text/plain", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "task", a.ID)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutReader(context.Background(), "task", "file", "text/plain", strings.NewReader("content")); err == nil {
		t.Fatal("corrupt duplicate accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutReader(context.Background(), "task", "file", "text/plain", strings.NewReader("content")); err == nil {
		t.Fatal("duplicate symlink followed")
	}
	if _, err := store.Read(context.Background(), "task", a.ID); err == nil {
		t.Fatal("read symlink followed")
	}
	link := filepath.Join(root, "other")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutReader(context.Background(), "other", "file", "text/plain", strings.NewReader("content")); err == nil {
		t.Fatal("task directory symlink followed")
	}
	if _, err := New(link, 64); err == nil {
		t.Fatal("root symlink followed")
	}
}

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) { return copy(p, "partial"), io.ErrUnexpectedEOF }
func TestPutReaderReadFailureLeavesNoArtifact(t *testing.T) {
	root := t.TempDir()
	s, err := New(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutReader(context.Background(), "task", "file", "text/plain", failingReader{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	names, _ := os.ReadDir(filepath.Join(root, "task"))
	if len(names) != 0 {
		t.Fatalf("failed write left files: %v", names)
	}
}
