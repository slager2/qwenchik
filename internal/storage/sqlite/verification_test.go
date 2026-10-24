package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestCompletionPersistsOnlyOwnedVerifiedCitations(t *testing.T) {
	for _, status := range []string{"supported", "fallback_excerpts"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			store, turn := conversationFixture(t, "research")
			verifyConversationRun(t, store, turn.Run.ID)
			owned, e := store.SaveEvidence(ctx, domain.Evidence{ID: "owned", TaskID: turn.Run.ID, Provider: "searxng", OriginalURL: "https://example.org/source", CanonicalURL: "https://example.org/source", Title: "Canonical title", ContentSHA256: "hash", HTTPStatus: 200, Excerpt: "Fetched source evidence"})
			if e != nil {
				t.Fatal(e)
			}
			other, e := store.CreateTask(ctx, domain.Task{ID: "foreign", Title: "Foreign", Description: "Foreign"}, "")
			if e != nil {
				t.Fatal(e)
			}
			foreign, e := store.SaveEvidence(ctx, domain.Evidence{ID: "foreign-evidence", TaskID: other.ID, Provider: "searxng", OriginalURL: "https://example.org/foreign", CanonicalURL: "https://example.org/foreign", ContentSHA256: "other", HTTPStatus: 200, Excerpt: "Foreign evidence"})
			if e != nil {
				t.Fatal(e)
			}
			payload := map[string]any{"phase": "citation_verification", "status": status, "reason_code": "claim_not_supported", "method": "verbatim_excerpt_fallback", "claim_count": 1, "secret": "private-fixture", "citations": []map[string]string{{"evidence_id": owned.ID, "url": owned.CanonicalURL, "title": "Untrusted title"}, {"evidence_id": foreign.ID, "url": foreign.CanonicalURL}, {"evidence_id": owned.ID, "url": "https://user:private-fixture@example.org/?token=private-fixture"}}}
			if _, e = store.RecordEvent(ctx, turn.Run.ID, "verification.updated", nil, "user", payload); e != nil {
				t.Fatal(e)
			}
			if _, e = store.RecordEvent(ctx, turn.Run.ID, "verification.updated", nil, "user", map[string]any{"status": "started"}); e != nil {
				t.Fatal(e)
			}
			if _, e = store.CompleteTask(ctx, turn.Run.ID, "Verified answer", nil); e != nil {
				t.Fatal(e)
			}
			messages, e := store.ConversationMessages(ctx, turn.ConversationID)
			if e != nil {
				t.Fatal(e)
			}
			message := messages[1]
			encoded, _ := json.Marshal(message)
			if len(message.Citations) != 1 || !strings.Contains(string(encoded), "Canonical title") || strings.Contains(string(encoded), "private-fixture") || strings.Contains(string(encoded), "foreign-evidence") || strings.Contains(string(encoded), "Untrusted title") {
				t.Fatal(string(encoded))
			}
			verification, ok := message.Metadata["verification"].(map[string]any)
			if !ok || verification["status"] != status {
				t.Fatalf("metadata lost %#v", message.Metadata)
			}
			legacy, e := store.Messages(ctx, turn.Run.ID)
			if e != nil || len(legacy) != 1 || legacy[0].ID != message.ID || legacy[0].Content != message.Content {
				t.Fatalf("legacy projection %#v %v", legacy, e)
			}
		})
	}
}

