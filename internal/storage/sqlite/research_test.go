package sqlite

import (
	"context"
	"encoding/json"
	"errors"
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

func TestApprovalIsDurableIdempotentAndConsumedOnce(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "approval.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(ctx, domain.Task{ID: "approval-task", Title: "Confirm action"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	stage := domain.StageSynthesis
	if _, err := store.TransitionTask(ctx, task.ID, domain.StatusRunning, &stage, "", "", nil); err != nil {
		t.Fatal(err)
	}
	approval, err := store.CreateApproval(ctx, domain.Approval{
		TaskID: task.ID, ToolName: "external.submit", Arguments: json.RawMessage(`{"url":"https://example.com/submit","confirm":true}`),
		Target: "https://example.com/submit", RiskExplanation: "This submits a form.", ExpiresAt: time.Now().Add(time.Minute),
	}, map[string]any{"current_step": "step_1", "pending_approval": true})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := store.GetTask(ctx, task.ID)
	if err != nil || waiting.Status != domain.StatusWaitingApproval {
		t.Fatalf("task after approval request = %q, err=%v", waiting.Status, err)
	}
	duplicate, err := store.CreateApproval(ctx, domain.Approval{
		TaskID: task.ID, ToolName: "external.submit", Arguments: json.RawMessage(`{"confirm":true,"url":"https://example.com/submit"}`),
		Target: "https://example.com/submit", RiskExplanation: "Duplicate request.", ExpiresAt: time.Now().Add(time.Minute),
	}, map[string]any{"pending_approval": true})
	if err != nil || duplicate.ID != approval.ID {
		t.Fatalf("duplicate approval = %#v, err=%v", duplicate, err)
	}
	decided, err := store.DecideApproval(ctx, approval.ID, true, "local_user", "approve-once")
	if err != nil || decided.Status != domain.ApprovalApproved {
		t.Fatalf("approval decision = %#v, err=%v", decided, err)
	}
	retried, err := store.DecideApproval(ctx, approval.ID, true, "local_user", "approve-once")
	if err != nil || retried.ID != approval.ID {
		t.Fatalf("idempotent approval retry = %#v, err=%v", retried, err)
	}
	queued, err := store.GetTask(ctx, task.ID)
	if err != nil || queued.Status != domain.StatusQueued {
		t.Fatalf("task after approval = %q, err=%v", queued.Status, err)
	}
	grant, ok, err := store.ConsumeApproval(ctx, task.ID, "external.submit", json.RawMessage(`{"url":"https://example.com/submit","confirm":true}`))
	if err != nil || !ok || grant != approval.ID {
		t.Fatalf("consume approval grant=%q ok=%t err=%v", grant, ok, err)
	}
	if _, ok, err := store.ConsumeApproval(ctx, task.ID, "external.submit", json.RawMessage(`{"confirm":true,"url":"https://example.com/submit"}`)); ok || !errors.Is(err, ErrApprovalAlreadyConsumed) {
		t.Fatalf("approval replay result ok=%t err=%v", ok, err)
	}
}

func TestRejectedApprovalCancelsTaskAndExpiredApprovalFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "approval-expiry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"rejected-task", "expired-task"} {
		task, err := store.CreateTask(ctx, domain.Task{ID: id, Title: id}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.QueueTask(ctx, task.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.ClaimNext(ctx); err != nil {
			t.Fatal(err)
		}
		stage := domain.StageSynthesis
		if _, err := store.TransitionTask(ctx, task.ID, domain.StatusRunning, &stage, "", "", nil); err != nil {
			t.Fatal(err)
		}
		expires := time.Now().Add(time.Minute)
		if id == "expired-task" {
			expires = time.Now().Add(100 * time.Millisecond)
		}
		approval, err := store.CreateApproval(ctx, domain.Approval{TaskID: task.ID, ToolName: "external.submit", Arguments: json.RawMessage(`{"target":"value"}`), Target: "value", RiskExplanation: "external effect", ExpiresAt: expires}, map[string]any{"pending_approval": true})
		if err != nil {
			t.Fatal(err)
		}
		if id == "rejected-task" {
			if _, err := store.DecideApproval(ctx, approval.ID, false, "local_user", "reject-1"); err != nil {
				t.Fatal(err)
			}
			cancelled, err := store.GetTask(ctx, id)
			if err != nil || cancelled.Status != domain.StatusCancelled {
				t.Fatalf("rejected task status=%q err=%v", cancelled.Status, err)
			}
		} else {
			time.Sleep(150 * time.Millisecond)
			count, err := store.ExpireApprovals(ctx)
			if err != nil || count != 1 {
				t.Fatalf("expire approvals count=%d err=%v", count, err)
			}
			expired, err := store.GetTask(ctx, id)
			if err != nil || expired.Status != domain.StatusFailed {
				t.Fatalf("expired task status=%q err=%v", expired.Status, err)
			}
			approval, err = store.Approval(ctx, approval.ID)
			if err != nil || approval.Status != domain.ApprovalExpired {
				t.Fatalf("approval expiry status=%q err=%v", approval.Status, err)
			}
		}
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
