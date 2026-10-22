package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadRangeBoundedOffsetAndCancellation(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	content := "prefix" + strings.Repeat("a", 1<<20) + "suffix"
	artifact, _, err := store.Put(context.Background(), "task", "large.txt", "text/plain", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes = 1
	for _, test := range []struct {
		offset int64
		limit  int
		want   string
	}{
		{0, 6, "prefix"}, {int64(len(content) - 6), 64 << 10, "suffix"}, {int64(len(content)), 1, ""}, {6, 64 << 10, strings.Repeat("a", 64<<10)},
	} {
		got, err := store.ReadRange(context.Background(), "task", artifact.ID, test.offset, test.limit)
		if err != nil || string(got) != test.want {
			t.Fatalf("offset=%d limit=%d bytes=%d err=%v", test.offset, test.limit, len(got), err)
		}
	}
	for _, test := range []struct {
		task, id string
		offset   int64
		limit    int
	}{
		{"task", artifact.ID, -1, 1}, {"task", artifact.ID, 0, 0}, {"task", artifact.ID, 0, 65537}, {"task", artifact.ID, int64(len(content) + 1), 1},
		{"../task", artifact.ID, 0, 1}, {"task", "../secret", 0, 1}, {"other", artifact.ID, 0, 1},
	} {
		if _, err := store.ReadRange(context.Background(), test.task, test.id, test.offset, test.limit); err == nil {
			t.Fatalf("invalid range accepted: %+v", test)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ReadRange(ctx, "task", artifact.ID, 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestReadRangeRejectsSymlinksNonregularAndNonprivateFiles(t *testing.T) {
	for _, kind := range []string{"file_symlink", "task_symlink", "root_symlink", "fifo", "public_file", "public_task"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			store, err := New(root, 1024)
			if err != nil {
				t.Fatal(err)
			}
			artifact, _, err := store.Put(context.Background(), "task", "text.txt", "text/plain", []byte("private"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "task", artifact.ID)
			switch kind {
			case "file_symlink":
				if err := os.Rename(path, path+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-real", path); err != nil {
					t.Fatal(err)
				}
			case "task_symlink":
				if err := os.Rename(filepath.Join(root, "task"), filepath.Join(root, "real")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "task")); err != nil {
					t.Fatal(err)
				}
			case "root_symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				store.root = alias
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "public_file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "public_task":
				if err := os.Chmod(filepath.Join(root, "task"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.ReadRange(context.Background(), "task", artifact.ID, 0, 16); err == nil {
				t.Fatal("unsafe artifact was read")
			}
		})
	}
}
