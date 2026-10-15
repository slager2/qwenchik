package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/sandbox"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

type sandboxRunnerStub struct {
	image     bool
	args      []string
	commands  int
	writeFile bool
}

func (s *sandboxRunnerStub) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "context" {
		return []byte("unix:///run/user/1000/docker.sock"), nil
	}
	if len(args) > 0 && args[0] == "info" && s.image {
		return []byte(`["name=rootless"]`), nil
	}
	if len(args) > 0 && args[0] == "info" {
		return []byte(`["name=seccomp"]`), nil
	}
	if len(args) > 0 && args[0] == "image" && s.image {
		return []byte("sha256:local"), nil
	}
	return nil, errors.New("sandbox image is unavailable")
}

func (s *sandboxRunnerStub) Run(_ context.Context, _ string, args []string, stdout, stderr io.Writer) error {
	s.commands++
	s.args = append([]string(nil), args...)
	_, _ = io.WriteString(stdout, "container output")
	if s.writeFile {
		for index, argument := range args {
			if argument != "--mount" || index+1 >= len(args) {
				continue
			}
			for _, field := range strings.Split(args[index+1], ",") {
				if strings.HasPrefix(field, "src=") {
					return os.WriteFile(filepath.Join(strings.TrimPrefix(field, "src="), "generated.txt"), []byte("generated output"), 0o600)
				}
			}
		}
	}
	return nil
}

type sandboxRepoStub struct{}

func (sandboxRepoStub) SaveArtifact(_ context.Context, artifact domain.Artifact, _ string) (domain.Artifact, error) {
	return artifact, nil
}

func TestSandboxToolRunsOnlyThroughRootlessDocker(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := &sandboxRunnerStub{image: true, writeFile: true}
	runtime, err := sandbox.New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspace"), runner)
	if err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterSandboxTools(registry, SandboxTools{Runtime: runtime, Artifacts: artifactStore, Repository: sandboxRepoStub{}}); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(WithTaskID(context.Background(), "task_01"), "coder", "run_command_sandbox", json.RawMessage(`{"command":["sh","-c","printf safe"]}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["stdout"] != "container output" || runner.commands != 1 {
		t.Fatalf("unexpected sandbox result=%#v commands=%d", output, runner.commands)
	}
	files, ok := output["generated_files"].([]map[string]any)
	if !ok || len(files) != 1 || files[0]["path"] != "generated.txt" || files[0]["artifact_id"] == "" {
		t.Fatalf("generated sandbox file was not saved as an artifact: %#v", output["generated_files"])
	}
	arguments := strings.Join(runner.args, " ")
	if !strings.Contains(arguments, "--network=none") || !strings.Contains(arguments, "--read-only") || !strings.Contains(arguments, "--cap-drop=ALL") || strings.Contains(arguments, "docker.sock") {
		t.Fatalf("container arguments violate sandbox limits: %s", arguments)
	}
}

func TestSandboxToolFailsClosedWhenRootlessDockerUnavailable(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := &sandboxRunnerStub{image: false}
	runtime, err := sandbox.New("golang:1.23-alpine", filepath.Join(t.TempDir(), "workspace"), runner)
	if err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterSandboxTools(registry, SandboxTools{Runtime: runtime, Artifacts: artifactStore, Repository: sandboxRepoStub{}}); err != nil {
		t.Fatal(err)
	}
	_, err = registry.Execute(WithTaskID(context.Background(), "task_02"), "coder", "run_command_sandbox", json.RawMessage(`{"command":["touch","/tmp/host"]}`))
	if !errors.Is(err, sandbox.ErrSandboxUnavailable) {
		t.Fatalf("unavailable runtime error = %v", err)
	}
	if runner.commands != 0 {
		t.Fatalf("command runner invoked %d times without rootless daemon", runner.commands)
	}
}
