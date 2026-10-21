package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func sourceFixture(taskID, suffix string) (domain.Artifact, domain.Artifact, domain.Evidence) {
	raw := domain.Artifact{ID: "raw-" + suffix, TaskID: taskID, Name: "source.raw", MIMEType: "text/html", SHA256: "raw-hash-" + suffix, SizeBytes: 100}
	text := domain.Artifact{ID: "text-" + suffix, TaskID: taskID, Name: "source.txt", MIMEType: "text/plain; charset=utf-8", SHA256: "text-hash-" + suffix, SizeBytes: 50}
	evidence := domain.Evidence{TaskID: taskID, Provider: "direct_http", OriginalURL: "https://example.org/" + suffix, CanonicalURL: "https://example.org/" + suffix, ContentSHA256: raw.SHA256, RawArtifactID: raw.ID, TextArtifactID: text.ID, Excerpt: "原文 example", QuoteSpans: []domain.QuoteSpan{{Start: 4, End: 14, Text: "原文 example", CoordinateSpace: "extracted_text_runes"}}, QualityFlags: []string{}, Taint: "untrusted_external_content"}
	return raw, text, evidence
}

func saveSource(ctx context.Context, s *Store, raw, text domain.Artifact, e domain.Evidence) (domain.Evidence, error) {
	return s.SaveResearchSource(ctx, raw, raw.TaskID+"/"+raw.ID, text, text.TaskID+"/"+text.ID, e)
}

func assertSourceCounts(t *testing.T, s *Store, taskID string, artifacts, evidence, events int) {
	t.Helper()
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM artifacts WHERE task_id=?`, artifacts},
		{`SELECT COUNT(*) FROM evidence WHERE task_id=?`, evidence},
		{`SELECT COUNT(*) FROM events WHERE task_id=? AND type IN ('artifact.created','evidence.created')`, events},
	} {
		var n int
		if err := s.db.QueryRow(check.query, taskID).Scan(&n); err != nil || n != check.want {
			t.Fatalf("%s = %d want %d: %v", check.query, n, check.want, err)
		}
	}
	if session, err := s.GetResearchSession(context.Background(), taskID); err != nil || session.Counters.Evidence != evidence {
		t.Fatalf("evidence counters: %#v %v", session.Counters, err)
	}
}

func TestSaveResearchSourceRollsBackEveryMetadataBoundary(t *testing.T) {
	for name, trigger := range map[string]string{
		"second artifact": `CREATE TRIGGER reject_source BEFORE INSERT ON artifacts WHEN NEW.mime_type='text/plain; charset=utf-8' BEGIN SELECT RAISE(ABORT,'injected'); END`,
		"evidence":        `CREATE TRIGGER reject_source BEFORE INSERT ON evidence BEGIN SELECT RAISE(ABORT,'injected'); END`,
		"artifact event":  `CREATE TRIGGER reject_source BEFORE INSERT ON events WHEN NEW.type='artifact.created' AND json_extract(NEW.payload_json,'$.mime_type')='text/plain; charset=utf-8' BEGIN SELECT RAISE(ABORT,'injected'); END`,
		"evidence event":  `CREATE TRIGGER reject_source BEFORE INSERT ON events WHEN NEW.type='evidence.created' BEGIN SELECT RAISE(ABORT,'injected'); END`,
		"counter":         `CREATE TRIGGER reject_source BEFORE UPDATE OF counters_json ON research_sessions BEGIN SELECT RAISE(ABORT,'injected'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			raw, text, e := sourceFixture(turn.Run.ID, "a")
			if _, err := s.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := saveSource(ctx, s, raw, text, e); err == nil {
				t.Fatal("injected failure accepted")
			}
			assertSourceCounts(t, s, e.TaskID, 0, 0, 0)
			if _, err := s.db.Exec(`DROP TRIGGER reject_source`); err != nil {
				t.Fatal(err)
			}
			if _, err := saveSource(ctx, s, raw, text, e); err != nil {
				t.Fatal(err)
			}
			assertSourceCounts(t, s, e.TaskID, 2, 1, 3)
		})
	}
}

