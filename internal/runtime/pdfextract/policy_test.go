package pdfextract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPolicyDefaultsAndValidation(t *testing.T) {
	p := DefaultPolicy()
	if p.MaxInputBytes != 67_108_864 || p.MaxPages != 20 || p.MaxTextRunes != 50_000 || p.ExtractionTimeout != 30*time.Second || p.MemoryBytes != 536_870_912 || p.TempBytes != 67_108_864 || p.CPUs != 1 || p.PIDs != 32 || p.AllowUnsandboxedFallback {
		t.Fatalf("defaults: %#v", p)
	}
	for name, mutate := range map[string]func(*Policy){
		"input":          func(p *Policy) { p.MaxInputBytes = 0 },
		"input overflow": func(p *Policy) { p.MaxInputBytes = math.MaxInt64 },
		"pages":          func(p *Policy) { p.MaxPages = -1 },
		"text":           func(p *Policy) { p.MaxTextRunes = 0 },
		"text overflow":  func(p *Policy) { p.MaxTextRunes = math.MaxInt },
		"timeout":        func(p *Policy) { p.ExtractionTimeout = 0 },
		"memory":         func(p *Policy) { p.MemoryBytes = 0 },
		"temp":           func(p *Policy) { p.TempBytes = 0 },
		"CPU zero":       func(p *Policy) { p.CPUs = 0 },
		"CPU nan":        func(p *Policy) { p.CPUs = math.NaN() },
		"CPU inf":        func(p *Policy) { p.CPUs = math.Inf(1) },
		"pids":           func(p *Policy) { p.PIDs = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			p := DefaultPolicy()
			mutate(&p)
			if _, err := NewWithPolicy("", nil, p); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func inputFile(t *testing.T, value []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "input-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.Write(value); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExtractFilePolicyBoundsOffsetOwnershipAndPermissions(t *testing.T) {
	requirePDFExecutionPlatform(t)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	p := DefaultPolicy()
	p.MaxInputBytes = 4
	runner := newFakeRunner()
	e, err := NewWithPolicy(fakeImageID, runner, p)
	if err != nil {
		t.Fatal(err)
	}
	f := inputFile(t, []byte("test"))
	if _, err := e.ExtractFile(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if string(runner.input) != "test" {
		t.Fatalf("input offset was not ignored: %q", runner.input)
	}
	if offset, err := f.Seek(0, io.SeekCurrent); err != nil || offset != 4 {
		t.Fatalf("borrowed file modified: offset=%d err=%v", offset, err)
	}
	if info, err := f.Stat(); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file closed or permissions modified: %v %v", info, err)
	}
	if _, err := e.ExtractFile(context.Background(), inputFile(t, []byte("extra"))); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized file: %v", err)
	}
	if len(runner.runs) != 1 {
		t.Fatal("oversized file reached Docker")
	}
	if _, err := e.ExtractFile(context.Background(), nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("nil input: %v", err)
	}
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, err := e.ExtractFile(context.Background(), dir); !errors.Is(err, ErrMalformed) {
		t.Fatalf("directory input: %v", err)
	}
}

func TestCustomPolicyRemovesLegacyPageTextAndResourceCaps(t *testing.T) {
	requirePDFExecutionPlatform(t)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	p := DefaultPolicy()
	p.MaxPages, p.MaxTextRunes = 25, 60_000
	p.MemoryBytes, p.TempBytes, p.CPUs, p.PIDs = 768<<20, 96<<20, 1.5, 48
	runner := newFakeRunner()
	runner.stdout = outputPrefix + "21\n" + strings.Repeat("😀", 50_080)
	e, err := NewWithPolicy(fakeImageID, runner, p)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.Extract(context.Background(), []byte("test"))
	if err != nil || result.PageCount != 21 {
		t.Fatalf("custom policy: %#v %v", result, err)
	}
	args := strings.Join(runner.runs[0], " ")
	for _, value := range []string{"--memory 805306368", "--cpus 1.5", "--pids-limit 48", "size=100663296", "QAGENT_PDF_MAX_PAGES=25"} {
		if !strings.Contains(args, value) {
			t.Errorf("missing %q in %s", value, args)
		}
	}
	for _, pages := range []int{1, 21} {
		p.MaxPages = pages
		p.MaxTextRunes = 3
		for _, tc := range []struct {
			output string
			want   error
		}{
			{outputPrefix + "1\nяяя", nil},
			{outputPrefix + "1\nяяяя", ErrTextTooLarge},
			{outputPrefix + "22\nabc", ErrTooManyPages},
		} {
			_, err := parseOutputWithPolicy([]byte(tc.output), p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("parse output error=%v want=%v", err, tc.want)
			}
		}
	}
}

func TestFileTimeoutAndParentCancellationAreDistinct(t *testing.T) {
	requirePDFExecutionPlatform(t)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	p := DefaultPolicy()
	p.ExtractionTimeout = 10 * time.Millisecond
	runner := newFakeRunner()
	runner.runHook = func(ctx context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, outputPrefix+"1\npartial")
		<-ctx.Done()
		return ctx.Err()
	}
	e, err := NewWithPolicy(fakeImageID, runner, p)
	if err != nil {
		t.Fatal(err)
	}
	f := inputFile(t, []byte("test"))
	result, err := e.ExtractFile(context.Background(), f)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) || result != (Result{}) || runner.cleanups != 1 {
		t.Fatalf("timeout: %#v %v cleanup=%d", result, err, runner.cleanups)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ExtractFile(ctx, f); !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Fatalf("parent cancellation: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if _, err := e.ExtractFile(ctx, f); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) {
		t.Fatalf("parent deadline: %v", err)
	}
}

func TestDockerTempQuotaRejectsInputWithoutLeakingResult(t *testing.T) {
	requirePDFExecutionPlatform(t)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	p := DefaultPolicy()
	p.TempBytes = 64 << 10
	runner := newFakeRunner()
	e, err := NewWithPolicy(fakeImageID, runner, p)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.ExtractFile(context.Background(), inputFile(t, []byte("x")))
	if !errors.Is(err, ErrResourceExceeded) || result != (Result{}) || len(runner.runs) != 0 {
		t.Fatalf("temp quota: %#v %v", result, err)
	}
}

func TestPureGoFileWorkerReceivesCustomPolicy(t *testing.T) {
	requirePDFExecutionPlatform(t)
	p := DefaultPolicy()
	p.AllowUnsandboxedFallback = true
	p.MaxPages, p.MaxTextRunes = 25, 100
	e, err := NewWithPolicy("", nil, p)
	if err != nil {
		t.Fatal(err)
	}
	f := inputFile(t, testPDF(21, "x"))
	result, err := e.ExtractFile(context.Background(), f)
	if err != nil || result.PageCount != 21 || result.Method != methodPureGo {
		t.Fatalf("custom helper pages: %#v %v", result, err)
	}
	p.MaxPages = 20
	e, _ = NewWithPolicy("", nil, p)
	if result, err := e.ExtractFile(context.Background(), f); !errors.Is(err, ErrTooManyPages) || result != (Result{}) {
		t.Fatalf("worker page limit: %#v %v", result, err)
	}
	p.MaxPages, p.MaxTextRunes = 25, 2
	e, _ = NewWithPolicy("", nil, p)
	if result, err := e.ExtractFile(context.Background(), f); !errors.Is(err, ErrTextTooLarge) || result != (Result{}) {
		t.Fatalf("worker text limit: %#v %v", result, err)
	}
	if _, err := parsePureGoReader(bytes.NewReader([]byte("x")), p.MaxInputBytes+1, p); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("reader byte limit: %v", err)
	}
}

