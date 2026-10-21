package pdfextract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

const fakeImageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeRunner struct {
	contextEndpoint string
	securityOptions string
	imageErr        error
	stdout          string
	stderr          string
	runErr          error
	runHook         func(context.Context, []string, io.Writer, io.Writer) error
	outputs         [][]string
	runs            [][]string
	cleanups        int
	input           []byte
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "docker" {
		return nil, errors.New("unexpected command")
	}
	f.outputs = append(f.outputs, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "context" {
		return []byte(f.contextEndpoint), nil
	}
	if len(args) > 0 && args[0] == "info" {
		return []byte(f.securityOptions), nil
	}
	if len(args) > 0 && args[0] == "image" {
		if f.imageErr != nil {
			return nil, f.imageErr
		}
		return []byte(fakeImageID), nil
	}
	return nil, errors.New("unexpected Docker inspect")
}

func (f *fakeRunner) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	if name != "docker" {
		return errors.New("unexpected command")
	}
	args = append([]string(nil), args...)
	f.runs = append(f.runs, args)
	if len(args) > 0 && args[0] == "rm" {
		f.cleanups++
		return nil
	}
	if f.runHook != nil {
		return f.runHook(ctx, args, stdout, stderr)
	}
	for index, arg := range args {
		if arg == "--mount" && index+1 < len(args) {
			mount := args[index+1]
			for _, part := range strings.Split(mount, ",") {
				if strings.HasPrefix(part, "src=") {
					f.input, _ = os.ReadFile(strings.TrimPrefix(part, "src="))
				}
			}
		}
	}
	if _, err := io.WriteString(stdout, f.stdout); err != nil {
		return err
	}
	if _, err := io.WriteString(stderr, f.stderr); err != nil {
		return err
	}
	return f.runErr
}

func (f *fakeRunner) RunInput(ctx context.Context, name string, args []string, input io.Reader, stdout, stderr io.Writer) error {
	var err error
	f.input, err = io.ReadAll(input)
	if err != nil {
		return err
	}
	return f.Run(ctx, name, args, stdout, stderr)
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		contextEndpoint: "unix:///run/user/1000/docker.sock",
		securityOptions: `["name=seccomp,profile=builtin","name=rootless"]`,
		stdout:          outputPrefix + "2\nExtracted PDF text.",
	}
}

func newFakeExtractor(t *testing.T, runner Runner) *Extractor {
	t.Helper()
	extractor, err := New(fakeImageID, runner)
	if err != nil {
		t.Fatal(err)
	}
	return extractor
}

func TestExtractUsesFixedRootlessLocalContainerAndReturnsCompleteOutput(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := newFakeRunner()
	extractor := newFakeExtractor(t, runner)
	input := []byte("not actually parsed by the fake container")
	result, err := extractor.Extract(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Extracted PDF text." || result.PageCount != 2 {
		t.Fatalf("unexpected extraction result: %#v", result)
	}
	if !bytes.Equal(runner.input, input) {
		t.Fatalf("read-only staged input = %q, want %q", runner.input, input)
	}
	if len(runner.runs) != 1 {
		t.Fatalf("Docker run calls = %d, want 1", len(runner.runs))
	}
	args := runner.runs[0]
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit 32", "--memory 536870912", "--cpus 1", "--user 10001:10001", "-i",
		"--tmpfs /tmp:rw,nosuid,nodev,noexec,size=67108864", "--env QAGENT_PDF_MAX_PAGES=20", "--env QAGENT_PDF_MAX_INPUT_BYTES=67108864",
		"--entrypoint /usr/local/bin/extract-pdf", fakeImageID,
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("Docker args missing %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"--cap-add", "--network=host", "--privileged", "sh -c", "--pull=always"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("Docker args contain forbidden command/option %q: %s", forbidden, joined)
		}
	}
	if args[len(args)-1] != fakeImageID {
		t.Fatalf("container has an unexpected command after image: %#v", args)
	}
	if runner.cleanups != 0 {
		t.Fatalf("successful extraction unexpectedly needed forced cleanup: %d", runner.cleanups)
	}
}

func TestExtractSharesOnePolicyDeadlineAcrossContainerWork(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := newFakeRunner()
	runner.runHook = func(ctx context.Context, _ []string, stdout, _ io.Writer) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("Docker run context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > ExtractionBudget {
			t.Fatalf("Docker run deadline remaining = %s, want at most %s", remaining, ExtractionBudget)
		}
		_, _ = io.WriteString(stdout, outputPrefix+"1\npartial")
		return context.DeadlineExceeded
	}
	result, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if result.Text != "" || result.PageCount != 0 {
		t.Fatalf("timeout returned partial evidence: %#v", result)
	}
	if runner.cleanups != 1 {
		t.Fatalf("cleanup calls after timeout = %d, want 1", runner.cleanups)
	}
}

func TestOutputFailureIsBoundedAndCleansContainer(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := newFakeRunner()
	runner.stdout = strings.Repeat("x", maxStdoutBytes+1)
	_, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf"))
	if !errors.Is(err, ErrOutputExceeded) {
		t.Fatalf("output limit error = %v", err)
	}
	if runner.cleanups != 1 {
		t.Fatalf("cleanup calls after output failure = %d, want 1", runner.cleanups)
	}
}

func TestStderrFailureIsBoundedAndCleansContainer(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := newFakeRunner()
	runner.stderr = strings.Repeat("diagnostic", maxStderrBytes)
	_, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf"))
	if !errors.Is(err, ErrOutputExceeded) {
		t.Fatalf("stderr limit error = %v", err)
	}
	if runner.cleanups != 1 {
		t.Fatalf("cleanup calls after stderr failure = %d, want 1", runner.cleanups)
	}
}

