package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/sandbox"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

type SandboxRepository interface {
	SaveArtifact(context.Context, domain.Artifact, string) (domain.Artifact, error)
}

type SandboxTools struct {
	Runtime    *sandbox.Runtime
	Artifacts  *artifacts.Store
	Repository SandboxRepository
}

type commandArgs struct {
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

func RegisterSandboxTools(registry *Registry, runtime SandboxTools) error {
	if registry == nil || runtime.Runtime == nil || runtime.Artifacts == nil || runtime.Repository == nil {
		return errors.New("sandbox tool dependencies are incomplete")
	}
	registry.RegisterTaskCleanup(func(_ context.Context, taskID string) error { return runtime.Runtime.CleanupTask(taskID) })
	commandSchema := json.RawMessage(`{"type":"object","properties":{"command":{"type":"array","items":{"type":"string","minLength":1,"maxLength":4096},"minItems":1,"maxItems":64},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"required":["command"],"additionalProperties":false}`)
	outputSchema := json.RawMessage(`{"type":"object","properties":{"execution_id":{"type":"string"},"exit_code":{"type":"integer"},"stdout":{"type":"string"},"stderr":{"type":"string"},"output_truncated":{"type":"boolean"},"generated_files":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"artifact_id":{"type":"string"},"size":{"type":"integer"},"sha256":{"type":"string"}},"required":["path","artifact_id","size","sha256"],"additionalProperties":false}}},"additionalProperties":false}`)
	if err := registry.RegisterValidated(Descriptor{
		Name: "run_command_sandbox", Description: "Run an argv command in an isolated rootless Docker container with network disabled.",
		ArgumentsSchema: commandSchema, OutputSchema: outputSchema,
		RequiredCapability: "sandbox.execute", RiskClass: "sandbox_execute", TimeoutSeconds: 600,
		Idempotent: false, MaxOutputBytes: 256 << 10, AllowedRoles: []string{"coder"},
	}, validateCommandArgs, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args commandArgs
		if err := decodeToolArgs(raw, &args); err != nil {
			return nil, err
		}
		return runSandboxCommand(ctx, runtime, args)
	}); err != nil {
		return err
	}
	if err := registry.Register(Descriptor{
		Name: "run_tests_sandbox", Description: "Run `go test ./...` in the isolated task workspace with network disabled.",
		ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), OutputSchema: outputSchema,
		RequiredCapability: "sandbox.execute", RiskClass: "sandbox_execute", TimeoutSeconds: 600,
		Idempotent: true, MaxOutputBytes: 256 << 10, AllowedRoles: []string{"verifier"},
	}, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return runSandboxCommand(ctx, runtime, commandArgs{Command: []string{"go", "test", "./..."}, TimeoutSeconds: 600})
	}); err != nil {
		return err
	}
	return nil
}

func validateCommandArgs(ctx context.Context, raw json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var args commandArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return err
	}
	if len(args.Command) == 0 || len(args.Command) > 64 {
		return errors.New("sandbox command must contain 1 to 64 argv entries")
	}
	if args.TimeoutSeconds < 0 || args.TimeoutSeconds > 600 {
		return errors.New("sandbox command timeout must be at most 600 seconds")
	}
	return nil
}

func runSandboxCommand(ctx context.Context, runtime SandboxTools, args commandArgs) (any, error) {
	taskID := taskIDFromContext(ctx)
	if taskID == "" {
		return nil, errors.New("sandbox command requires a task context")
	}
	timeout := time.Duration(args.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	result, err := runtime.Runtime.Run(ctx, sandbox.Request{TaskID: taskID, Command: args.Command, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	files := make([]map[string]any, 0, len(result.GeneratedFiles))
	for _, generated := range result.GeneratedFiles {
		content, err := runtime.Runtime.ReadGeneratedFile(taskID, generated.Path)
		if err != nil {
			return nil, fmt.Errorf("read generated sandbox file: %w", err)
		}
		hash := sha256.Sum256(content)
		if hex.EncodeToString(hash[:]) != generated.SHA256 {
			return nil, errors.New("generated sandbox file changed before artifact save")
		}
		mimeType := mime.TypeByExtension(filepath.Ext(generated.Path))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		artifact, reference, err := runtime.Artifacts.Put(ctx, taskID, filepath.Base(generated.Path), mimeType, content)
		if err != nil {
			return nil, err
		}
		artifact, err = runtime.Repository.SaveArtifact(ctx, artifact, reference)
		if err != nil {
			return nil, err
		}
		files = append(files, map[string]any{"path": generated.Path, "artifact_id": artifact.ID, "size": artifact.SizeBytes, "sha256": artifact.SHA256})
	}
	return map[string]any{
		"execution_id": result.ExecutionID, "exit_code": result.ExitCode,
		"stdout": strings.TrimSpace(result.Stdout), "stderr": strings.TrimSpace(result.Stderr),
		"output_truncated": result.OutputTruncated, "generated_files": files,
	}, nil
}
