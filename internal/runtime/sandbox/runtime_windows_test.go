package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsSandboxIsUnavailableWithoutCreatingWorkspaceOrCallingRunner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	runner := &fakeRunner{}
	runtime, err := New("golang:1.23-alpine", root, runner)
	if err != nil {
		t.Fatal(err)
	}

	status := runtime.Doctor(context.Background())
	if status.Available || !strings.Contains(status.Message, "windows") {
		t.Fatalf("Windows sandbox status = %#v, want explicit unavailable status", status)
	}
	if _, err := runtime.Run(context.Background(), Request{}); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("Run error = %v, want ErrSandboxUnavailable", err)
	}
	if _, err := runtime.Workspace("task-windows"); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("Workspace error = %v, want ErrSandboxUnavailable", err)
	}
	if err := runtime.CleanupTask("task-windows"); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("CleanupTask error = %v, want ErrSandboxUnavailable", err)
	}
	if runner.runs != 0 {
		t.Fatalf("Docker runner called %d times on Windows", runner.runs)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox workspace root was created: %v", err)
	}
}
