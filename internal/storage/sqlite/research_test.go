package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestArtifactAndEvidencePersistenceDeduplicatesByContent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateTask(ctx, domain.Task{ID: "research-task", Title: "Research"}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := store.SaveArtifact(ctx, domain.Artifact{ID: "raw-id", TaskID: "research-task", Name: "source.html", MIMEType: "text/html", SHA256: "raw-hash", SizeBytes: 50}, "research-task/raw-id")
	if err != nil {
		t.Fatal(err)
	}
	text, err := store.SaveArtifact(ctx, domain.Artifact{ID: "text-id", TaskID: "research-task", Name: "source.txt", MIMEType: "text/plain", SHA256: "text-hash", SizeBytes: 24}, "research-task/text-id")
	if err != nil {
		t.Fatal(err)
	}
	evidence := domain.Evidence{
		TaskID: "research-task", Provider: "direct_http", OriginalQuery: "q", OriginalURL: "https://example.com/",
		CanonicalURL: "https://example.com/", Title: "Example", RetrievedAt: time.Now().UTC(), HTTPStatus: 200,
		ContentSHA256: "source-hash", ExtractionMethod: "local_html_parser", RawArtifactID: raw.ID, TextArtifactID: text.ID,
		Excerpt: "A source excerpt.", QuoteSpans: []domain.QuoteSpan{{Start: 0, End: 17, Text: "A source excerpt."}},
		QualityFlags: []string{}, Taint: "untrusted_external_content",
	}
	first, err := store.SaveEvidence(ctx, evidence)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SaveEvidence(ctx, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("duplicate evidence IDs = %q and %q", first.ID, second.ID)
	}
	items, err := store.Evidence(ctx, "research-task")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Taint != "untrusted_external_content" || items[0].Excerpt != evidence.Excerpt || items[0].RawArtifactID != raw.ID {
		t.Fatalf("unexpected evidence readback: %#v", items)
	}
	events, err := store.EventsAfter(ctx, "research-task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == "evidence.created" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("evidence.created event count = %d, want 1", count)
	}
}

func TestArtifactReferenceRejectsTraversal(t *testing.T) {
	if safeArtifactReference("../outside/file") || safeArtifactReference("task/../../etc/passwd") || safeArtifactReference("/tmp/absolute") {
		t.Fatal("unsafe artifact path accepted")
	}
	if !safeArtifactReference("task-id/artifact_id") {
		t.Fatal("safe artifact reference was rejected")
	}
}
