package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	security string
	image    bool
	args     []string
	stdout   string
	stderr   string
	runs     int
	runErr   error
	cleanups int
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "docker" {
		return nil, errors.New("unexpected binary")
	}
	if len(args) > 0 && args[0] == "context" {
		return []byte("unix:///run/user/1000/docker.sock"), nil
	}
	if len(args) > 0 && args[0] == "context" {
		return []byte("unix:///run/user/1000/docker.sock"), nil
	}
	if len(args) > 0 && args[0] == "info" {
		return []byte(f.security), nil
	}
	if len(args) > 0 && args[0] == "image" && f.image {
		return []byte("sha256:local-image"), nil
	}
	return nil, errors.New("image missing")
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdout, stderr io.Writer) error {
	if name != "docker" {
		return errors.New("unexpected binary")
	}
	f.runs++
	f.args = append([]string(nil), args...)
	if len(args) > 0 && args[0] == "rm" {
		f.cleanups++
		return nil
	}
	_, _ = io.WriteString(stdout, f.stdout)
	_, _ = io.WriteString(stderr, f.stderr)
	return f.runErr
}

func TestDockerSandboxUsesRootlessIsolatedContainer(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := &fakeRunner{security: ` ["name=seccomp,profile=builtin","name=rootless"] `, image: true, stdout: "ok"}
	runtime, err := New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspaces"), runner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(context.Background(), Request{TaskID: "task-01", Command: []string{"sh", "-c", "printf ok"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.args, " ")
	for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit 64", "--memory 512m", "--cpus 1", "--user 0:0", "type=bind,src=", "dst=/workspace,rw", "golang:1.23-alpine", "sh -c printf ok"} {
		if !strings.Contains(joined, required) {
			t.Errorf("docker args missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, ".docker") || strings.Contains(joined, "/home/") || result.Stdout != "ok" || result.ExecutionID == "" || runner.runs != 1 {
		t.Fatalf("unexpected sandbox result or host mount: result=%#v args=%s", result, joined)
	}
}

func TestSandboxUnavailableNeverRunsCommandOnHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := &fakeRunner{security: `["name=seccomp,profile=builtin"]`, image: true}
	runtime, err := New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspaces"), runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(context.Background(), Request{TaskID: "task-02", Command: []string{"touch", "/tmp/host"}}); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("Run error = %v, want ErrSandboxUnavailable", err)
	}
	if runner.runs != 0 {
		t.Fatalf("container runner executed %d times without rootless Docker", runner.runs)
	}
}

func TestSandboxDoctorRunsOnlyBoundedIsolatedSmoke(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := &fakeRunner{security: `["name=rootless"]`, image: true}
	runtime, err := New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspaces"), runner)
	if err != nil {
		t.Fatal(err)
	}
	status := runtime.Doctor(context.Background())
	if !status.Available || runner.runs != 1 {
		t.Fatalf("sandbox doctor status=%#v runs=%d", status, runner.runs)
	}
	args := strings.Join(runner.args, " ")
	if !strings.Contains(args, "--network=none") || !strings.Contains(args, "--read-only") || !strings.HasSuffix(args, "golang:1.23-alpine true") {
		t.Fatalf("unsafe doctor smoke command: %s", args)
	}
}

func TestSandboxRejectsRemoteDockerEndpoint(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "tcp://docker.example:2376")
	runner := &fakeRunner{security: `["name=rootless"]`, image: true}
	runtime, err := New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspaces"), runner)
	if err != nil {
		t.Fatal(err)
	}
	status := runtime.Doctor(context.Background())
	if status.Available || !strings.Contains(status.Message, "remote Docker contexts") || runner.runs != 0 {
		t.Fatalf("remote docker endpoint status=%#v runs=%d", status, runner.runs)
	}
}

func TestSandboxBoundsOutputAndRejectsInvalidRequests(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := &fakeRunner{security: `["name=rootless"]`, image: true, stdout: strings.Repeat("x", 128)}
	runtime, err := New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspaces"), runner)
	if err != nil {
		t.Fatal(err)
	}
	runtime.limits.OutputBytes = 16
	result, err := runtime.Run(context.Background(), Request{TaskID: "task-03", Command: []string{"echo", "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != 16 || !result.OutputTruncated {
		t.Fatalf("output bound failed: stdout=%d truncated=%t", len(result.Stdout), result.OutputTruncated)
	}
	if _, err := runtime.Run(context.Background(), Request{TaskID: "../outside", Command: []string{"echo", "bad"}}); err == nil {
		t.Fatal("invalid task ID was accepted")
	}
	if _, err := runtime.Run(context.Background(), Request{TaskID: "task-03", Command: []string{"echo", "bad\x00arg"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("NUL argument error = %v", err)
	}
	if len(bytes.TrimSpace([]byte(result.Stdout))) != 16 {
		t.Fatal("unexpected truncated output")
	}
}

func TestWorkspaceCleanupIsTaskScoped(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	runtime, err := New("golang:1.23-alpine", root, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runtime.Workspace("task-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Workspace("task-two")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "generated.txt"), []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CleanupTask("task-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task-one workspace still exists: %v", err)
	}
	if info, err := os.Stat(second); err != nil || !info.IsDir() {
		t.Fatalf("task-two workspace was removed: %v", err)
	}
}

func TestSandboxCleansContainerAfterRunnerFailure(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := &fakeRunner{security: `["name=rootless"]`, image: true, runErr: errors.New("docker client interrupted")}
	runtime, err := New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspaces"), runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(context.Background(), Request{TaskID: "task-cleanup", Command: []string{"sh", "-c", "sleep 30"}, Timeout: time.Second}); err == nil {
		t.Fatal("expected sandbox runner failure")
	}
	if runner.cleanups != 1 {
		t.Fatalf("docker container cleanup calls = %d, want 1", runner.cleanups)
	}
}
