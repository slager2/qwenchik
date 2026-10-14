package artifacts

import (
	"context"
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
