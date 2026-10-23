package pdfextract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsPDFExtractionFailsClosedWithoutDockerOrPureGoFallback(t *testing.T) {
	runner := newFakeRunner()
	policy := DefaultPolicy()
	policy.AllowUnsandboxedFallback = true
	extractor, err := NewWithPolicy(fakeImageID, runner, policy)
	if err != nil {
		t.Fatal(err)
	}

	if err := extractor.Check(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Check error = %v, want ErrUnavailable", err)
	}
	if _, err := extractor.Extract(context.Background(), []byte("not a PDF")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Extract error = %v, want ErrUnavailable", err)
	}

	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, []byte("not a PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := extractor.ExtractFile(context.Background(), file); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ExtractFile error = %v, want ErrUnavailable", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractPureGoFile(context.Background(), file, info.Size(), policy); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("direct pure-Go fallback error = %v, want ErrUnavailable", err)
	}
	if code := runPureGoWorker(); code != 1 {
		t.Fatalf("pure-Go worker exit code = %d, want fail-closed code 1", code)
	}
	if len(runner.outputs) != 0 || len(runner.runs) != 0 {
		t.Fatalf("Windows PDF extraction invoked Docker: outputs=%v runs=%v", runner.outputs, runner.runs)
	}
}