func TestByteStagingUsesPrivatePermissions(t *testing.T) {
	requirePDFExecutionPlatform(t)
	dir, path, err := stageInput([]byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for path, mode := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permissions for %s: %v %v", path, info, err)
		}
	}
}

type countingInputRunner struct {
	*fakeRunner
	bytes int64
}

func (r *countingInputRunner) RunInput(ctx context.Context, name string, args []string, input io.Reader, stdout, stderr io.Writer) error {
	var err error
	r.bytes, err = io.Copy(io.Discard, input)
	if err != nil {
		return err
	}
	return r.Run(ctx, name, args, stdout, stderr)
}

func TestLargeFileTransportAndExactInputBoundary(t *testing.T) {
	requirePDFExecutionPlatform(t)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	for _, size := range []int64{40_534_667, 64 << 20} {
		p := DefaultPolicy()
		p.TempBytes = 65 << 20
		runner := &countingInputRunner{fakeRunner: newFakeRunner()}
		e, err := NewWithPolicy(fakeImageID, runner, p)
		if err != nil {
			t.Fatal(err)
		}
		f := inputFile(t, nil)
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		if _, err := e.ExtractFile(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		if runner.bytes != size {
			t.Fatalf("streamed %d want %d", runner.bytes, size)
		}
		if err := f.Truncate(p.MaxInputBytes + 1); err != nil {
			t.Fatal(err)
		}
		if _, err := e.ExtractFile(context.Background(), f); !errors.Is(err, ErrInputTooLarge) {
			t.Fatalf("limit+1: %v", err)
		}
		if len(runner.runs) != 1 {
			t.Fatal("oversized input reached container")
		}
	}
}

func TestPureGoReaderCustomTextBoundary(t *testing.T) {
	document := testPDF(1, "boundary")
	p := DefaultPolicy()
	result, err := parsePureGoReader(bytes.NewReader(document), int64(len(document)), p)
	if err != nil {
		t.Fatal(err)
	}
	p.MaxTextRunes = len([]rune(result.Text))
	if _, err := parsePureGoReader(bytes.NewReader(document), int64(len(document)), p); err != nil {
		t.Fatalf("exact boundary: %v", err)
	}
	p.MaxTextRunes--
	if result, err := parsePureGoReader(bytes.NewReader(document), int64(len(document)), p); !errors.Is(err, ErrTextTooLarge) || result != (Result{}) {
		t.Fatalf("boundary+1: %#v %v", result, err)
	}
}
