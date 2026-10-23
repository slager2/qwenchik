package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
)

func TestStreamTerminalContractsPreservePartial(t *testing.T) {
	for _, test := range []struct {
		name, tail string
		want       error
		content    string
	}{
		{"done", "data: [DONE]\n\n", nil, "partial"},
		{"finish stop", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", nil, "partial"},
		{"truncated", "data: {\"choices\":[{\"delta\":{\"content\":\" tail\"},\"finish_reason\":\"length\"}]}\n\n", llm.ErrOutputTruncated, "partial tail"},
		{"unexpected EOF", "", llm.ErrStreamInterrupted, "partial"},
		{"invalid JSON", "data: invalid\n\n", llm.ErrInvalidModelOutput, "partial"},
		{"empty choices", "data: {\"choices\":[]}\n\n", llm.ErrInvalidModelOutput, "partial"},
		{"error envelope", "data: {\"error\":{\"message\":\"private-fixture\"}}\n\n", llm.ErrInvalidModelOutput, "partial"},
		{"usage", "data: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\ndata: [DONE]\n\n", nil, "partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"+test.tail)
			}))
			defer server.Close()
			client := New(server.URL, "", "local", time.Second, 128, 0.2, 1, false)
			stream, e := client.Stream(context.Background(), llm.ChatRequest{})
			if e != nil {
				t.Fatal(e)
			}
			var content string
			var failure error
			for event := range stream {
				content += event.Text
				failure = errors.Join(failure, event.Err)
			}
			if content != test.content || !errors.Is(failure, test.want) {
				t.Fatalf("content=%q error=%v want %v", content, failure, test.want)
			}
			if failure != nil && strings.Contains(failure.Error(), "private-fixture") {
				t.Fatal("raw response leaked")
			}
		})
	}
}

func TestStreamUsageOnlyFrameAddsFinalLlamaTimings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if options, ok := request["stream_options"].(map[string]any); !ok || options["include_usage"] != true {
			t.Errorf("stream_options = %#v, want include_usage=true", request["stream_options"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":3}},\"timings\":{\"cache_n\":4,\"prompt_n\":10,\"prompt_ms\":12.5,\"prompt_per_second\":800,\"predicted_n\":5,\"predicted_ms\":50,\"predicted_per_second\":100}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "", "local", time.Second, 128, 0, 1, false)
	stream, err := client.Stream(context.Background(), llm.ChatRequest{ContextWindow: 2048})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	var statuses []domain.InferenceStatus
	var final *llm.ChatTelemetry
	for event := range stream {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		content += event.Text
		if event.Telemetry != nil {
			statuses = append(statuses, event.Telemetry.Status)
			if event.Telemetry.Status == domain.InferenceStatusCompleted {
				final = event.Telemetry
			}
		}
	}
	if content != "hello" {
		t.Fatalf("content = %q, want hello", content)
	}
	for _, want := range []domain.InferenceStatus{
		domain.InferenceStatusLoading, domain.InferenceStatusPrefilling,
		domain.InferenceStatusGenerating, domain.InferenceStatusCompleted,
	} {
		found := false
		for _, status := range statuses {
			found = found || status == want
		}
		if !found {
			t.Errorf("status %q was not emitted: %v", want, statuses)
		}
	}
	if final == nil {
		t.Fatal("completed telemetry was not emitted")
	}
	if got := *final.PromptTokens.Value; got != 10 || final.PromptTokens.Source != domain.MetricSourceExact {
		t.Errorf("prompt tokens = %d (%s), want exact 10", got, final.PromptTokens.Source)
	}
	if got := *final.OutputTokens.Value; got != 5 || final.OutputTokens.Source != domain.MetricSourceExact {
		t.Errorf("output tokens = %d (%s), want exact 5", got, final.OutputTokens.Source)
	}
	if got := *final.CachedPromptTokens.Value; got != 4 || final.CachedPromptTokens.Source != domain.MetricSourceExact {
		t.Errorf("cached prompt tokens = %d (%s), want exact 4", got, final.CachedPromptTokens.Source)
	}
	if final.PromptMS.Source != domain.MetricSourceExact || *final.PromptMS.Value != 12.5 || final.GenerationMS.Source != domain.MetricSourceExact || *final.GenerationMS.Value != 50 {
		t.Errorf("timing metrics were not parsed: prompt=%#v generation=%#v", final.PromptMS, final.GenerationMS)
	}
}

func TestStreamAcceptsEmptyFinalDeltaAndEOFAfterFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "", "local", time.Second, 128, 0, 1, false)
	stream, err := client.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	var failure error
	var final *llm.ChatTelemetry
	for event := range stream {
		content += event.Text
		failure = errors.Join(failure, event.Err)
		if event.Telemetry != nil && event.Telemetry.Status == domain.InferenceStatusCompleted {
			final = event.Telemetry
		}
	}
	if content != "answer" || failure != nil || final == nil {
		t.Fatalf("content=%q error=%v final=%#v", content, failure, final)
	}
}

