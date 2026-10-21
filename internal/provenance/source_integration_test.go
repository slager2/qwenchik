package provenance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type sourceFetcher struct{ body []byte }

func (sourceFetcher) ValidateURL(context.Context, string) error { return nil }

func (f sourceFetcher) Fetch(_ context.Context, url string, options research.FetchOptions) (research.FetchedDocument, error) {
	if options.MaxBytes > 0 && int64(len(f.body)) > options.MaxBytes {
		return research.FetchedDocument{}, research.ErrResponseTooBig
	}
	hash := sha256.Sum256(f.body)
	return research.FetchedDocument{OriginalURL: url, FinalURL: url, ContentType: "text/html", HTTPStatus: 200, ContentSHA256: hex.EncodeToString(hash[:]), Body: f.body}, nil
}

type sourceExtractorFunc func(context.Context, research.FetchedDocument) (research.ExtractedDocument, error)

func (f sourceExtractorFunc) Extract(ctx context.Context, doc research.FetchedDocument) (research.ExtractedDocument, error) {
	return f(ctx, doc)
}

type staleHashFetcher struct{ sourceFetcher }

func (f staleHashFetcher) Fetch(ctx context.Context, url string, options research.FetchOptions) (research.FetchedDocument, error) {
	doc, err := f.sourceFetcher.Fetch(ctx, url, options)
	doc.ContentSHA256 = "stale-fetcher-hash"
	return doc, err
}

type legacySourceRepository struct{ store *sqlite.Store }

func (r legacySourceRepository) SaveArtifact(ctx context.Context, a domain.Artifact, ref string) (domain.Artifact, error) {
	return r.store.SaveArtifact(ctx, a, ref)
}

func (r legacySourceRepository) SaveEvidence(ctx context.Context, e domain.Evidence) (domain.Evidence, error) {
	return r.store.SaveEvidence(ctx, e)
}

type failingSourceRepository struct {
	t      *testing.T
	called bool
}

var sourceDBFailure = errors.New("injected metadata transaction failure")

func (r *failingSourceRepository) SaveArtifact(context.Context, domain.Artifact, string) (domain.Artifact, error) {
	r.t.Fatal("atomic repository used legacy SaveArtifact")
	return domain.Artifact{}, sourceDBFailure
}

func (r *failingSourceRepository) SaveEvidence(context.Context, domain.Evidence) (domain.Evidence, error) {
	r.t.Fatal("atomic repository used legacy SaveEvidence")
	return domain.Evidence{}, sourceDBFailure
}

func (r *failingSourceRepository) SaveResearchSource(_ context.Context, raw domain.Artifact, rawRef string, text domain.Artifact, textRef string, e domain.Evidence) (domain.Evidence, error) {
	r.called = true
	if raw.ID == "" || text.ID == "" || e.RawArtifactID != raw.ID || e.TextArtifactID != text.ID || e.ContentSHA256 != raw.SHA256 || rawRef != raw.TaskID+"/"+raw.ID || textRef != text.TaskID+"/"+text.ID {
		r.t.Fatalf("evidence was not built from computed owned artifact IDs: %#v %#v %#v", raw, text, e)
	}
	return domain.Evidence{}, sourceDBFailure
}

