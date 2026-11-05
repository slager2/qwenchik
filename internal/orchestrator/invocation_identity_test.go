package orchestrator

import (
	"context"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
)

type captureModel struct{ requests []llm.ChatRequest }

func (m *captureModel) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	m.requests = append(m.requests, request)
	return llm.ChatResponse{Content: "{}"}, nil
}

func (m *captureModel) Stream(_ context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	m.requests = append(m.requests, request)
	events := make(chan llm.ChatEvent)
	close(events)
	return events, nil
}

// A resumed run creates a new session client whose first request revalidates
// the pinned runtime fingerprint. The durable invocation hash of that request
// must equal the hash recorded for the same request before the pause, or the
// retry is rejected as an unknown non-idempotent outcome.
func TestSessionInvocationHashIsStableAcrossResumeFingerprintRevalidation(t *testing.T) {
	model := domain.ModelProfileSnapshot{Model: "local-qwen", BaseURL: "http://127.0.0.1:8000/v1", ContextLength: 32768, MaxOutputTokens: 1024, Temperature: 0.2, TopP: 0.8, RuntimeFingerprint: "runtime-a", ThinkingPinned: true}
	request := llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "system", Content: "verifier"}, {Role: "user", Content: "{}"}}}

	before := &captureModel{}
	first := &sessionModelClient{inner: before, model: model, promptCacheEnabled: true}
	for range 2 { // planner (revalidates), then verifier (cached metadata)
		if _, err := first.Complete(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	after := &captureModel{}
	resumed := &sessionModelClient{inner: after, model: model, promptCacheEnabled: true}
	if _, err := resumed.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if before.requests[1].RuntimeFingerprint != "" || after.requests[0].RuntimeFingerprint != "runtime-a" {
		t.Fatalf("revalidation pattern changed: before=%q resumed=%q", before.requests[1].RuntimeFingerprint, after.requests[0].RuntimeFingerprint)
	}
	original, err := modelInvocationHash(before.requests[1])
	if err != nil {
		t.Fatal(err)
	}
	retried, err := modelInvocationHash(after.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if original != retried {
		t.Fatal("resumed request hash differs only because it revalidated the runtime fingerprint")
	}
	other := model
	other.RuntimeFingerprint = "runtime-b"
	changed := &captureModel{}
	if _, err := (&sessionModelClient{inner: changed, model: other}).Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	otherHash, err := modelInvocationHash(changed.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if otherHash == original {
		t.Fatal("a different pinned runtime identity must change the invocation hash")
	}
}
