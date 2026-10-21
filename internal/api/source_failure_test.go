package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestSourceFailureSanitizerUsesTypedAllowlist(t *testing.T) {
	for _, code := range []string{"fetch_dns_failed", "task_fetch_budget_exceeded", "source_response_too_large", "pdf_source_too_large", "pdf_extraction_input_too_large", "pdf_page_limit_exceeded", "pdf_text_limit_exceeded", "pdf_resource_exceeded", "pdf_timeout", "pdf_extractor_unavailable", "pdf_malformed", "pdf_encrypted", "fetch_outcome_unknown"} {
		result := safeSourceFields(map[string]any{"error_code": code})
		if result["error_code"] != code {
			t.Fatal(result)
		}
	}
	result := safeSourceFields(map[string]any{
		"tool": "fetch_url", "attempt_id": "fetch-1:0", "plan_attempt": float64(2), "step_id": "fetch_1",
		"target_url": "https://user:private-fixture@example.org/report?token=private-fixture",
		"error_code": "raw error private-fixture", "message": "private-fixture", "stderr": "private-fixture",
		"declared_bytes": float64(40_534_667), "effective_limit_bytes": float64(67_108_864), "fetched_bytes": float64(0),
		"retryable": false, "try_next_source": true, "user_retry_task": false, "fallback_attempted": false,
		"remaining_candidates": float64(4), "timestamp": "2026-10-20T10:00:00Z", "mime_type": "application/pdf; secret=private-fixture",
	})
	body, _ := json.Marshal(result)
	if strings.Contains(string(body), "private-fixture") || result["target_url"] != nil || result["error_code"] != "source_failed" || result["fetched_bytes"] != float64(0) || result["mime_type"] != "application/pdf" || !validFingerprint(result["source_fingerprint"].(string)) {
		t.Fatal(string(body))
	}
	for _, value := range []any{float64(-1), 1.5, float64(1 << 41), "40534667", true, nil} {
		invalid := safeSourceFields(map[string]any{"declared_bytes": value, "fetched_bytes": value, "remaining_candidates": value, "plan_attempt": value})
		if len(invalid) != 0 {
			t.Fatalf("invalid value %v accepted: %v", value, invalid)
		}
	}
	invalid := safeSourceFields(map[string]any{"attempt_id": "private-fixture?token=secret", "timestamp": "yesterday", "mime_type": "secret/private-fixture", "retryable": "true", "try_next_source": 1, "source_fingerprint": "invalid", "remaining_candidates": float64(1_000_001)})
	if len(invalid) != 0 {
		t.Fatal(invalid)
	}
}

