package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
)

type labPolicyNoInference struct{}

func (labPolicyNoInference) Complete(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	panic("passive policy endpoint invoked completion")
}

func (labPolicyNoInference) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	panic("passive policy endpoint invoked streaming")
}

type labPolicyNoNetwork struct{}

func (labPolicyNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	panic("passive policy endpoint invoked an HTTP probe")
}

func TestLabPolicyIsPassiveSanitizedSnapshot(t *testing.T) {
	check := func(context.Context) error { panic("passive policy endpoint invoked a readiness check") }
	s := New(nil, nil, check, "http://127.0.0.1:8000/v1", "private-secret", "http://127.0.0.1:8081", nil)
	s.SetLLMClient(labPolicyNoInference{})
	s.client = &http.Client{Transport: labPolicyNoNetwork{}}
	s.SetFetchTempCheck(check)
	s.searchDiagnostics = func() []research.ProviderOutcome { panic("passive policy endpoint invoked search diagnostics") }
	s.runtimeStatus = func() map[string]any { panic("passive policy endpoint invoked runtime status") }
	handler := s.Handler()
	get := func() map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/lab/policy", nil)
		r.Host = "127.0.0.1:8080"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "http://") {
			t.Fatalf("policy leaked private configuration: %s", w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 2 {
			t.Fatalf("unexpected operational fields: %v", body)
		}
		return body
	}
	body := get()
	if body["hold_resource_pauses"] != false || body["research_limits"].(map[string]any)["configured"] != false {
		t.Fatalf("unexpected defaults: %v", body)
	}
	hash := strings.Repeat("ab", 32)
	policy := map[string]any{
		"privacy_mode": domain.PrivacyOffline, "offline_corpus_sha256": hash,
		"adaptive_enabled": true, "sectioned_report_enabled": true, "pdf_bytes": 67108864,
		"api_key": "private-secret", "offline_corpus": "private-content", "corpus_path": "private-path",
		"base_url": "http://private-host", "pdf_pages": -1,
	}
	s.SetResearchPolicy(policy, check)
	policy["offline_corpus_sha256"] = "mutated"
	for _, hold := range []bool{true, false} {
		s.SetLabHoldResourcePauses(hold)
		body = get()
		limits := body["research_limits"].(map[string]any)
		if body["hold_resource_pauses"] != hold || limits["offline_corpus_sha256"] != hash || limits["privacy_mode"] != domain.PrivacyOffline || limits["adaptive_enabled"] != true || limits["sectioned_report_enabled"] != true || limits["pdf_bytes"] != float64(67108864) {
			t.Fatalf("policy mismatch: %v", body)
		}
		if _, ok := limits["pdf_pages"]; ok {
			t.Fatal("invalid numeric policy escaped sanitizer")
		}
		limits["offline_corpus_sha256"] = "response mutation"
	}
}
