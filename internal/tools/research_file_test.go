package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

type fileFetchStub struct {
	doc     research.FetchedDocument
	options research.FetchOptions
	calls   int
}

func (f *fileFetchStub) ValidateURL(context.Context, string) error { return nil }
func (f *fileFetchStub) Fetch(_ context.Context, _ string, options research.FetchOptions) (research.FetchedDocument, error) {
	f.calls++
	f.options = options
	return f.doc, nil
}

type fileExtractorFunc func(context.Context, research.FetchedDocument) (research.ExtractedDocument, error)

func (f fileExtractorFunc) Extract(ctx context.Context, d research.FetchedDocument) (research.ExtractedDocument, error) {
	return f(ctx, d)
}

type fileResearchRepo struct {
	artifacts []domain.Artifact
	evidence  []domain.Evidence
}

func (r *fileResearchRepo) SaveArtifact(_ context.Context, a domain.Artifact, _ string) (domain.Artifact, error) {
	r.artifacts = append(r.artifacts, a)
	return a, nil
}
func (r *fileResearchRepo) SaveEvidence(_ context.Context, e domain.Evidence) (domain.Evidence, error) {
	e.ID = "evidence-file"
	r.evidence = append(r.evidence, e)
	return e, nil
}

func TestFetchToolLargeFileBackedPersistsFromStartAndAccountsBytes(t *testing.T) {
	const size int64 = 40_534_667
	file, err := os.CreateTemp(t.TempDir(), "pdf-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := io.WriteString(file, "%PDF-1.7\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, _ = io.Copy(h, io.NewSectionReader(file, 0, size))
	hash := hex.EncodeToString(h.Sum(nil))
	fetcher := &fileFetchStub{doc: research.FetchedDocument{File: file, SizeBytes: size, ContentSHA256: hash, OriginalURL: "https://example.org/report", FinalURL: "https://example.org/report", ContentType: "application/pdf", DeclaredBytes: ptrInt64(size), EffectiveLimitBytes: 64 << 20}}
	root := t.TempDir()
	store, err := artifacts.New(root, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fileResearchRepo{}
	registry := NewRegistry()
	pdfPolicy := pdfextract.DefaultPolicy()
	pdfPolicy.MaxPages = 25
	extractor := fileExtractorFunc(func(_ context.Context, d research.FetchedDocument) (research.ExtractedDocument, error) {
		if d.File == nil || d.Body != nil {
			t.Fatal("extractor received memory-backed input")
		}
		_, _ = d.File.Seek(size-1, io.SeekStart)
		return research.ExtractedDocument{Title: "PDF", Text: "Verified extracted text.", Method: "fake_pdf", PageCount: 25}, nil
	})
	if err := RegisterResearchTools(registry, ResearchTools{Fetcher: fetcher, Extractor: extractor, Artifacts: store, Repository: repo, PDFPolicy: pdfPolicy}); err != nil {
		t.Fatal(err)
	}
	descriptor := registry.byName["fetch_url"].descriptor
	if descriptor.TimeoutSeconds < 160 || !strings.Contains(string(descriptor.OutputSchema), `"maximum":25`) {
		t.Fatalf("policy descriptor: %#v", descriptor)
	}
	ctx := WithFetchByteLimit(WithTaskID(context.Background(), "task"), size)
	args := json.RawMessage(`{"url":"https://example.org/report"}`)
	if _, err := registry.Execute(ctx, "researcher", "fetch_url", args); !errors.Is(err, ErrUnknownTarget) || fetcher.calls != 0 {
		t.Fatalf("unapproved fetch reached executor: %v", err)
	}
	registry.RememberURLs("task", []research.SearchHit{{URL: "https://example.org/report"}})
	result, err := registry.Execute(ctx, "researcher", "fetch_url", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["fetched_bytes"] != size || fetcher.options.MaxBytes != size || fetcher.options.TaskID != "task" {
		t.Fatalf("byte accounting: %#v %#v", result, fetcher.options)
	}
	if len(repo.artifacts) != 2 || len(repo.evidence) != 1 || repo.artifacts[0].SizeBytes != size || repo.artifacts[0].SHA256 != hash {
		t.Fatalf("persistence: %#v %#v", repo.artifacts, repo.evidence)
	}
	raw, err := os.Open(filepath.Join(root, "task", repo.artifacts[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	magic := make([]byte, 5)
	_, _ = io.ReadFull(raw, magic)
	if string(magic) != "%PDF-" {
		t.Fatal("artifact copied from extractor's ending offset")
	}
	if _, err := os.Stat(file.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary PDF leaked: %v", err)
	}
}

func ptrInt64(n int64) *int64 { return &n }

func TestFetchToolFileExtractionFailuresCleanupAndMetadata(t *testing.T) {
	for _, test := range []struct {
		cause error
		code  string
	}{
		{pdfextract.ErrInputTooLarge, "pdf_extraction_input_too_large"}, {pdfextract.ErrTooManyPages, "pdf_page_limit_exceeded"}, {pdfextract.ErrTextTooLarge, "pdf_text_limit_exceeded"}, {pdfextract.ErrResourceExceeded, "pdf_resource_exceeded"}, {pdfextract.ErrOutputExceeded, "pdf_resource_exceeded"}, {pdfextract.ErrTimeout, "pdf_timeout"}, {pdfextract.ErrUnavailable, "pdf_extractor_unavailable"}, {pdfextract.ErrMalformed, "pdf_malformed"}, {pdfextract.ErrEncrypted, "pdf_encrypted"}, {context.Canceled, ""},
	} {
		t.Run(test.cause.Error(), func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "pdf-")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(file, "%PDF-1.7")
			defer file.Close()
			fetcher := &fileFetchStub{doc: research.FetchedDocument{File: file, SizeBytes: 8, ContentType: "application/pdf", ContentSHA256: "hash", DeclaredBytes: ptrInt64(8), EffectiveLimitBytes: 1024}}
			root := t.TempDir()
			store, err := artifacts.New(root, 1024)
			if err != nil {
				t.Fatal(err)
			}
			repo := &fileResearchRepo{}
			registry := NewRegistry()
			ctx, cancel := context.WithCancel(WithTaskID(context.Background(), "task"))
			defer cancel()
			extractor := fileExtractorFunc(func(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
				if test.cause == context.Canceled {
					cancel()
				}
				return research.ExtractedDocument{}, test.cause
			})
			if err := RegisterResearchTools(registry, ResearchTools{Fetcher: fetcher, Extractor: extractor, Artifacts: store, Repository: repo}); err != nil {
				t.Fatal(err)
			}
			registry.RememberURLs("task", []research.SearchHit{{URL: "https://example.org/report"}})
			_, err = registry.Execute(ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://example.org/report"}`))
			var failure *research.FetchFailure
			if !errors.Is(err, test.cause) || !errors.As(err, &failure) || failure.Code != test.code || failure.FetchedBytes != 8 || failure.Phase != "extract" || failure.DeclaredBytes == nil || *failure.DeclaredBytes != 8 || failure.EffectiveLimitBytes != 1024 || failure.MIMEType != "application/pdf" {
				t.Fatalf("metadata: %#v %v", failure, err)
			}
			if len(repo.artifacts) != 0 || len(repo.evidence) != 0 {
				t.Fatal("failed extraction persisted evidence")
			}
			names, _ := os.ReadDir(root)
			if len(names) != 0 {
				t.Fatalf("failed extraction created artifacts: %v", names)
			}
			if _, err := os.Stat(file.Name()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temp leaked: %v", err)
			}
		})
	}
}
