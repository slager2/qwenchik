package api

import (
	"context"
	"encoding/json"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type contextProbeFixtureClient struct {
	calls        atomic.Int32
	entered      chan struct{}
	release      chan struct{}
	cached       int64
	cacheUnknown bool
}

func (c *contextProbeFixtureClient) CheckContextRequest(_ context.Context, r llm.ChatRequest) (domain.ContextBudget, error) {
	server := r.ContextWindow
	d := domain.NewContextBudget(server, server, r.ContextOutputReserve, &server)
	n := len(r.Messages[len(r.Messages)-1].Content) / 4
	d.PromptTokens = &n
	d.CountMethod = "server_template_tokenize"
	if n > *d.AvailableInputTokens {
		return d, llm.ErrContextBudget
	}
	return d, nil
}
func (c *contextProbeFixtureClient) Complete(ctx context.Context, r llm.ChatRequest) (llm.ChatResponse, error) {
	c.calls.Add(1)
	if c.entered != nil {
		close(c.entered)
		select {
		case <-c.release:
		case <-ctx.Done():
			return llm.ChatResponse{}, ctx.Err()
		}
	}
	findings := []map[string]string{}
	for _, a := range contextAnchors {
		findings = append(findings, map[string]string{"id": a.ID, "quote": a.Quote})
	}
	b, _ := json.Marshal(map[string]any{"findings": findings})
	response := llm.ChatResponse{Content: string(b), PromptTokens: len(r.Messages[len(r.Messages)-1].Content) / 4, OutputTokens: 99}
	if !c.cacheUnknown {
		now := time.Now().UTC()
		tokens := domain.NewUnavailableMetric[int64](domain.MetricUnitTokens, now)
		rate := domain.NewUnavailableMetric[float64](domain.MetricUnitTokensPerSecond, now)
		ms := domain.NewUnavailableMetric[float64](domain.MetricUnitMilliseconds, now)
		response.Telemetry = &llm.ChatTelemetry{InferenceTelemetry: domain.InferenceTelemetry{
			Status: domain.InferenceStatusCompleted, UpdatedAt: now,
			RequestedTokens: tokens, PromptTokens: tokens, OutputTokens: tokens, TotalTokens: tokens,
			CachedPromptTokens:    domain.NewExactMetric(c.cached, domain.MetricUnitTokens, now),
			PromptTokensPerSecond: rate, OutputTokensPerSecond: rate,
			TTFTMS: ms, PromptMS: ms, GenerationMS: ms, ContextTokens: tokens, ContextWindow: tokens,
		}}
	}
	return response, nil
}
func (c *contextProbeFixtureClient) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, llm.ErrInvalidModelOutput
}

func TestContextLoadProbeUsesSharedSlotAndCannotClaimAnIdleSuccess(t *testing.T) {
	s, store := newTestServer(t)
	defer store.Close()
	inner := &contextProbeFixtureClient{entered: make(chan struct{}), release: make(chan struct{})}
	guard := llm.NewGuardedClient(inner)
	s.SetLLMClient(guard)
	s.SetContextProbe(true, 32768, 4096, func(context.Context) error { return nil })
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = guard.Complete(context.Background(), llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "ordinary request"}}})
	}()
	<-inner.entered
	r := httptest.NewRequest("POST", "/api/doctor/context-probe", strings.NewReader(`{"context":32768}`))
	r.Host = "127.0.0.1"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	close(inner.release)
	<-done
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"inference_busy"`) || strings.Contains(w.Body.String(), `"status":"passed"`) || inner.calls.Load() != 1 {
		t.Fatalf("probe bypassed slot: calls=%d %s", inner.calls.Load(), w.Body.String())
	}
}

func TestContextProbeDisabledByDefaultAndProducesMeasuredReceiptWhenEnabled(t *testing.T) {
	s, store := newTestServer(t)
	defer store.Close()
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/doctor/context-probe", strings.NewReader(`{"context":32768}`))
		r.Host = "127.0.0.1"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != 404 {
		t.Fatal("diagnostic was enabled by default")
	}
	inner := &contextProbeFixtureClient{}
	s.SetLLMClient(llm.NewGuardedClient(inner))
	s.SetContextProbe(true, 32768, 4096, func(context.Context) error { return nil })
	w := request()
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"passed"`) || !strings.Contains(w.Body.String(), `"target_exercised":true`) || !strings.Contains(w.Body.String(), `"independent_factual_accuracy":null`) || inner.calls.Load() != 1 {
		t.Fatalf("receipt=%s", w.Body.String())
	}
}

func TestContextProbeRequiresObservedUncachedPrefill(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached", true: "unknown"}[unknown], func(t *testing.T) {
			s, store := newTestServer(t)
			defer store.Close()
			s.SetLLMClient(llm.NewGuardedClient(&contextProbeFixtureClient{cached: 100, cacheUnknown: unknown}))
			s.SetContextProbe(true, 32768, 4096, func(context.Context) error { return nil })
			r := httptest.NewRequest("POST", "/api/doctor/context-probe", strings.NewReader(`{"context":32768}`))
			r.Host = "127.0.0.1"
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"error_code":"uncached_prefill_not_established"`) || strings.Contains(w.Body.String(), `"status":"passed"`) {
				t.Fatalf("cached or unknown prefill accepted: %s", w.Body.String())
			}
		})
	}
}