func TestCommitVerificationPersistsPartialCriteriaAndRequiresOwnedEvidence(t *testing.T) {
	ctx := context.Background()
	store, turn := conversationFixture(t, "research")
	verifyConversationRun(t, store, turn.Run.ID)
	owned, err := store.SaveEvidence(ctx, domain.Evidence{
		ID: "owned-criterion-evidence", TaskID: turn.Run.ID, Provider: "searxng",
		OriginalURL: "https://example.org/source", CanonicalURL: "https://example.org/source",
		ContentSHA256: "source-hash", HTTPStatus: 200, Excerpt: "Fetched source evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	criteria := []domain.VerificationCriterionResult{
		{StepID: "fetch_1", Criterion: 0, Classification: "critical", Met: true, EvidenceIDs: []string{owned.ID}},
		{StepID: "fetch_1", Criterion: 1, Classification: "supplementary", Met: false},
	}
	foreign := append([]domain.VerificationCriterionResult(nil), criteria...)
	foreign[0].EvidenceIDs = []string{"not-owned"}
	if err := store.CommitVerification(ctx, turn.Run.ID, "PARTIAL", "Core requirements are supported.", nil, []string{"Supplementary publication date is unavailable."}, foreign); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("unowned criterion evidence accepted: %v", err)
	}
	if err := store.CommitVerification(ctx, turn.Run.ID, "PARTIAL", "Core requirements are supported.", nil, []string{"Supplementary publication date is unavailable."}, criteria); err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(ctx, turn.Run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Verdict              string                               `json:"verdict"`
		Missing              []string                             `json:"missing"`
		SupplementaryMissing []string                             `json:"supplementary_missing"`
		Criteria             []domain.VerificationCriterionResult `json:"criteria"`
	}
	for _, event := range events {
		if event.Type == "verification.completed" {
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if payload.Verdict != "PARTIAL" || len(payload.Missing) != 0 || len(payload.SupplementaryMissing) != 1 || len(payload.Criteria) != 2 || payload.Criteria[0].EvidenceIDs[0] != owned.ID {
		t.Fatalf("verification criteria event = %+v", payload)
	}
	if _, err := store.CompleteTask(ctx, turn.Run.ID, "Supported answer", nil); err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(ctx, turn.Run.ID)
	if err != nil || task.Status != domain.StatusCompleted || task.TerminalOutcome != "partial" {
		t.Fatalf("partial verification terminal outcome: task=%+v err=%v", task, err)
	}
}

func TestCreateConversationIdempotencyIsAtomicAndConcurrent(t *testing.T) {
	ctx := context.Background()
	store, _ := conversationFixture(t, "chat")
	c := domain.Conversation{Title: "Idempotent", Mode: "chat", ModelProfile: map[string]any{}, ResearchSettings: map[string]any{}}
	var wg sync.WaitGroup
	results := make(chan domain.Conversation, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, e := store.CreateConversationIdempotent(ctx, c, "create")
			if e != nil {
				failures <- e
				return
			}
			results <- created
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	id := ""
	for result := range results {
		if id != "" && id != result.ID {
			t.Fatal("duplicate conversation")
		}
		id = result.ID
	}
	c.Title = "Changed"
	if _, e := store.CreateConversationIdempotent(ctx, c, "create"); !errors.Is(e, ErrIdempotency) {
		t.Fatalf("conflicting creation %v", e)
	}
	if _, e := store.db.Exec(`CREATE TRIGGER fail_creation_key BEFORE INSERT ON idempotency_keys WHEN new.key='rollback-create' BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := store.CreateConversationIdempotent(ctx, c, "rollback-create"); e == nil {
		t.Fatal("expected rollback")
	}
	var count int
	if e := store.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE title='Changed'`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("creation leaked through rollback: %d %v", count, e)
	}
}

func TestVerificationMetadataRejectsUnsafeCitationAndUnknownDiagnostics(t *testing.T) {
	owned := []domain.Evidence{{ID: "owned", CanonicalURL: "https://example.org/source", Title: "Real title", HTTPStatus: 200, Excerpt: "Evidence"}}
	raw := json.RawMessage(`{"phase":"citation_verification","status":"supported","reason_code":"secret=private-fixture","method":"Bearer private-fixture","claim_count":9999,"citations":[{"evidence_id":"owned","url":"https://user:private-fixture@example.org/source"}]}`)
	metadata := SafeVerificationMetadata(raw, owned)
	encoded, _ := json.Marshal(metadata)
	if strings.Contains(string(encoded), "private-fixture") || len(metadata["citations"].([]map[string]string)) != 0 || metadata["claim_count"] != nil || metadata["reason_code"] != nil || metadata["method"] != nil {
		t.Fatal(string(encoded))
	}
}
