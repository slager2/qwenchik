package research

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type repeatedByteReader byte

func (r repeatedByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func policyFetcher(t *testing.T, p FetchPolicy, body io.Reader, mime string, length int64) *SafeFetcher {
	t.Helper()
	f, err := NewSafeFetcherWithFetchPolicy(p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mime}}, ContentLength: length, Body: io.NopCloser(body), Request: req}, nil
	})
	return f
}

func smallFetchPolicy(t *testing.T) FetchPolicy {
	t.Helper()
	p := DefaultFetchPolicy()
	p.TempRoot = t.TempDir()
	p.SourceFetchBytes = 64
	p.PDFBytes = 1024
	p.TempTaskBytes = 2048
	p.TempGlobalBytes = 4096
	return p
}

func assertNoFetchTemps(t *testing.T, root string) {
	t.Helper()
	names, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(name.Name(), "fetch-") {
			t.Errorf("leaked temp: %s", name.Name())
		}
	}
}

func TestFetchPolicyValidation(t *testing.T) {
	p := DefaultFetchPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*FetchPolicy){func(p *FetchPolicy) { p.PDFBytes = 0 }, func(p *FetchPolicy) { p.SourceFetchBytes = p.TaskFetchBudgetBytes + 1 }, func(p *FetchPolicy) { p.TempTaskBytes = p.TempGlobalBytes + 1 }, func(p *FetchPolicy) { p.TempRoot = "" }, func(p *FetchPolicy) { p.SourceDownloadTimeout = 0 }, func(p *FetchPolicy) { p.PDFBytes = 1<<63 - 1 }} {
		q := p
		mutate(&q)
		if q.Validate() == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestFetchIncidentSizePDFFileBackedHashCleanup(t *testing.T) {
	const size int64 = 40_534_667
	p := DefaultFetchPolicy()
	p.TempRoot = t.TempDir()
	payload := func() io.Reader {
		return io.MultiReader(strings.NewReader("%PDF-1.7\n"), io.LimitReader(repeatedByteReader('x'), size-9))
	}
	f := policyFetcher(t, p, payload(), "application/pdf", size)
	d, err := f.Fetch(context.Background(), "https://8.8.8.8/report", FetchOptions{TaskID: "task-large"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Cleanup()
	if d.File == nil || d.Body != nil || d.ByteSize() != size {
		t.Fatalf("not file-backed: file=%v body=%d size=%d", d.File, len(d.Body), d.ByteSize())
	}
	info, err := d.File.Stat()
	if err != nil || info.Size() != size || info.Mode().Perm() != 0600 {
		t.Fatalf("file metadata: %v %v", info, err)
	}
	h := sha256.New()
	_, _ = io.Copy(h, payload())
	if d.ContentSHA256 != hex.EncodeToString(h.Sum(nil)) {
		t.Fatal("stream hash mismatch")
	}
	name := d.File.Name()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.Cleanup() }()
	}
	wg.Wait()
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup: %v", err)
	}
	assertNoFetchTemps(t, p.TempRoot)
}

func TestPDFHeaderRejectNoReadNoFileAndPrimarySourceCode(t *testing.T) {
	p := smallFetchPolicy(t)
	f := policyFetcher(t, p, bodyReaderFunc(func([]byte) (int, error) { t.Fatal("header rejection read body"); return 0, io.EOF }), "application/pdf", p.PDFBytes+1)
	_, err := f.Fetch(context.Background(), "https://8.8.8.8/report", FetchOptions{MaxBytes: 32})
	var failure *FetchFailure
	if !errors.As(err, &failure) || failure.Code != "pdf_source_too_large" || failure.FetchedBytes != 0 || failure.DeclaredBytes == nil || *failure.DeclaredBytes != 1025 || failure.EffectiveLimitBytes != 32 {
		t.Fatalf("failure: %#v %v", failure, err)
	}
	assertNoFetchTemps(t, p.TempRoot)
}

