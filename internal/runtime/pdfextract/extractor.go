package pdfextract

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/runtime/sandbox"
)

const (
	MaxInputBytes    = 5_000_000
	MaxTextRunes     = 50_000
	MaxPages         = 20
	ExtractionBudget = 5 * time.Second
	maxStdoutBytes   = MaxTextRunes*utf8.UTFMax + 256
	maxStderrBytes   = 16 << 10
)

const outputPrefix = "QAGENT_PDF_PAGES:"

var (
	ErrUnavailable    = errors.New("isolated PDF extraction is unavailable")
	ErrMalformed      = errors.New("PDF is malformed or could not be extracted")
	ErrEncrypted      = errors.New("encrypted PDFs are not supported")
	ErrTooManyPages   = errors.New("PDF exceeds the 20 page limit")
	ErrTextTooLarge   = errors.New("PDF text exceeds the 50000 character limit")
	ErrInputTooLarge  = errors.New("PDF input exceeds the 5 MB limit")
	ErrOutputExceeded = errors.New("PDF extractor output exceeds its limit")

	imageIDPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	digestImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:=-]{0,254}@sha256:[a-f0-9]{64}$`)
)

type Runner = sandbox.CommandRunner

// Result contains only fully validated extraction output.
type Result struct {
	Text      string
	PageCount int
}

type Extractor struct {
	image  string
	runner Runner
}

func ValidateImageReference(image string) error {
	if image == "" {
		return nil
	}
	if !imageIDPattern.MatchString(image) && !digestImagePattern.MatchString(image) {
		return errors.New("PDF_EXTRACTOR_IMAGE must be a local immutable sha256 image ID or digest reference")
	}
	return nil
}

func New(image string, runner Runner) (*Extractor, error) {
	if err := ValidateImageReference(image); err != nil {
		return nil, err
	}
	if runner == nil {
		runner = sandbox.ExecRunner{}
	}
	return &Extractor{image: image, runner: runner}, nil
}

// Check verifies the configured image and a local rootless Docker endpoint without
// starting a container. It never pulls or builds an image.
func (e *Extractor) Check(ctx context.Context) error {
	if e == nil || e.image == "" {
		return ErrUnavailable
	}
	if err := e.checkRuntime(ctx); err != nil {
		return err
	}
	return nil
}

func (e *Extractor) Extract(ctx context.Context, document []byte) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if len(document) > MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	if e == nil || e.image == "" {
		return Result{}, ErrUnavailable
	}
	if err := e.checkRuntime(ctx); err != nil {
		return Result{}, err
	}

	inputDir, inputPath, err := stageInput(document)
	if err != nil {
		return Result{}, fmt.Errorf("stage PDF input: %w", err)
	}
	defer os.RemoveAll(inputDir)

	identifier, err := randomID()
	if err != nil {
		return Result{}, err
	}
	containerName := "qagent-pdf-" + identifier
	cidDir, err := os.MkdirTemp("/tmp", "qagent-pdf-cid-")
	if err != nil {
		return Result{}, fmt.Errorf("create PDF container metadata directory: %w", err)
	}
	defer os.RemoveAll(cidDir)
	cidFile := filepath.Join(cidDir, "container.cid")

	stdout := &limitedBuffer{limit: maxStdoutBytes}
	stderr := &limitedBuffer{limit: maxStderrBytes}
	args := []string{
		"run", "--rm", "--pull=never", "--cidfile", cidFile, "--name", containerName,
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit", "16", "--memory", "128m", "--cpus", "0.50",
		"--ulimit", "nofile=64:64", "--user", "10001:10001",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=8m",
		"--mount", "type=bind,src=" + inputPath + ",dst=/input/document.pdf,readonly",
		"--entrypoint", "/usr/local/bin/extract-pdf",
		e.image,
	}

	runCtx, cancel := context.WithTimeout(ctx, ExtractionBudget)
	runErr := e.runner.Run(runCtx, "docker", args, stdout, stderr)
	runContextErr := runCtx.Err()
	cancel()
	if runContextErr != nil || stdout.exceeded || stderr.exceeded || runErr != nil {
		e.cleanupContainer(containerName)
		if runContextErr != nil {
			return Result{}, runContextErr
		}
		if stdout.exceeded || stderr.exceeded {
			return Result{}, ErrOutputExceeded
		}
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
			return Result{}, runErr
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			switch exitErr.ExitCode() {
			case 23:
				return Result{}, ErrEncrypted
			case 24:
				return Result{}, ErrTooManyPages
			case 125:
				return Result{}, ErrUnavailable
			}
		}
		return Result{}, ErrMalformed
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if stdout.exceeded || stderr.exceeded {
		e.cleanupContainer(containerName)
		return Result{}, ErrOutputExceeded
	}

	result, err := parseOutput(stdout.Bytes())
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func parseOutput(output []byte) (Result, error) {
	lineEnd := bytes.IndexByte(output, '\n')
	if lineEnd < 0 || !bytes.HasPrefix(output[:lineEnd], []byte(outputPrefix)) {
		return Result{}, ErrMalformed
	}
	pageValue := string(output[len(outputPrefix):lineEnd])
	pageCount, err := strconv.Atoi(pageValue)
	if err != nil || pageCount < 1 {
		return Result{}, ErrMalformed
	}
	if pageCount > MaxPages {
		return Result{}, ErrTooManyPages
	}
	textBytes := output[lineEnd+1:]
	if !utf8.Valid(textBytes) {
		return Result{}, ErrMalformed
	}
	text := string(textBytes)
	if utf8.RuneCountInString(text) > MaxTextRunes {
		return Result{}, ErrTextTooLarge
	}
	return Result{Text: text, PageCount: pageCount}, nil
}

func (e *Extractor) checkRuntime(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := checkCtx.Err(); err != nil {
		return err
	}

	dockerHost := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if os.Getenv("DOCKER_CONTEXT") != "" {
		dockerHost = ""
	}
	if dockerHost == "" {
		endpoint, err := e.runner.Output(checkCtx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err != nil {
			return unavailable(ctx, "Docker context endpoint could not be inspected")
		}
		dockerHost = strings.Trim(strings.TrimSpace(string(endpoint)), "\"'")
	}
	if !isLocalUnixEndpoint(dockerHost) {
		return fmt.Errorf("%w: remote Docker contexts are not allowed", ErrUnavailable)
	}

	securityJSON, err := e.runner.Output(checkCtx, "docker", "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return unavailable(ctx, "rootless Docker is unavailable")
	}
	var securityOptions []string
	if err := json.Unmarshal(securityJSON, &securityOptions); err != nil {
		return fmt.Errorf("%w: Docker security options could not be read", ErrUnavailable)
	}
	rootless := false
	for _, option := range securityOptions {
		if strings.Contains(strings.ToLower(option), "rootless") {
			rootless = true
			break
		}
	}
	if !rootless {
		return fmt.Errorf("%w: Docker daemon is not running rootless", ErrUnavailable)
	}

	imageID, err := e.runner.Output(checkCtx, "docker", "image", "inspect", "--format", "{{.Id}}", e.image)
	if err != nil || strings.TrimSpace(string(imageID)) == "" {
		return unavailable(ctx, "PDF extractor image is missing locally")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func unavailable(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrUnavailable, message)
}

func stageInput(document []byte) (directory, path string, err error) {
	directory, err = os.MkdirTemp("/tmp", "qagent-pdf-input-")
	if err != nil {
		return "", "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(directory)
		}
	}()
	if strings.ContainsAny(directory, ",:") {
		return "", "", errors.New("temporary PDF path contains Docker mount delimiters")
	}
	path = filepath.Join(directory, "document.pdf")
	if err := os.WriteFile(path, document, 0o444); err != nil {
		return "", "", err
	}
	if err := os.Chmod(path, 0o444); err != nil {
		return "", "", err
	}
	cleanup = false
	return directory, path, nil
}

func randomID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func isLocalUnixEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	return err == nil && parsed.Scheme == "unix" && parsed.Host == "" && filepath.IsAbs(parsed.Path) && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (e *Extractor) cleanupContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.runner.Run(ctx, "docker", []string{"rm", "--force", name}, io.Discard, io.Discard)
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, ErrOutputExceeded
	}
	if len(value) > remaining {
		_, _ = b.buffer.Write(value[:remaining])
		b.exceeded = true
		return remaining, ErrOutputExceeded
	}
	return b.buffer.Write(value)
}

func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

var _ Runner = sandbox.ExecRunner{}