func TestStreamCancellationKeepsPartialEstimates(t *testing.T) {
	serverStopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(serverStopped)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := New(server.URL, "", "local", time.Second, 128, 0, 1, false)
	stream, err := client.Stream(ctx, llm.ChatRequest{})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var sawText bool
	for event := range stream {
		if event.Text == "partial" {
			sawText = true
			cancel()
		}
		if event.Telemetry != nil && event.Telemetry.Status == domain.InferenceStatusCancelled {
			if event.Telemetry.OutputTokens.Source != domain.MetricSourceEstimated || event.Telemetry.OutputTokens.Value == nil || *event.Telemetry.OutputTokens.Value < 1 {
				t.Errorf("cancellation lost partial output estimate: %#v", event.Telemetry.OutputTokens)
			}
		}
	}
	if !sawText {
		t.Fatal("stream did not deliver partial text before cancellation")
	}
	select {
	case <-serverStopped:
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not stop the server")
	}
}

func TestStreamRejectsEmptySuccessfulOutput(t *testing.T) {
	for _, body := range []string{"data: [DONE]\n\n", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, body)
		}))
		c := New(s.URL, "", "local", time.Second, 128, 0, 1, false)
		stream, e := c.Stream(context.Background(), llm.ChatRequest{})
		if e != nil {
			t.Fatal(e)
		}
		var failure error
		for event := range stream {
			failure = errors.Join(failure, event.Err)
		}
		s.Close()
		if !errors.Is(failure, llm.ErrInvalidModelOutput) {
			t.Fatal(failure)
		}
	}
}

func TestStructuredFallbackIsExplicitAndBounded(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name    string
			status  int
			body    string
			calls   int
			success bool
		}{
			{"unsupported", 400, `{"error":{"message":"json_schema response_format is not supported"}}`, 2, true},
			{"unsupported422", 422, `{"error":{"code":"unsupported_json_schema"}}`, 2, true},
			{"invalid schema", 400, `{"error":{"message":"schema is invalid"}}`, 1, false},
			{"auth", 401, `{"error":{"message":"json_schema unsupported"}}`, 1, false},
			{"rate limit", 429, `{"error":{"message":"json_schema unsupported"}}`, 1, false},
			{"server error", 500, `{"error":{"message":"json_schema unsupported"}}`, 1, false},
			{"no retry loop", 400, `{"error":{"message":"json_schema unsupported"}}`, 2, false},
		} {
			t.Run(fmt.Sprintf("%t/%s", streaming, test.name), func(t *testing.T) {
				var calls atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					var body requestBody
					if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
						t.Error(e)
					}
					if n == 1 && body.ResponseFormat.Type != "json_schema" {
						t.Error("schema omitted")
					}
					if n == 2 && (body.ResponseFormat.Type != "json_object" || body.ResponseFormat.JSONSchema != nil) {
						t.Error("invalid fallback")
					}
					if n == 1 || !test.success {
						w.WriteHeader(test.status)
						fmt.Fprint(w, test.body)
						return
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{}\"}}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, `{"choices":[{"message":{"content":"{}"}}]}`)
					}
				}))
				defer s.Close()
				c := New(s.URL, "", "local", time.Second, 128, 0, 1, false)
				var callbacks atomic.Int32
				request := llm.ChatRequest{
					JSONSchema: json.RawMessage(`{"type":"object"}`),
					BeforeFallbackAttempt: func() error {
						callbacks.Add(1)
						return nil
					},
				}
				var e error
				if streaming {
					var events <-chan llm.ChatEvent
					events, e = c.Stream(context.Background(), request)
					if e == nil {
						for event := range events {
							e = errors.Join(e, event.Err)
						}
					}
				} else {
					_, e = c.Complete(context.Background(), request)
				}
				wantCallbacks := int32(0)
				if test.calls == 2 {
					wantCallbacks = 1
				}
				if (e == nil) != test.success || int(calls.Load()) != test.calls || callbacks.Load() != wantCallbacks {
					t.Fatalf("calls=%d callbacks=%d error=%v", calls.Load(), callbacks.Load(), e)
				}
				if !test.success {
					var httpError *HTTPError
					if !errors.As(e, &httpError) || httpError.StatusCode != test.status {
						t.Fatalf("expected typed HTTP %d error, got %v", test.status, e)
					}
				}
			})
		}
	}
}

type fallbackBudgetError struct{ message string }

func (e *fallbackBudgetError) Error() string { return e.message }