func TestResearchSourceToolPersistsExactOwnedSource(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "atomic"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.CreateTask(ctx, domain.Task{ID: "source-task", Title: "Source"}, ""); err != nil {
				t.Fatal(err)
			}
			files, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 100000)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("<html><body><p>" + strings.Repeat("Introductory background. ", 300) + "</p><p>Протокол throughput &amp; recovery 東京 😀.</p><p>" + strings.Repeat("Other details. ", 300) + "</p></body></html>")
			var repo tools.ResearchRepository = db
			if legacy {
				repo = legacySourceRepository{db}
			}
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{Fetcher: staleHashFetcher{sourceFetcher{body}}, Extractor: research.NewHTMLExtractorWithPDF(30000, nil), Artifacts: files, Repository: repo}); err != nil {
				t.Fatal(err)
			}
			registry.RememberURLs("source-task", []research.SearchHit{{URL: "https://example.org/source"}})
			out, err := registry.Execute(tools.WithTaskID(ctx, "source-task"), "researcher", "fetch_url", json.RawMessage(`{"url":"https://example.org/source","original_query":"throughput recovery"}`))
			if err != nil {
				t.Fatal(err)
			}
			items, err := db.Evidence(ctx, "source-task")
			if err != nil || len(items) != 1 {
				t.Fatalf("evidence: %#v %v", items, err)
			}
			e := items[0]
			if out.(map[string]any)["evidence_id"] != e.ID {
				t.Fatal("tool did not return persisted evidence ID")
			}
			metadata, err := db.Artifacts(ctx, "source-task")
			if err != nil || len(metadata) != 2 {
				t.Fatalf("artifacts: %#v %v", metadata, err)
			}
			for _, a := range metadata {
				content, err := files.Read(ctx, "source-task", a.ID)
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(content)
				identity := sha256.Sum256([]byte(a.TaskID + "\x00" + a.MIMEType + "\x00" + a.SHA256))
				if a.SHA256 != hex.EncodeToString(hash[:]) || a.ID != hex.EncodeToString(identity[:]) || a.SizeBytes != int64(len(content)) {
					t.Fatalf("artifact metadata not content/MIME owned: %#v", a)
				}
				if a.ID == e.RawArtifactID {
					if string(content) != string(body) || e.ContentSHA256 != a.SHA256 {
						t.Fatal("raw source hash mismatch")
					}
				} else if a.ID == e.TextArtifactID {
					span := e.QuoteSpans[0]
					if span.Start == 0 || span.CoordinateSpace != "extracted_text_runes" || string([]rune(string(content))[span.Start:span.End]) != e.Excerpt || span.Text != e.Excerpt {
						t.Fatalf("persisted span mismatch: %#v", span)
					}
				} else {
					t.Fatalf("unreferenced artifact metadata: %#v", a)
				}
			}
		})
	}
}

func TestResearchSourceToolRejectsBeforeCreatingFilesOrMetadata(t *testing.T) {
	parseErr := errors.New("partial parse failed")
	for _, tc := range []struct {
		name     string
		text     string
		parseErr error
		maxBody  int64
		want     error
	}{
		{"partial parse", "Partial text must not be accepted.", parseErr, 100000, parseErr},
		{"empty parse", " \n\t", nil, 100000, research.ErrExtractionEmpty},
		{"body limit", "Must not reach extraction.", nil, 4, research.ErrResponseTooBig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.CreateTask(ctx, domain.Task{ID: "source-task", Title: "Source"}, ""); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "artifacts")
			files, err := artifacts.New(root, 100000)
			if err != nil {
				t.Fatal(err)
			}
			extractor := sourceExtractorFunc(func(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
				if tc.maxBody == 4 {
					t.Fatal("oversized body reached extraction")
				}
				return research.ExtractedDocument{Text: tc.text}, tc.parseErr
			})
			registry := tools.NewRegistry()
			if err := tools.RegisterResearchTools(registry, tools.ResearchTools{Fetcher: sourceFetcher{[]byte("raw source body")}, Extractor: extractor, Artifacts: files, Repository: db, MaxFetchBytes: tc.maxBody}); err != nil {
				t.Fatal(err)
			}
			registry.RememberURLs("source-task", []research.SearchHit{{URL: "https://example.org/source"}})
			if _, err := registry.Execute(tools.WithTaskID(ctx, "source-task"), "researcher", "fetch_url", json.RawMessage(`{"url":"https://example.org/source"}`)); !errors.Is(err, tc.want) {
				t.Fatalf("failure = %v, want %v", err, tc.want)
			}
			metadata, err := db.Artifacts(ctx, "source-task")
			if err != nil || len(metadata) != 0 {
				t.Fatalf("rejected source artifacts: %#v %v", metadata, err)
			}
			evidence, err := db.Evidence(ctx, "source-task")
			if err != nil || len(evidence) != 0 {
				t.Fatalf("rejected source evidence: %#v %v", evidence, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected source files: %#v %v", entries, err)
			}
		})
	}
}

func TestResearchSourceToolAtomicFailureRetainsOnlyImmutableFiles(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "artifacts")
	files, err := artifacts.New(root, 100000)
	if err != nil {
		t.Fatal(err)
	}
	repo := &failingSourceRepository{t: t}
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{Fetcher: sourceFetcher{[]byte("<html><body><p>Source content extracted successfully.</p></body></html>")}, Extractor: research.NewHTMLExtractorWithPDF(30000, nil), Artifacts: files, Repository: repo}); err != nil {
		t.Fatal(err)
	}
	registry.RememberURLs("source-task", []research.SearchHit{{URL: "https://example.org/source"}})
	if _, err := registry.Execute(tools.WithTaskID(ctx, "source-task"), "researcher", "fetch_url", json.RawMessage(`{"url":"https://example.org/source"}`)); !errors.Is(err, sourceDBFailure) || !repo.called {
		t.Fatalf("atomic metadata failure: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "source-task"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected two complete unreferenced immutable files after DB failure, got %d", count)
	}
}
