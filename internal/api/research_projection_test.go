package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestResearchEventsAndSourcesUseSafeNestedProjections(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	c, e := store.CreateConversation(ctx, domain.Conversation{Title: "Research", Mode: "research"})
	if e != nil {
		t.Fatal(e)
	}
	turn, e := store.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Research"}, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, claimed, err := store.ClaimNext(ctx); err != nil || !claimed {
		t.Fatalf("claim research fixture: %v %v", claimed, err)
	}
	owned, e := store.SaveEvidence(ctx, domain.Evidence{ID: "owned-evidence", TaskID: turn.Run.ID, CanonicalURL: "https://example.org/article", OriginalURL: "https://example.org/article", Title: "Actual title", Provider: "searxng", ContentSHA256: "owned-content", HTTPStatus: 200, Excerpt: "Actual fetched evidence", RetrievedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	other, e := store.CreateTask(ctx, domain.Task{ID: "foreign-task", Title: "Foreign", Description: "Foreign"}, "")
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := store.SaveEvidence(ctx, domain.Evidence{ID: "foreign-evidence", TaskID: other.ID, CanonicalURL: "https://foreign.example/page", OriginalURL: "https://foreign.example/page", Title: "Foreign", Provider: "searxng", ContentSHA256: "foreign-content", HTTPStatus: 200, Excerpt: "Foreign excerpt", RetrievedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	emit := func(kind string, payload map[string]any) {
		t.Helper()
		if _, e = store.RecordEvent(ctx, turn.Run.ID, kind, nil, "user", payload); e != nil {
			t.Fatal(e)
		}
	}
	fingerprint := sourceFingerprint(owned.CanonicalURL)
	for _, status := range []string{"discovered", "validated"} {
		emit("search.hit", map[string]any{"step_id": "search_1", "hit": research.SearchHitObservation{Fingerprint: fingerprint, URL: owned.CanonicalURL, Title: "Discovery title", Provider: "searxng", Status: status}, "api_key": "private-fixture"})
	}
	emit("tool.started", map[string]any{"tool": "fetch_url", "target_url": owned.CanonicalURL})
	emit("search.hit", map[string]any{"step_id": "search_1", "hit": research.SearchHitObservation{Fingerprint: sourceFingerprint("rejected"), URL: "https://user:private-fixture@example.org/private?token=private-fixture", Title: "private-fixture", Status: "rejected", ErrorCode: "url_rejected_ssrf"}})
	emit("search.hit", map[string]any{"hit": research.SearchHitObservation{Fingerprint: sourceFingerprint("failed"), URL: "https://example.org/failed", Status: "validated"}})
	emit("tool.failed", map[string]any{"tool": "fetch_url", "target_url": "https://example.org/failed", "error_code": "fetch_timeout"})
	emit("query.updated", map[string]any{"step_id": "search_1", "query": "https://user:private-fixture@example.org/?token=private-fixture", "secret": "private-fixture"})
	emit("verification.updated", map[string]any{"phase": "citation_verification", "status": "supported", "method": "local_model_entailment_and_exact_excerpt_matching", "reason_code": "", "claim_count": 1, "private": "private-fixture", "citations": []map[string]string{{"evidence_id": owned.ID, "url": owned.CanonicalURL, "title": "Untrusted title"}, {"evidence_id": foreign.ID, "url": foreign.CanonicalURL, "title": foreign.Title}, {"evidence_id": owned.ID, "url": "https://user:private-fixture@example.org/private", "title": "private-fixture"}}})
	conversationRequest(t, server.Handler(), "POST", "/api/runs/"+turn.Run.ID+"/cancel", "", "", 202)
	for _, suffix := range []string{"events", "events/stream", "sources"} {
		response := conversationRequest(t, server.Handler(), "GET", "/api/runs/"+turn.Run.ID+"/"+suffix, "", "", 200)
		body := response.Body.String()
		if strings.Contains(body, "private-fixture") || strings.Contains(body, "foreign-evidence") || strings.Contains(body, "foreign.example") {
			t.Fatalf("unsafe %s: %s", suffix, body)
		}
		if strings.HasPrefix(suffix, "events") {
			if !strings.Contains(body, "query_fingerprint") || strings.Contains(body, `"query":`) {
				t.Fatal(body)
			}
		} else {
			var sources []map[string]any
			if e = json.Unmarshal(response.Body.Bytes(), &sources); e != nil {
				t.Fatal(e)
			}
			if len(sources) != 3 {
				t.Fatalf("wrong merge: %s", body)
			}
			states := map[string]string{}
			for _, source := range sources {
				key, _ := source["url"].(string)
				states[key], _ = source["status"].(string)
				if key == "https://example.org/failed" && source["error_code"] != "fetch_timeout" {
					t.Fatal(source)
				}
			}
			if states[owned.CanonicalURL] != "cited" || states["https://example.org/failed"] != "failed" || states[""] != "rejected" {
				t.Fatal(states)
			}
		}
	}
}

func TestCreateConversationAPIIdempotency(t *testing.T) {
	s, store := newTestServer(t)
	defer store.Close()
	h := s.Handler()
	first := conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Idempotent","mode":"chat"}`, "create-key", 201)
	replay := conversationRequest(t, h, "POST", "/api/conversations", `{"mode":"chat","title":"Idempotent"}`, "create-key", 201)
	var a, b domain.Conversation
	json.Unmarshal(first.Body.Bytes(), &a)
	json.Unmarshal(replay.Body.Bytes(), &b)
	if a.ID != b.ID {
		t.Fatal("conversation duplicated")
	}
	conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Changed","mode":"chat"}`, "create-key", 409)
}