func TestSchemaFallbackCallbackAllowsOrDeniesFallbackRequest(t *testing.T) {
	t.Run("allowed guarded completion", func(t *testing.T) {
		var requests atomic.Int32
		var callbacks atomic.Int32
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			var body map[string]json.RawMessage
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Error(e)
			}
			if _, ok := body["BeforeFallbackAttempt"]; ok {
				t.Error("fallback callback was serialized")
			}
			if requests.Load() == 1 {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"json_schema is not supported"}}`)
				return
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"{}"}}]}`)
		}))
		defer s.Close()
		request := llm.ChatRequest{
			JSONSchema: json.RawMessage(`{"type":"object"}`),
			BeforeFallbackAttempt: func() error {
				callbacks.Add(1)
				return nil
			},
		}
		if _, e := json.Marshal(request); e != nil {
			t.Fatalf("marshal request with ephemeral callback: %v", e)
		}
		client := llm.NewGuardedClient(New(s.URL, "", "local", time.Second, 128, 0, 1, false))
		if _, e := client.Complete(context.Background(), request); e != nil {
			t.Fatal(e)
		}
		if requests.Load() != 2 || callbacks.Load() != 1 {
			t.Fatalf("requests=%d callbacks=%d, want 2 and 1", requests.Load(), callbacks.Load())
		}
	})

	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("denied streaming=%t", streaming), func(t *testing.T) {
			var requests atomic.Int32
			var callbacks atomic.Int32
			denied := &fallbackBudgetError{message: "fallback budget exhausted"}
			callbackError := fmt.Errorf("charge fallback attempt: %w", denied)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body map[string]json.RawMessage
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error(e)
				}
				if _, ok := body["BeforeFallbackAttempt"]; ok {
					t.Error("fallback callback was serialized")
				}
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"error":{"code":"unsupported_json_schema"}}`)
			}))
			defer s.Close()
			client := llm.NewGuardedClient(New(s.URL, "", "local", time.Second, 128, 0, 1, false))
			request := llm.ChatRequest{
				JSONSchema: json.RawMessage(`{"type":"object"}`),
				BeforeFallbackAttempt: func() error {
					callbacks.Add(1)
					return callbackError
				},
			}
			var err error
			if streaming {
				_, err = client.Stream(context.Background(), request)
			} else {
				_, err = client.Complete(context.Background(), request)
			}
			var budgetError *fallbackBudgetError
			if err != callbackError || !errors.Is(err, denied) || !errors.As(err, &budgetError) || budgetError != denied {
				t.Fatalf("error = %v, want original callback error with typed budget cause", err)
			}
			if requests.Load() != 1 || callbacks.Load() != 1 {
				t.Fatalf("requests=%d callbacks=%d, want 1 and 1", requests.Load(), callbacks.Load())
			}
		})
	}
}

func TestGuardedHTTPStreamCancelReleasesConnectionAndSlot(t *testing.T) {
	closed := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer s.Close()
	g := llm.NewGuardedClient(New(s.URL, "", "local", time.Second, 128, 0, 1, false))
	ctx, cancel := context.WithCancel(context.Background())
	stream, e := g.Stream(ctx, llm.ChatRequest{})
	if e != nil {
		t.Fatal(e)
	}
	<-stream
	if e = llm.ProbeStructuredOutput(context.Background(), g); !errors.Is(e, llm.ErrInferenceBusy) {
		t.Fatal(e)
	}
	cancel()
	for range stream {
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("upstream HTTP request leaked")
	}
}

func TestTimeoutAndCompletionValidation(t *testing.T) {
	for _, body := range []string{`{`, `{"choices":[]}`, `{"choices":[{"message":{"content":""}}]}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		c := New(s.URL, "", "local", time.Second, 128, 0, 1, false)
		_, e := c.Complete(context.Background(), llm.ChatRequest{})
		s.Close()
		if !errors.Is(e, llm.ErrInvalidModelOutput) {
			t.Fatal(e)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request any
		_ = json.NewDecoder(r.Body).Decode(&request)
		<-r.Context().Done()
	}))
	defer s.Close()
	c := New(s.URL, "", "local", 20*time.Millisecond, 128, 0, 1, false)
	if _, e := c.Complete(context.Background(), llm.ChatRequest{}); !errors.Is(e, llm.ErrModelTimeout) {
		t.Fatal(e)
	}
}

func TestStreamTimeoutPreservesPartialAndClassifiesTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	c := New(s.URL, "", "local", 30*time.Millisecond, 128, 0, 1, false)
	stream, e := c.Stream(context.Background(), llm.ChatRequest{})
	if e != nil {
		t.Fatal(e)
	}
	var content string
	var failure error
	var final *llm.ChatTelemetry
	for event := range stream {
		content += event.Text
		failure = errors.Join(failure, event.Err)
		if event.Telemetry != nil && event.Err != nil {
			final = event.Telemetry
		}
	}
	if content != "partial" || !errors.Is(failure, llm.ErrModelTimeout) || !errors.Is(failure, llm.ErrStreamInterrupted) {
		t.Fatalf("content=%q error=%v", content, failure)
	}
	if final == nil || final.Status != domain.InferenceStatusFailed || final.OutputTokens.Source != domain.MetricSourceEstimated || final.OutputTokens.Value == nil {
		t.Fatalf("interruption did not preserve partial telemetry: %#v", final)
	}
}

func TestCompletionLengthReturnsPartialWithTypedError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`)
	}))
	defer s.Close()
	c := New(s.URL, "", "local", time.Second, 128, 0, 1, false)
	result, e := c.Complete(context.Background(), llm.ChatRequest{})
	if result.Content != "partial" || !errors.Is(e, llm.ErrOutputTruncated) {
		t.Fatalf("result=%#v error=%v", result, e)
	}
	if result.Telemetry == nil || result.Telemetry.Status != domain.InferenceStatusFailed {
		t.Fatalf("truncated completion telemetry = %#v, want failed", result.Telemetry)
	}
}
