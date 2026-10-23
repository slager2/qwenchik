package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	ErrSandboxUnavailable = errors.New("sandbox_unavailable")
	ErrInvalidRequest     = errors.New("invalid sandbox request")
)

var (
	validImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
	validTask  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

type CommandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
	Run(context.Context, string, []string, io.Writer, io.Writer) error
}

type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	stdout, stderr := &boundedBuffer{max: 1 << 20}, &boundedBuffer{max: 64 << 10}
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.truncated || stderr.truncated {
		return nil, errors.New("sandbox runtime check output exceeded limit")
	}
	return []byte(stdout.String()), nil
}

func (ExecRunner) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

type Runtime struct {
	image         string
	workspaceRoot string
	runner        CommandRunner
	limits        Limits
}

type Limits struct {
	CPU          string
	Memory       string
	PIDs         int
	OutputBytes  int
	MaxFileCount int
	MaxFileBytes int64
}

type DoctorStatus struct {
	Available  bool   `json:"available"`
	Rootless   bool   `json:"rootless"`
	ImageReady bool   `json:"image_ready"`
	Message    string `json:"message"`
}

type Request struct {
	TaskID    string
	Command   []string
	Timeout   time.Duration
	Workspace string
}

type GeneratedFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Result struct {
	ExecutionID     string          `json:"execution_id"`
	ExitCode        int             `json:"exit_code"`
	Stdout          string          `json:"stdout"`
	Stderr          string          `json:"stderr"`
	OutputTruncated bool            `json:"output_truncated"`
	GeneratedFiles  []GeneratedFile `json:"generated_files"`
}