func TestExtractRejectsOversizedTextWithoutReturningPartialResult(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	runner := newFakeRunner()
	runner.stdout = outputPrefix + "1\n" + strings.Repeat("я", MaxTextRunes+1)
	result, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf"))
	if !errors.Is(err, ErrTextTooLarge) {
		t.Fatalf("text limit error = %v", err)
	}
	if result.Text != "" || result.PageCount != 0 {
		t.Fatalf("oversized output leaked partial evidence: %#v", result)
	}
}

func TestExtractRejectsPageCountAndMalformedOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   error
	}{
		{name: "too many pages", output: outputPrefix + "21\ntext", want: ErrTooManyPages},
		{name: "bad header", output: "partial evidence", want: ErrMalformed},
		{name: "invalid utf8", output: outputPrefix + "1\n\xff", want: ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DOCKER_HOST", "")
			t.Setenv("DOCKER_CONTEXT", "")
			runner := newFakeRunner()
			runner.stdout = test.output
			result, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf"))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if result.Text != "" || result.PageCount != 0 {
				t.Fatalf("invalid output leaked partial evidence: %#v", result)
			}
		})
	}
}

func TestExtractFailsClosedWithoutRootlessLocalDockerOrImage(t *testing.T) {
	t.Run("no configured image", func(t *testing.T) {
		extractor, err := New("", newFakeRunner())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := extractor.Extract(context.Background(), []byte("pdf")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("no-image error = %v, want unavailable", err)
		}
	})
	t.Run("remote Docker host", func(t *testing.T) {
		t.Setenv("DOCKER_CONTEXT", "")
		t.Setenv("DOCKER_HOST", "tcp://docker.example:2376")
		runner := newFakeRunner()
		if _, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("remote Docker error = %v, want unavailable", err)
		}
		if len(runner.outputs) != 0 || len(runner.runs) != 0 {
			t.Fatalf("remote context reached Docker: outputs=%v runs=%v", runner.outputs, runner.runs)
		}
	})
	t.Run("remote Docker context", func(t *testing.T) {
		t.Setenv("DOCKER_CONTEXT", "remote")
		t.Setenv("DOCKER_HOST", "")
		runner := newFakeRunner()
		runner.contextEndpoint = "tcp://docker.example:2376"
		if _, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("remote-context error = %v, want unavailable", err)
		}
		if len(runner.outputs) != 1 || len(runner.runs) != 0 {
			t.Fatalf("remote context was used: outputs=%v runs=%v", runner.outputs, runner.runs)
		}
	})
	t.Run("non-rootless Docker", func(t *testing.T) {
		t.Setenv("DOCKER_CONTEXT", "")
		t.Setenv("DOCKER_HOST", "")
		runner := newFakeRunner()
		runner.securityOptions = `["name=seccomp,profile=builtin"]`
		if _, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("non-rootless error = %v, want unavailable", err)
		}
		if len(runner.runs) != 0 {
			t.Fatalf("non-rootless Docker ran a container: %v", runner.runs)
		}
	})
	t.Run("missing image", func(t *testing.T) {
		t.Setenv("DOCKER_CONTEXT", "")
		t.Setenv("DOCKER_HOST", "")
		runner := newFakeRunner()
		runner.imageErr = errors.New("image not found")
		if _, err := newFakeExtractor(t, runner).Extract(context.Background(), []byte("pdf")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("missing-image error = %v, want unavailable", err)
		}
		if len(runner.runs) != 0 {
			t.Fatalf("missing image ran a container: %v", runner.runs)
		}
	})
}

func TestExtractRejectsInputAbovePolicyBeforeDocker(t *testing.T) {
	runner := newFakeRunner()
	input := make([]byte, MaxInputBytes+1)
	_, err := newFakeExtractor(t, runner).Extract(context.Background(), input)
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("input size error = %v", err)
	}
	if len(runner.outputs) != 0 || len(runner.runs) != 0 {
		t.Fatalf("oversized input reached Docker: outputs=%v runs=%v", runner.outputs, runner.runs)
	}
}

func TestImageReferenceMustBeImmutable(t *testing.T) {
	for _, image := range []string{
		fakeImageID,
		"alpine@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"registry.example:5000/team/pdf@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if err := ValidateImageReference(image); err != nil {
			t.Errorf("immutable image %q rejected: %v", image, err)
		}
	}
	for _, image := range []string{"", "alpine:latest", "alpine", "--network=host", "alpine@sha256:bad"} {
		if err := ValidateImageReference(image); image != "" && err == nil {
			t.Errorf("mutable/invalid image %q accepted", image)
		}
	}
}

func TestWrapperUsesFixedPopplerCommandsAndRejectsUnsafeInputs(t *testing.T) {
	script, err := os.ReadFile("extract-pdf")
	if err != nil {
		t.Fatal(err)
	}
	value := string(script)
	for _, required := range []string{
		"pdfinfo \"$input\"", "Encrypted:", "\"$pages\" -gt \"$QAGENT_PDF_MAX_PAGES\"", "pdftotext -enc UTF-8 -nopgbrk \"$input\" -",
		"QAGENT_PDF_PAGES:%s\\n", "umask 077", "cat >\"$input\"",
	} {
		if !strings.Contains(value, required) {
			t.Errorf("fixed wrapper missing %q", required)
		}
	}
	if strings.Contains(value, `"$@"`) || strings.Contains(value, "eval ") || strings.Contains(value, "sh -c") {
		t.Fatal("wrapper interpolates arguments or evaluates external content")
	}
}