func TestSaveResearchSourceDeduplicatesCanonicalRecordAtCap(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) { session.Budget.MaxEvidence = 1 })
	claimResearchFixture(t, s)
	raw, text, e := sourceFixture(turn.Run.ID, "a")
	first, err := saveSource(ctx, s, raw, text, e)
	if err != nil {
		t.Fatal(err)
	}
	e.ID, e.Title, e.Excerpt, e.QuoteSpans = "caller-id", "changed", "fabricated", nil
	second, err := saveSource(ctx, s, raw, text, e)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("dedupe must return persisted evidence: %#v %#v %v", first, second, err)
	}
	assertSourceCounts(t, s, e.TaskID, 2, 1, 3)
	raw, text, e = sourceFixture(turn.Run.ID, "b")
	if _, err := saveSource(ctx, s, raw, text, e); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("cap: %v", err)
	}
	assertSourceCounts(t, s, e.TaskID, 2, 1, 3)
}

func TestSaveResearchSourceRejectsInconsistentOwnership(t *testing.T) {
	for _, kind := range []string{"raw task", "text task", "raw id", "text id", "hash", "mime", "mime prefix", "malformed mime", "reference", "existing metadata", "dedupe artifacts"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, turn, _ := researchConversationFixture(t, nil)
			claimResearchFixture(t, s)
			raw, text, e := sourceFixture(turn.Run.ID, "a")
			rawRef, textRef := raw.TaskID+"/"+raw.ID, text.TaskID+"/"+text.ID
			wantArtifacts, wantEvidence, wantEvents := 0, 0, 0
			switch kind {
			case "raw task":
				raw.TaskID = "foreign"
			case "text task":
				text.TaskID = "foreign"
			case "raw id":
				e.RawArtifactID = "foreign"
			case "text id":
				e.TextArtifactID = "foreign"
			case "hash":
				e.ContentSHA256 = "wrong"
			case "mime":
				text.MIMEType = "application/pdf"
			case "mime prefix":
				text.MIMEType = "text/plain-invalid"
			case "malformed mime":
				text.MIMEType = "text/plain; charset"
			case "reference":
				textRef = "foreign/" + text.ID
			case "existing metadata":
				if _, err := s.SaveArtifact(ctx, text, textRef); err != nil {
					t.Fatal(err)
				}
				text.SHA256 = "wrong"
				wantArtifacts, wantEvents = 1, 1
			case "dedupe artifacts":
				if _, err := saveSource(ctx, s, raw, text, e); err != nil {
					t.Fatal(err)
				}
				text.ID, e.TextArtifactID, textRef = "different", "different", text.TaskID+"/different"
				wantArtifacts, wantEvidence, wantEvents = 2, 1, 3
			}
			if _, err := s.SaveResearchSource(ctx, raw, rawRef, text, textRef, e); err == nil {
				t.Fatal("inconsistent metadata accepted")
			}
			assertSourceCounts(t, s, turn.Run.ID, wantArtifacts, wantEvidence, wantEvents)
		})
	}
}

func TestSaveResearchSourceFencesStaleLease(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	if err := s.EnableWorkerLeases("source-worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	task := claimResearchFixture(t, s)
	raw, text, e := sourceFixture(turn.Run.ID, "a")
	leased := domain.WithExecutionLease(ctx, *task.Lease)
	if _, err := saveSource(ctx, s, raw, text, e); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("missing lease: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET tasklease_epoch=tasklease_epoch+1 WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := saveSource(leased, s, raw, text, e); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale lease: %v", err)
	}
	assertSourceCounts(t, s, e.TaskID, 0, 0, 0)
}

func TestSaveResearchSourceConcurrentCap(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, func(session *domain.ResearchSession) { session.Budget.MaxEvidence = 1 })
	claimResearchFixture(t, s)
	results := make(chan error, 6)
	for i := 0; i < cap(results); i++ {
		go func(i int) {
			raw, text, e := sourceFixture(turn.Run.ID, fmt.Sprint(i))
			_, err := saveSource(ctx, s, raw, text, e)
			results <- err
		}(i)
	}
	accepted := 0
	for i := 0; i < cap(results); i++ {
		if err := <-results; err == nil {
			accepted++
		} else if !errors.Is(err, ErrResearchBudgetExceeded) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d sources", accepted)
	}
	assertSourceCounts(t, s, turn.Run.ID, 2, 1, 3)
}