func New(image, workspaceRoot string, runner CommandRunner, configuredLimits ...Limits) (*Runtime, error) {
	if runtime.GOOS == "windows" {
		if runner == nil {
			runner = ExecRunner{}
		}
		return &Runtime{image: image, workspaceRoot: workspaceRoot, runner: runner}, nil
	}
	if image == "" {
		image = "golang:1.23-alpine"
	}
	if !validImage.MatchString(image) {
		return nil, errors.New("SANDBOX_IMAGE is not a valid image reference")
	}
	if workspaceRoot == "" {
		return nil, errors.New("WORKSPACE_ROOT is required")
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	if strings.ContainsAny(root, ",:") {
		return nil, errors.New("workspace root cannot contain comma or colon characters")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	limits := Limits{CPU: "1", Memory: "512m", PIDs: 64, OutputBytes: 64 << 10, MaxFileCount: 100, MaxFileBytes: 10 << 20}
	if len(configuredLimits) > 0 {
		limits = configuredLimits[0]
		if limits.CPU == "" {
			limits.CPU = "1"
		}
		if limits.Memory == "" {
			limits.Memory = "512m"
		}
		if limits.PIDs < 1 || limits.PIDs > 256 {
			limits.PIDs = 64
		}
		if limits.OutputBytes < 1 || limits.OutputBytes > 256<<10 {
			limits.OutputBytes = 64 << 10
		}
		if limits.MaxFileCount < 1 || limits.MaxFileCount > 1000 {
			limits.MaxFileCount = 100
		}
		if limits.MaxFileBytes < 1 || limits.MaxFileBytes > 100<<20 {
			limits.MaxFileBytes = 10 << 20
		}
	}
	return &Runtime{image: image, workspaceRoot: root, runner: runner, limits: limits}, nil
}

func (d *Runtime) Doctor(ctx context.Context) DoctorStatus {
	return d.check(ctx, true)
}

func (d *Runtime) check(ctx context.Context, smoke bool) DoctorStatus {
	if runtime.GOOS == "windows" {
		return DoctorStatus{Message: "sandbox runtime is unsupported on windows"}
	}
	checkTimeout := 10 * time.Second
	if smoke {
		checkTimeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	dockerHost := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if os.Getenv("DOCKER_CONTEXT") != "" {
		dockerHost = ""
	}
	if dockerHost == "" {
		endpoint, err := d.runner.Output(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err != nil {
			return DoctorStatus{Message: "Docker context endpoint could not be inspected"}
		}
		dockerHost = strings.Trim(strings.TrimSpace(string(endpoint)), "\"'")
	}
	if !isLocalUnixEndpoint(dockerHost) {
		return DoctorStatus{Message: "remote Docker contexts are not allowed for task sandboxes"}
	}
	output, err := d.runner.Output(ctx, "docker", "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return DoctorStatus{Message: "Docker daemon is unavailable"}
	}
	var securityOptions []string
	if err := json.Unmarshal(output, &securityOptions); err != nil {
		return DoctorStatus{Message: "Docker security options could not be read"}
	}
	rootless := false
	for _, option := range securityOptions {
		if strings.Contains(strings.ToLower(option), "rootless") {
			rootless = true
			break
		}
	}
	if !rootless {
		return DoctorStatus{Message: "Docker daemon is not running rootless"}
	}
	if _, err := d.runner.Output(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", d.image); err != nil {
		return DoctorStatus{Rootless: true, Message: "rootless Docker is available but the configured sandbox image is missing locally"}
	}
	if smoke {
		stdout, stderr := &boundedBuffer{max: 1024}, &boundedBuffer{max: 1024}
		args := []string{"run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit", "16", "--memory", "64m", "--cpus", "0.25", "--user", "0:0", "--tmpfs", "/tmp:rw,nosuid,size=8m", d.image, "true"}
		if err := d.runner.Run(ctx, "docker", args, stdout, stderr); err != nil {
			return DoctorStatus{Rootless: true, ImageReady: true, Message: "rootless Docker sandbox smoke command failed"}
		}
	}
	return DoctorStatus{Available: true, Rootless: true, ImageReady: true, Message: "rootless Docker and sandbox image are available"}
}

func isLocalUnixEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	return err == nil && parsed.Scheme == "unix" && parsed.Host == "" && filepath.IsAbs(parsed.Path) && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (d *Runtime) Workspace(taskID string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", ErrSandboxUnavailable
	}
	if !validTask.MatchString(taskID) {
		return "", errors.New("invalid task ID for sandbox workspace")
	}
	root, err := filepath.EvalSymlinks(d.workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	workspace := filepath.Join(root, taskID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(workspace)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("sandbox workspace must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("sandbox workspace escaped configured root")
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		return "", err
	}
	return resolved, nil
}

func (d *Runtime) CleanupTask(taskID string) error {
	if runtime.GOOS == "windows" {
		return ErrSandboxUnavailable
	}
	if !validTask.MatchString(taskID) {
		return errors.New("invalid task ID for sandbox cleanup")
	}
	root, err := filepath.EvalSymlinks(d.workspaceRoot)
	if err != nil {
		return err
	}
	workspace := filepath.Join(root, taskID)
	info, err := os.Lstat(workspace)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("refusing to clean a non-directory sandbox workspace")
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == "." || filepath.IsAbs(relative) {
		return errors.New("sandbox cleanup path escaped configured root")
	}
	return os.RemoveAll(resolved)
}

func (d *Runtime) ReadGeneratedFile(taskID, relativePath string) ([]byte, error) {
	workspace, err := d.Workspace(taskID)
	if err != nil {
		return nil, err
	}
	relativePath = filepath.Clean(filepath.FromSlash(relativePath))
	if relativePath == "." || filepath.IsAbs(relativePath) || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return nil, errors.New("invalid generated file path")
	}
	path := filepath.Join(workspace, relativePath)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > d.limits.MaxFileBytes {
		return nil, errors.New("generated file is not a bounded regular file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(workspace, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, errors.New("generated file escaped sandbox workspace")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, d.limits.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > d.limits.MaxFileBytes {
		return nil, errors.New("generated file exceeds size limit")
	}
	return content, nil
}

func (d *Runtime) Run(ctx context.Context, request Request) (Result, error) {
	if runtime.GOOS == "windows" {
		return Result{}, ErrSandboxUnavailable
	}
	if len(request.Command) == 0 || len(request.Command) > 64 {
		return Result{}, ErrInvalidRequest
	}
	totalArgs := 0
	for _, argument := range request.Command {
		if argument == "" || strings.ContainsRune(argument, '\x00') || len(argument) > 4096 {
			return Result{}, ErrInvalidRequest
		}
		totalArgs += len(argument)
	}
	if totalArgs > 16<<10 {
		return Result{}, ErrInvalidRequest
	}
	status := d.check(ctx, false)
	if !status.Available {
		return Result{}, fmt.Errorf("%w: %s", ErrSandboxUnavailable, status.Message)
	}
	workspace, err := d.Workspace(request.TaskID)
	if err != nil {
		return Result{}, err
	}
	if request.Workspace != "" && filepath.Clean(request.Workspace) != workspace {
		return Result{}, ErrInvalidRequest
	}
	timeout := request.Timeout
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 2 * time.Minute
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := &boundedBuffer{max: d.limits.OutputBytes}, &boundedBuffer{max: d.limits.OutputBytes}
	identifier, err := executionID()
	if err != nil {
		return Result{}, err
	}
	containerName := "qagent-" + strings.ReplaceAll(identifier, "-", "")
	containerDir, err := os.MkdirTemp("", "qagent-sandbox-")
	if err != nil {
		return Result{}, fmt.Errorf("create sandbox container metadata directory: %w", err)
	}
	defer os.RemoveAll(containerDir)
	if err := os.Chmod(containerDir, 0o700); err != nil {
		return Result{}, err
	}
	cidFile := filepath.Join(containerDir, "container.cid")
	args := []string{
		"run", "--rm", "--pull=never", "--cidfile", cidFile, "--network=none", "--read-only",
		"--name", containerName,
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit", strconv.Itoa(d.limits.PIDs),
		"--memory", d.limits.Memory, "--cpus", d.limits.CPU,
		"--ulimit", "nofile=256:256", "--user", "0:0",
		"--tmpfs", "/tmp:rw,nosuid,size=64m",
		"--mount", "type=bind,src=" + workspace + ",dst=/workspace,rw",
		"--workdir", "/workspace", d.image,
	}
	args = append(args, request.Command...)
	runErr := d.runner.Run(commandContext, "docker", args, stdout, stderr)
	if commandContext.Err() != nil {
		d.cleanupContainer(containerName)
		return Result{}, commandContext.Err()
	}
	result := Result{ExecutionID: identifier, ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(), OutputTruncated: stdout.truncated || stderr.truncated, GeneratedFiles: []GeneratedFile{}}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			d.cleanupContainer(containerName)
			return Result{}, fmt.Errorf("sandbox runner failed: %w", runErr)
		}
		containerID, _ := os.ReadFile(cidFile)
		if len(bytes.TrimSpace(containerID)) == 0 {
			d.cleanupContainer(containerName)
			return Result{}, fmt.Errorf("%w: Docker did not start the configured image", ErrSandboxUnavailable)
		}
		result.ExitCode = exitErr.ExitCode()
	}
	result.GeneratedFiles, err = d.generatedFiles(workspace)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (d *Runtime) cleanupContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.runner.Run(ctx, "docker", []string{"rm", "--force", name}, io.Discard, io.Discard)
}

func (d *Runtime) generatedFiles(workspace string) ([]GeneratedFile, error) {
	var files []GeneratedFile
	var total int64
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if len(files) >= d.limits.MaxFileCount {
			return errors.New("sandbox generated file count exceeds limit")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > d.limits.MaxFileBytes || total+info.Size() > d.limits.MaxFileBytes {
			return errors.New("sandbox generated file bytes exceed limit")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		copied, copyErr := io.Copy(hash, io.LimitReader(file, d.limits.MaxFileBytes+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if copied != info.Size() {
			return errors.New("sandbox file size changed during collection")
		}
		relative, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		files = append(files, GeneratedFile{Path: filepath.ToSlash(relative), Size: copied, SHA256: hex.EncodeToString(hash.Sum(nil))})
		total += copied
		return nil
	})
	return files, err
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	max       int
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	remaining := b.max - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.buffer.Write(value[:remaining])
		b.truncated = true
		return len(value), nil
	}
	return b.buffer.Write(value)
}

func (b *boundedBuffer) String() string { return b.buffer.String() }

func executionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}
