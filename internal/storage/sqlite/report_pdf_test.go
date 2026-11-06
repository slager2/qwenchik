package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestSaveReportPDFArtifactOnlyForTerminalResearchRuns(t *testing.T) {
	ctx := context.Background()
	store, turn, _ := researchConversationFixture(t, nil)
	artifact := domain.Artifact{ID: strings.Repeat("c", 64), TaskID: turn.Run.ID, Name: "research_report-0123456789abcdef.pdf", MIMEType: "application/pdf", SHA256: strings.Repeat("d", 64), SizeBytes: 2048}
	ref := filepath.Join(turn.Run.ID, artifact.ID)
	if _, err := store.SaveReportPDFArtifact(ctx, artifact, ref); err != ErrConflict {
		t.Fatalf("running task accepted a PDF cache artifact: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET status='completed' WHERE id=?`, turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, turn.Run.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for repeat := 0; repeat < 2; repeat++ {
		if _, err := store.SaveReportPDFArtifact(ctx, artifact, ref); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.Artifacts(ctx, turn.Run.ID)
	if err != nil || len(items) != 1 || items[0].Name != artifact.Name {
		t.Fatalf("PDF artifact not recorded once: %+v %v", items, err)
	}
	var after int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, turn.Run.ID).Scan(&after); err != nil || after != before {
		t.Fatalf("PDF cache appended events: before=%d after=%d err=%v", before, after, err)
	}
	for _, bad := range []domain.Artifact{
		{ID: artifact.ID, TaskID: artifact.TaskID, Name: "evil.pdf", MIMEType: "application/pdf", SHA256: artifact.SHA256, SizeBytes: 10},
		{ID: artifact.ID, TaskID: artifact.TaskID, Name: artifact.Name, MIMEType: "text/html", SHA256: artifact.SHA256, SizeBytes: 10},
		{ID: artifact.ID, TaskID: artifact.TaskID, Name: artifact.Name, MIMEType: "application/pdf", SHA256: strings.Repeat("e", 64), SizeBytes: 2048},
	} {
		if _, err := store.SaveReportPDFArtifact(ctx, bad, ref); err == nil {
			t.Fatalf("accepted invalid PDF artifact %+v", bad)
		}
	}
	if _, err := store.SaveReportPDFArtifact(ctx, artifact, "../escape"); err == nil {
		t.Fatal("accepted unsafe storage reference")
	}
}

func TestPendingReportPDFAutosaves(t *testing.T) {
	ctx := context.Background()
	store, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) { session.Profile = domain.ResearchProfileOvernight })
	since := time.Now().Add(-time.Minute)
	if items, err := store.PendingReportPDFAutosaves(ctx, since, 10); err != nil || len(items) != 0 {
		t.Fatalf("running task listed: %+v %v", items, err)
	}
	started, completed := time.Now().Add(-30*time.Minute).UTC(), time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE tasks SET status='completed',started_at=?,completed_at=? WHERE id=?`, stamp(started), stamp(completed), turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, turn.Run.ID, "task.completed", nil, "user", nil); err != nil {
		t.Fatal(err)
	}
	if items, err := store.PendingReportPDFAutosaves(ctx, time.Now().Add(time.Hour), 10); err != nil || len(items) != 0 {
		t.Fatalf("completion before the watermark listed: %+v %v", items, err)
	}
	items, err := store.PendingReportPDFAutosaves(ctx, since, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("completed research not listed: %+v %v", items, err)
	}
	item := items[0]
	if item.TaskID != turn.Run.ID || item.Profile != domain.ResearchProfileOvernight || item.CompletedAt.Sub(item.StartedAt) < 29*time.Minute || item.Title == "" {
		t.Fatalf("unexpected candidate %+v", item)
	}
	if err := store.RecordReportPDFAutosave(ctx, turn.Run.ID, "task.completed", nil); err == nil {
		t.Fatal("accepted a foreign event type")
	}
	if err := store.RecordReportPDFAutosave(ctx, turn.Run.ID, EventReportPDFAutosaved, map[string]any{"file_name": "x.pdf"}); err != nil {
		t.Fatal(err)
	}
	if items, err := store.PendingReportPDFAutosaves(ctx, since, 10); err != nil || len(items) != 0 {
		t.Fatalf("handled run listed again: %+v %v", items, err)
	}
}
