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
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/runtime/sandbox"
)

const (
	MaxInputBytes    = 64 << 20
	MaxTextRunes     = 50_000
	MaxPages         = 20
	ExtractionBudget = 30 * time.Second
	maxStdoutBytes   = MaxTextRunes*utf8.UTFMax + 256
	maxStderrBytes   = 16 << 10
)

const outputPrefix = "QAGENT_PDF_PAGES:"

const (
	workerModeEnv  = "QAGENT_PDFEXTRACT_WORKER"
	workerTokenEnv = "QAGENT_PDFEXTRACT_WORKER_TOKEN"
	workerMode     = "pure-go-v1"
	methodPoppler  = "isolated_poppler_pdfinfo_pdftotext"
	methodPureGo   = "pure_go_pdf_subprocess"
)

var (
	ErrUnavailable      = errors.New("isolated PDF extraction is unavailable")
	ErrMalformed        = errors.New("PDF is malformed or could not be extracted")
	ErrEncrypted        = errors.New("encrypted PDFs are not supported")
	ErrTooManyPages     = errors.New("PDF exceeds the configured page limit")
	ErrTextTooLarge     = errors.New("PDF text exceeds the configured character limit")
	ErrInputTooLarge    = errors.New("PDF input exceeds the configured byte limit")
	ErrResourceExceeded = errors.New("PDF extractor resource limit exceeded")
	ErrTimeout          = errors.New("PDF extraction timed out")
	ErrOutputExceeded   = errors.New("PDF extractor output exceeds its limit")
	ErrNoText           = errors.New("PDF contains no extractable text")

	imageIDPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	digestImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:=-]{0,254}@sha256:[a-f0-9]{64}$`)
)

type Runner = sandbox.CommandRunner

type InputRunner interface {
	Runner
	RunInput(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

// Result contains only fully validated extraction output.
type Result struct {
	Text      string
	PageCount int
	Method    string
}

type Extractor struct {
	image  string
	runner Runner
	policy Policy
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
	return NewWithPolicy(image, runner, DefaultPolicy())
}

func NewWithPolicy(image string, runner Runner, policy Policy) (*Extractor, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if err := ValidateImageReference(image); err != nil {
		return nil, err
	}
	if runner == nil {
		runner = inputExecRunner{}
	}
	return &Extractor{image: image, runner: runner, policy: policy}, nil
}

// Check verifies the configured image and a local rootless Docker endpoint without
// starting a container. It never pulls or builds an image.
func (e *Extractor) Check(ctx context.Context) error {
	if e == nil || e.image == "" {
		return ErrUnavailable
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("%w: isolated PDF extraction is unsupported on windows", ErrUnavailable)
	}
	if err := e.checkRuntime(ctx); err != nil {
		return err
	}
	return nil
}

func (e *Extractor) Extract(ctx context.Context, document []byte) (Result, error) {
	if e == nil {
		return Result{}, ErrUnavailable
	}
	if runtime.GOOS == "windows" {
		return Result{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if int64(len(document)) > e.policy.MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	runCtx, cancel := context.WithTimeout(ctx, e.policy.ExtractionTimeout)
	defer cancel()
	dir, path, err := stageInput(document)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	file, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()
	return e.extractFile(ctx, runCtx, file)
}

func (e *Extractor) ExtractFile(ctx context.Context, file *os.File) (Result, error) {
	if e == nil {
		return Result{}, ErrUnavailable
	}
	if runtime.GOOS == "windows" {
		return Result{}, ErrUnavailable
	}
	runCtx, cancel := context.WithTimeout(ctx, e.policy.ExtractionTimeout)
	defer cancel()
	return e.extractFile(ctx, runCtx, file)
}

func (e *Extractor) extractFile(parent, ctx context.Context, file *os.File) (Result, error) {
	if runtime.GOOS == "windows" {
		return Result{}, ErrUnavailable
	}
	if err := parent.Err(); err != nil {
		return Result{}, err
	}
	if file == nil {
		return Result{}, ErrMalformed
	}
	info, err := file.Stat()
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() {
		return Result{}, ErrMalformed
	}
	if info.Size() > e.policy.MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	var result Result
	if e.image == "" {
		err = ErrUnavailable
	} else {
		err = e.checkRuntime(ctx)
	}
	if err == nil {
		result, err = e.extractDocker(ctx, file, info.Size())
	}
	if errors.Is(err, ErrUnavailable) && e.policy.AllowUnsandboxedFallback && ctx.Err() == nil {
		result, err = extractPureGoFile(ctx, file, info.Size(), e.policy)
	}
	if parent.Err() != nil {
		return Result{}, parent.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return Result{}, errors.Join(ErrTimeout, context.DeadlineExceeded)
	}
	return result, err
}

func (e *Extractor) extractDocker(ctx context.Context, file *os.File, size int64) (Result, error) {
	const metadataReserve = 64 << 10
	if e.policy.TempBytes < metadataReserve || size > e.policy.TempBytes-metadataReserve {
		return Result{}, ErrResourceExceeded
	}
	inputRunner, ok := e.runner.(InputRunner)
	if !ok {
		return Result{}, ErrUnavailable
	}

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

	stdout := &limitedBuffer{limit: e.policy.stdoutBytes()}
	stderr := &limitedBuffer{limit: maxStderrBytes}
	args := []string{
		"run", "--rm", "-i", "--pull=never", "--cidfile", cidFile, "--name", containerName,
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit", strconv.Itoa(e.policy.PIDs), "--memory", strconv.FormatInt(e.policy.MemoryBytes, 10), "--cpus", strconv.FormatFloat(e.policy.CPUs, 'f', -1, 64),
		"--memory-swap", strconv.FormatInt(e.policy.MemoryBytes, 10),
		"--ulimit", "nofile=64:64", "--user", "10001:10001",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=" + strconv.FormatInt(e.policy.TempBytes, 10),
		"--env", "QAGENT_PDF_MAX_PAGES=" + strconv.Itoa(e.policy.MaxPages),
		"--env", "QAGENT_PDF_MAX_INPUT_BYTES=" + strconv.FormatInt(e.policy.MaxInputBytes, 10),
		"--entrypoint", "/usr/local/bin/extract-pdf",
		e.image,
	}

	runErr := inputRunner.RunInput(ctx, "docker", args, io.NewSectionReader(file, 0, size), stdout, stderr)
	runContextErr := ctx.Err()
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
			case 28, 137:
				return Result{}, ErrResourceExceeded
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

	result, err := parseOutputWithPolicy(stdout.Bytes(), e.policy)
	if err != nil {
		return Result{}, err
	}
	result.Method = methodPoppler
	return result, nil
}

func parseOutput(output []byte) (Result, error) {
	return parseOutputWithPolicy(output, DefaultPolicy())
}

func parseOutputWithPolicy(output []byte, policy Policy) (Result, error) {
	lineEnd := bytes.IndexByte(output, '\n')
	if lineEnd < 0 || !bytes.HasPrefix(output[:lineEnd], []byte(outputPrefix)) {
		return Result{}, ErrMalformed
	}
	pageValue := string(output[len(outputPrefix):lineEnd])
	pageCount, err := strconv.Atoi(pageValue)
	if err != nil || pageCount < 1 {
		return Result{}, ErrMalformed
	}
	if pageCount > policy.MaxPages {
		return Result{}, ErrTooManyPages
	}
	textBytes := output[lineEnd+1:]
	if !utf8.Valid(textBytes) {
		return Result{}, ErrMalformed
	}
	text, err := stripPDFControls(string(textBytes))
	if err != nil {
		return Result{}, err
	}
	if utf8.RuneCountInString(text) > policy.MaxTextRunes {
		return Result{}, ErrTextTooLarge
	}
	if strings.TrimSpace(text) == "" {
		return Result{}, ErrNoText
	}
	return Result{Text: text, PageCount: pageCount}, nil
}

// stripPDFControls preserves line-oriented whitespace while removing control
// characters that are not meaningful formatting. Invalid UTF-8 is malformed.
func stripPDFControls(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", ErrMalformed
	}
	var cleaned strings.Builder
	cleaned.Grow(len(value))
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			continue
		}
		if r == '\r' {
			r = '\n'
		}
		cleaned.WriteRune(r)
	}
	return cleaned.String(), nil
}

func (e *Extractor) checkRuntime(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("%w: isolated PDF extraction is unsupported on windows", ErrUnavailable)
	}
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
	if err := os.WriteFile(path, document, 0o600); err != nil {
		return "", "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
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

type inputExecRunner struct{ sandbox.ExecRunner }

func (inputExecRunner) RunInput(ctx context.Context, name string, args []string, input io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, stdout, stderr
	cmd.WaitDelay = time.Second
	return cmd.Run()
}