func TestPDFFailureProjectionAndReplayPreserveIdentityBytesAndEvidence(t *testing.T) {
	ctx := context.Background()
	s, store := newTestServer(t)
	defer store.Close()
	c, err := store.CreateConversation(ctx, domain.Conversation{Title: "PDF", Mode: "research"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Read sources"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimNext(ctx); err != nil || !claimed {
		t.Fatalf("claim research fixture: %v %v", claimed, err)
	}
	emit := func(kind, visibility string, payload map[string]any) {
		t.Helper()
		if _, err := store.RecordEvent(ctx, turn.Run.ID, kind, nil, visibility, payload); err != nil {
			t.Fatal(err)
		}
	}
	const pdfURL = "https://example.org/report.pdf"
	const explicitURL = "https://example.net/explicit.pdf"
	emit("search.hit", "user", map[string]any{"hit": research.SearchHitObservation{Fingerprint: sourceFingerprint(pdfURL), URL: pdfURL, Title: "Original title", Snippet: "Original snippet", Provider: "searxng", Status: "validated"}})
	emit("tool.started", "user", map[string]any{"tool": "fetch_url", "attempt_id": "pdf-1", "step_id": "fetch_1", "target_url": pdfURL})
	for _, visibility := range []string{"audit", "user"} {
		emit("tool.failed", visibility, map[string]any{"tool": "fetch_url", "attempt_id": "pdf-1", "plan_attempt": 1, "step_id": "fetch_1", "target_url": pdfURL, "source_fingerprint": sourceFingerprint(pdfURL), "status": "rejected", "error_code": "pdf_source_too_large", "declared_bytes": 40_534_667, "effective_limit_bytes": 32_000_000, "fetched_bytes": 0, "mime_type": "application/pdf", "try_next_source": true, "remaining_candidates": 2, "fallback_attempted": false, "timestamp": "2026-10-20T10:00:00Z", "message": "private-fixture"})
	}
	emit("tool.started", "user", map[string]any{"tool": "fetch_url", "attempt_id": "pdf-1", "target_url": pdfURL})
	emit("tool.started", "user", map[string]any{"tool": "fetch_url", "attempt_id": "explicit-1", "target_url": explicitURL})
	emit("tool.failed", "user", map[string]any{"tool": "fetch_url", "attempt_id": "explicit-1", "target_url": explicitURL, "code": "pdf_malformed", "fetched_bytes": 321, "try_next_source": false})
	emit("tool.failed", "audit", map[string]any{"tool": "fetch_url", "target_url": "https://example.org/legacy", "failure": "source_unavailable", "fetched_bytes": 0})
	emit("tool.failed", "user", map[string]any{"tool": "fetch_url", "target_url": "https://user:private-fixture@example.org/?token=private-fixture", "failure": "unsafe_url"})
	pending, err := s.sourceProjection(ctx, turn.Run.ID)
	if err != nil || pending[0]["fallback_attempted"] != false {
		t.Fatal(pending, err)
	}
	emit("tool.started", "user", map[string]any{"tool": "fetch_url", "attempt_id": "html-2", "plan_attempt": 1, "step_id": "fetch_1", "target_url": "https://good.example/article"})
	emit("source.extracted", "user", map[string]any{"tool": "fetch_url", "attempt_id": "html-2", "target_url": "https://good.example/article"})
	_, err = store.SaveEvidence(ctx, domain.Evidence{ID: "accepted-html", TaskID: turn.Run.ID, CanonicalURL: "https://good.example/article", OriginalURL: "https://good.example/article", Title: "Accepted HTML", Provider: "searxng", ContentSHA256: "hash", HTTPStatus: 200, Excerpt: "Accepted text", RetrievedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	emit("research.warning", "user", map[string]any{"code": "sources_skipped", "partial": true, "message": "private-fixture", "source_failures": []map[string]any{
		{"attempt_id": "pdf-1", "target_url": pdfURL, "status": "rejected", "error_code": "pdf_source_too_large", "fetched_bytes": 0, "declared_bytes": 40_534_667, "message": "private-fixture"},
		{"attempt_id": "private-1", "target_url": "https://user:private-fixture@example.org/?token=private-fixture", "status": "rejected", "error_code": "raw private-fixture"},
	}})
	conversationRequest(t, s.Handler(), "POST", "/api/runs/"+turn.Run.ID+"/cancel", "", "", 202)
	for _, suffix := range []string{"sources", "events", "events/stream"} {
		response := conversationRequest(t, s.Handler(), "GET", "/api/runs/"+turn.Run.ID+"/"+suffix, "", "", 200)
		if strings.Contains(response.Body.String(), "private-fixture") {
			t.Fatal(response.Body.String())
		}
		if suffix != "sources" {
			for _, field := range []string{"pdf_source_too_large", `"fetched_bytes":0`, `"declared_bytes":40534667`, `"try_next_source":true`, `"source_fingerprint"`, "research.warning", `"partial":true`, `"source_failures"`} {
				if !strings.Contains(response.Body.String(), field) {
					t.Fatalf("missing %s in %s", field, response.Body.String())
				}
			}
			continue
		}
		var sources []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &sources); err != nil {
			t.Fatal(err)
		}
		if len(sources) != 5 {
			t.Fatal(sources)
		}
		for _, source := range sources {
			switch source["url"] {
			case pdfURL:
				if source["status"] != "rejected" || source["error_code"] != "pdf_source_too_large" || source["title"] != "Original title" || source["snippet"] != "Original snippet" || source["provider"] != "searxng" || source["fetched_bytes"] != float64(0) || source["timestamp"] != "2026-10-20T10:00:00Z" || source["fallback_attempted"] != true {
					t.Fatal(source)
				}
			case explicitURL:
				if source["status"] != "failed" || source["error_code"] != "pdf_malformed" || source["fetched_bytes"] != float64(321) || source["timestamp"] == nil {
					t.Fatal(source)
				}
			case "https://good.example/article":
				if source["status"] != "extracted" || source["evidence_id"] != "accepted-html" {
					t.Fatal(source)
				}
			}
		}
	}
}

func TestPartialWarningSanitizerBoundsAndDeduplicatesFailures(t *testing.T) {
	items := []map[string]any{
		{"attempt_id": "a", "status": "rejected", "error_code": "pdf_malformed", "fetched_bytes": 0, "fallback_attempted": true},
		{"attempt_id": "a", "status": "rejected", "error_code": "pdf_malformed", "fetched_bytes": 0, "fallback_attempted": false},
		{"attempt_id": "bad", "status": "fetching", "error_code": "private-fixture"},
	}
	encoded, _ := json.Marshal(map[string]any{"code": "private-fixture", "partial": "true", "source_failures": items, "message": "private-fixture"})
	result := safeResearchWarning(encoded)
	failures := result["source_failures"].([]map[string]any)
	if len(failures) != 1 || failures[0]["fetched_bytes"] != float64(0) || failures[0]["fallback_attempted"] != true || result["partial"] != nil || result["code"] != "research_warning" {
		t.Fatal(result)
	}
	for i := 0; i < 257; i++ {
		items = append(items, map[string]any{"attempt_id": fmt.Sprintf("failure-%d", i), "status": "failed", "error_code": "pdf_timeout"})
	}
	encoded, _ = json.Marshal(map[string]any{"code": "sources_skipped", "partial": true, "source_failures": items})
	result = safeResearchWarning(encoded)
	if len(result["source_failures"].([]map[string]any)) != 256 || result["source_failures_truncated"] != true {
		t.Fatal("unbounded public failure summary")
	}
}

func TestDoctorResearchPolicySnapshotIsSafeAndRuntimeOnly(t *testing.T) {
	models := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected model request: %s", r.URL.Path)
		}
		models++
		w.WriteHeader(200)
	}))
	defer model.Close()
	s, store := newTestServer(t)
	defer store.Close()
	s.llmURL = model.URL
	policy := map[string]any{"task_fetch_budget_bytes": int64(134217728), "source_fetch_bytes": 16777216, "pdf_bytes": 67108864, "pdf_pages": 20, "pdf_text_runes": 50000, "pdf_extraction_timeout_seconds": 30, "pdf_download_timeout_seconds": 120, "pdf_memory_bytes": 536870912, "pdf_temp_bytes": 67108864, "pdf_cpu_limit": 1.0, "pdf_pid_limit": 32, "api_key": "private-fixture", "fetch_temp_dir": "private-fixture", "pdf_allow_unsandboxed_fallback": false}
	checks := 0
	s.SetResearchPolicy(policy, func(ctx context.Context) error {
		checks++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
			t.Error("PDF runtime check is not bounded")
		}
		return errors.New("Docker private-fixture")
	})
	policy["pdf_bytes"] = 1
	for _, available := range []bool{false, true} {
		if available {
			s.SetResearchPolicy(map[string]any{"pdf_bytes": 67108864, "pdf_cpu_limit": 1, "pdf_allow_unsandboxed_fallback": false}, func(context.Context) error { checks++; return nil })
		}
		request := httptest.NewRequest("GET", "/api/doctor", nil)
		request.Host = "127.0.0.1:8080"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if strings.Contains(response.Body.String(), "private-fixture") {
			t.Fatal(response.Body.String())
		}
		var body struct {
			Limits  map[string]any `json:"research_limits"`
			Runtime map[string]any `json:"pdf_extractor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Limits["pdf_bytes"] != float64(67108864) || body.Limits["pdf_allow_unsandboxed_fallback"] != false || body.Runtime["isolated_available"] != available || body.Runtime["fallback_allowed"] != false {
			t.Fatal(response.Body.String())
		}
	}
	if checks != 2 || models != 2 {
		t.Fatalf("runtime=%d models=%d", checks, models)
	}
	s.SetResearchPolicy(map[string]any{"pdf_bytes": -1, "pdf_cpu_limit": "1", "pdf_pages": 1.5, "pdf_allow_unsandboxed_fallback": "true"}, nil)
	limits, runtime, _ := s.researchPolicyStatus(context.Background())
	if len(limits) != 2 || runtime["isolated_available"] != false || runtime["fallback_allowed"] != false {
		t.Fatal(limits, runtime)
	}
}