func TestPDFMagicAndTaskRemaining(t *testing.T) {
	for _, test := range []struct {
		name, mime, body, code string
		remaining              int64
	}{
		{"wrong MIME PDF", "text/plain", "%PDF-" + strings.Repeat("x", 100), "", 0},
		{"unknown MIME PDF", "application/octet-stream", "%PDF-" + strings.Repeat("x", 100), "", 0},
		{"fake PDF", "application/pdf", "<html>not a pdf</html>", "pdf_malformed", 0},
		{"remaining PDF", "application/pdf", "%PDF-" + strings.Repeat("x", 100), "task_fetch_budget_exceeded", 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := smallFetchPolicy(t)
			f := policyFetcher(t, p, strings.NewReader(test.body), test.mime, -1)
			d, err := f.Fetch(context.Background(), "https://8.8.8.8/doc", FetchOptions{MaxBytes: test.remaining})
			defer d.Cleanup()
			if test.code == "" {
				if err != nil || d.File == nil || d.ContentType != "application/pdf" {
					t.Fatalf("magic: %#v %v", d, err)
				}
			} else {
				var failure *FetchFailure
				if !errors.As(err, &failure) || failure.Code != test.code {
					t.Fatalf("failure: %#v %v", failure, err)
				}
			}
		})
	}
}

