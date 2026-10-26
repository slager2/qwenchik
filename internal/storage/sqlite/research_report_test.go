package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestReportArtifactsPersistAtEvidenceCeilingWithoutNewEvidence(t *testing.T) {
	ctx := context.Background()
	store, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) {
		session.Budget.MaxEvidence = 1
		session.Budget.MaxCitationSources = 1
	})
	verifyConversationRun(t, store, turn.Run.ID)
	if _, err := store.SaveEvidence(ctx, domain.Evidence{ID: "owned-source", TaskID: turn.Run.ID, Provider: "http", OriginalURL: "https://example.org", CanonicalURL: "https://example.org", ContentSHA256: strings.Repeat("a", 64), HTTPStatus: 200, Excerpt: "Owned evidence"}); err != nil {
		t.Fatal(err)
	}
	artifact := domain.Artifact{ID: "report-json", TaskID: turn.Run.ID, Name: "research_report.json", MIMEType: "application/vnd.qwenchik.research-report+json", SHA256: strings.Repeat("b", 64), SizeBytes: 50}
	for repeat := 0; repeat < 2; repeat++ {
		if _, err := store.SaveResearchReportArtifact(ctx, artifact, filepath.Join(turn.Run.ID, artifact.ID)); err != nil {
			t.Fatal(err)
		}
	}
	session, err := store.GetResearchSession(ctx, turn.Run.ID)
	if err != nil || session.Counters.Evidence != 1 {
		t.Fatalf("report spent evidence capacity: %+v %v", session.Counters, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE task_id=?`, turn.Run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("report artifact is not idempotent: count=%d err=%v", count, err)
	}
	if _, err := store.TransitionTask(ctx, turn.Run.ID, domain.StatusCancelled, nil, "task.cancelled", "user", nil); err != nil {
		t.Fatal(err)
	}
	artifact.ID = "cancelled-report"
	if _, err := store.SaveResearchReportArtifact(ctx, artifact, filepath.Join(turn.Run.ID, artifact.ID)); err == nil {
		t.Fatal("cancelled task persisted a report")
	}
}
