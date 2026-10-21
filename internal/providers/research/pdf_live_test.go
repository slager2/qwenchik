package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
)

func TestLivePDFPolicy(t *testing.T) {
	if os.Getenv("QAGENT_LIVE_PDF") != "1" {
		t.Skip("live PDF network test requires QAGENT_LIVE_PDF=1")
	}
	const sourceURL = "https://internat.msu.ru/wp-content/uploads/2026/10/cbm03_2026-101.pdf"
	const expectedBytes int64 = 40_534_667
	policy := DefaultFetchPolicy()
	policy.TempRoot = t.TempDir()
	if policy.PDFBytes != 64<<20 {
		t.Fatalf("PDF limit=%d, want 64 MiB", policy.PDFBytes)
	}
	fetcher, err := NewSafeFetcherWithFetchPolicy(policy, nil, nil)
	if err != nil {
		t.Fatalf("liveblocked: initialize secure fetcher: %v", err)
	}
	t.Cleanup(fetcher.client.CloseIdleConnections)
	baseline := livePDFTempNames(t, policy.TempRoot)
	var document FetchedDocument
	t.Cleanup(func() {
		var name string
		if document.File != nil {
			name = document.File.Name()
		}
		document.Cleanup()
		if name != "" {
			if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("download temp file was not removed: %v", err)
			}
			if _, err := document.File.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Errorf("download file was not closed: %v", err)
			}
		}
		if names := livePDFTempNames(t, policy.TempRoot); !reflect.DeepEqual(names, baseline) {
			t.Errorf("fetch temp cleanup mismatch: before=%v after=%v", baseline, names)
		} else {
			t.Logf("temp cleanup verified; retained baseline metadata entries=%d", len(baseline))
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 165*time.Second)
	defer cancel()
	ready := false
	t.Run("Download", func(t *testing.T) {
		started := time.Now()
		var err error
		document, err = fetcher.Fetch(ctx, sourceURL, FetchOptions{TaskID: "livePDF", MaxBytes: policy.TaskFetchBudgetBytes})
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("liveblocked: secure PDF download failed after %s: %v", elapsed, err)
		}
		t.Logf("actual network download elapsed=%s", elapsed)
		if document.File == nil || document.Body != nil {
			t.Fatal("PDF must be file-backed with no Body allocation")
		}
		info, err := document.File.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("download file mode=%s, want regular 0600", info.Mode())
		}
		if info.Size() != expectedBytes || document.SizeBytes != expectedBytes || document.ByteSize() != expectedBytes {
			t.Fatalf("actual PDF size: file=%d document=%d byte_size=%d, want %d", info.Size(), document.SizeBytes, document.ByteSize(), expectedBytes)
		}
		if document.EffectiveLimitBytes != policy.PDFBytes || document.ByteSize() > policy.PDFBytes {
			t.Fatalf("effective PDF limit=%d size=%d, want limit=%d", document.EffectiveLimitBytes, document.ByteSize(), policy.PDFBytes)
		}
		if document.ContentType != "application/pdf" || document.HTTPStatus != 200 {
			t.Fatalf("download MIME=%q status=%d", document.ContentType, document.HTTPStatus)
		}
		magic := make([]byte, 5)
		if _, err := document.File.ReadAt(magic, 0); err != nil || string(magic) != "%PDF-" {
			t.Fatalf("PDF magic validation failed: %v", err)
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.NewSectionReader(document.File, 0, info.Size()))
		if err != nil || n != expectedBytes {
			t.Fatalf("streaming hash bytes=%d error=%v", n, err)
		}
		actualHash := hex.EncodeToString(hash.Sum(nil))
		if actualHash != document.ContentSHA256 {
			t.Fatalf("download hash mismatch: computed=%s fetcher=%s", actualHash, document.ContentSHA256)
		}
		t.Logf("downloaded_bytes=%d sha256=%s effective_limit_bytes=%d file_mode=0600 magic=PDF body=nil", n, actualHash, document.EffectiveLimitBytes)
		ready = true
	})
	t.Run("RootlessExtractor", func(t *testing.T) {
		if !ready {
			t.Skip("rootless extraction not attempted: live download did not pass validation")
		}
		pdfPolicy := pdfextract.DefaultPolicy()
		if pdfPolicy.AllowUnsandboxedFallback {
			t.Fatal("live extraction requires unsandboxed fallback disabled")
		}
		image := strings.TrimSpace(os.Getenv("PDF_EXTRACTOR_IMAGE"))
		extractor, err := pdfextract.NewWithPolicy(image, nil, pdfPolicy)
		if err != nil {
			t.Fatalf("liveblocked: invalid isolated PDF extractor configuration: %v", err)
		}
		if err := extractor.Check(ctx); err != nil {
			if errors.Is(err, pdfextract.ErrUnavailable) {
				if image == "" {
					t.Skipf("rootless extraction unavailable: PDF_EXTRACTOR_IMAGE is unset; Check returned %v; download alone does not prove extraction", err)
				}
				t.Skipf("rootless extraction unavailable: configured immutable image/local rootless runtime failed Check: %v; download alone does not prove extraction", err)
			}
			t.Fatalf("liveblocked: isolated runtime check failed: %v", err)
		}
		t.Logf("runtime Check passed: local rootless Docker and immutable image=%s; unsandboxed_fallback=false", image)
		started := time.Now()
		result, err := extractor.ExtractFile(ctx, document.File)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("isolated PDF extraction failed after %s: %v", elapsed, err)
		}
		if result.Method != "isolated_poppler_pdfinfo_pdftotext" || result.PageCount < 1 || result.PageCount > pdfPolicy.MaxPages || strings.TrimSpace(result.Text) == "" || !utf8.ValidString(result.Text) || utf8.RuneCountInString(result.Text) > pdfPolicy.MaxTextRunes {
			t.Fatalf("invalid isolated extraction result: method=%q pages=%d text_runes=%d", result.Method, result.PageCount, utf8.RuneCountInString(result.Text))
		}
		if _, err := document.File.Stat(); err != nil {
			t.Fatalf("extractor closed borrowed input: %v", err)
		}
		t.Logf("isolated extraction passed: elapsed=%s method=%s pages=%d text_runes=%d", elapsed, result.Method, result.PageCount, utf8.RuneCountInString(result.Text))
	})
}

func livePDFTempNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("inspect live PDF temp cleanup: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