func TestPDFStreamOverflowGzipAndCancellationCleanup(t *testing.T) {
	for _, encoding := range []string{"", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			p := smallFetchPolicy(t)
			payload := "%PDF-" + strings.Repeat("x", 2048)
			var reader io.Reader = strings.NewReader(payload)
			if encoding == "gzip" {
				var compressed bytes.Buffer
				z := gzip.NewWriter(&compressed)
				_, _ = io.WriteString(z, payload)
				_ = z.Close()
				reader = bytes.NewReader(compressed.Bytes())
			}
			f := policyFetcher(t, p, reader, "application/pdf", -1)
			if encoding != "" {
				transport := f.client.Transport
				f.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					r, e := transport.RoundTrip(req)
					r.Header.Set("Content-Encoding", encoding)
					return r, e
				})
			}
			_, err := f.Fetch(context.Background(), "https://8.8.8.8/doc", FetchOptions{})
			var failure *FetchFailure
			if !errors.As(err, &failure) || failure.Code != "pdf_source_too_large" || failure.FetchedBytes != 1025 || failure.DeclaredBytes != nil {
				t.Fatalf("overflow: %#v %v", failure, err)
			}
			assertNoFetchTemps(t, p.TempRoot)
		})
	}
	p := smallFetchPolicy(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	f := policyFetcher(t, p, bodyReaderFunc(func(b []byte) (int, error) {
		calls++
		if calls == 1 {
			return copy(b, "%PDF-"+strings.Repeat("x", 507)), nil
		}
		cancel()
		return copy(b, "partial"), ctx.Err()
	}), "application/pdf", -1)
	_, err := f.Fetch(ctx, "https://8.8.8.8/doc", FetchOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	assertNoFetchTemps(t, p.TempRoot)
}

func TestFetchTempQuotasRecoveryAndSymlinks(t *testing.T) {
	p := smallFetchPolicy(t)
	p.TempTaskBytes = 1024
	p.TempGlobalBytes = 2048
	first, err := createFetchTemp(context.Background(), p, "one", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer first.cleanup()
	if _, err := createFetchTemp(context.Background(), p, "one", 1); err == nil {
		t.Fatal("task quota bypass")
	}
	second, err := createFetchTemp(context.Background(), p, "two", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer second.cleanup()
	if _, err := createFetchTemp(context.Background(), p, "three", 1); err == nil {
		t.Fatal("global quota bypass")
	}
	if _, err := NewSafeFetcherWithFetchPolicy(p, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.file.Name()); err != nil {
		t.Fatal("recovery removed active file")
	}
	stale := second.file.Name()
	_ = second.file.Close()
	if _, err := NewSafeFetcherWithFetchPolicy(p, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale not recovered: %v", err)
	}
	first.cleanup()
	second.cleanup()
	outside := t.TempDir()
	link := filepath.Join(p.TempRoot, "symlink")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	p.TempRoot = link
	if _, err := NewSafeFetcherWithFetchPolicy(p, nil, nil); err == nil {
		t.Fatal("symlink temp root accepted")
	}
}

func TestFetchSeparateSourceAndPDFTimeouts(t *testing.T) {
	for _, pdf := range []bool{false, true} {
		t.Run(map[bool]string{false: "source timeout", true: "PDF longer deadline"}[pdf], func(t *testing.T) {
			p := smallFetchPolicy(t)
			p.SourceFetchBytes = 1024
			p.SourceDownloadTimeout = 10 * time.Millisecond
			p.PDFDownloadTimeout = time.Second
			payload := strings.Repeat("x", 600)
			mime := "text/plain"
			if pdf {
				payload = "%PDF-" + payload[5:]
				mime = "application/pdf"
			}
			reader := strings.NewReader(payload)
			calls := 0
			f := policyFetcher(t, p, bodyReaderFunc(func(b []byte) (int, error) {
				calls++
				if calls > 1 {
					time.Sleep(30 * time.Millisecond)
				}
				return reader.Read(b)
			}), mime, -1)
			d, err := f.Fetch(context.Background(), "https://8.8.8.8/doc", FetchOptions{})
			defer d.Cleanup()
			if pdf {
				if err != nil || d.ByteSize() != 600 {
					t.Fatalf("PDF deadline: %v %d", err, d.ByteSize())
				}
			} else {
				var failure *FetchFailure
				if !errors.As(err, &failure) || failure.Code != "fetch_timeout" || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					t.Fatalf("timeout misclassified: %#v %v", failure, err)
				}
			}
		})
	}
}

func TestFetchTempConcurrentQuota(t *testing.T) {
	p := smallFetchPolicy(t)
	p.TempTaskBytes = 1024
	p.TempGlobalBytes = 1024
	var wg sync.WaitGroup
	var mu sync.Mutex
	var leases []*fetchTemp
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := createFetchTemp(context.Background(), p, "task", 1024)
			if err == nil {
				mu.Lock()
				leases = append(leases, lease)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	defer func() {
		for _, lease := range leases {
			lease.cleanup()
		}
	}()
	if len(leases) != 1 {
		t.Fatalf("quota admitted %d downloads", len(leases))
	}
}

func TestPDFExactLimitAndBoundedUnknownLength(t *testing.T) {
	for _, length := range []int64{-1, 1024} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			p := smallFetchPolicy(t)
			body := strings.NewReader("%PDF-" + strings.Repeat("x", 1019))
			f := policyFetcher(t, p, body, "application/pdf", length)
			d, err := f.Fetch(context.Background(), "https://8.8.8.8/doc", FetchOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Cleanup()
			if d.ByteSize() != 1024 || d.File == nil || body.Len() != 0 {
				t.Fatalf("exact limit: %d %v %d", d.ByteSize(), d.File, body.Len())
			}
			if (d.DeclaredBytes == nil) != (length < 0) {
				t.Fatalf("unknown length metadata: %v", d.DeclaredBytes)
			}
		})
	}
}

func TestFetchRecoveryPreservesUnownedFilesAndSymlinks(t *testing.T) {
	p := smallFetchPolicy(t)
	owned, err := createFetchTemp(context.Background(), p, "stale", 100)
	if err != nil {
		t.Fatal(err)
	}
	stale := owned.file.Name()
	_ = owned.file.Close()
	_ = owned.root.Close()
	foreign := filepath.Join(p.TempRoot, "keep-other-owner.tmp")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(p.TempRoot, fmt.Sprintf("fetch-%d-%s-%s", os.Geteuid(), strings.Repeat("a", 16), strings.Repeat("b", 32)))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSafeFetcherWithFetchPolicy(p, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale survived: %v", err)
	}
	for _, path := range []string{foreign, outside, link} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("recovery touched foreign file %s: %v", path, err)
		}
	}
}
