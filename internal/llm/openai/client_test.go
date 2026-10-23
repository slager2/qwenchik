package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
)

func TestCompleteUsesOpenAICompatibleJSONContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer local-test" {
			t.Errorf("Authorization header not passed to configured local adapter")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request["model"] != "qwen-test" || request["response_format"].(map[string]any)["type"] != "json_object" {
			t.Errorf("unexpected request body: %#v", request)
		}
		if request["temperature"] != 0.2 || request["top_p"] != 0.8 {
			t.Errorf("configured sampling defaults were not sent: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "local-test", "qwen-test", time.Second, 128, 0.2, 0.8, false)
	response, err := client.Complete(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "hello"}}, JSONMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != `{"ok":true}` || response.PromptTokens != 7 || response.OutputTokens != 3 {
		t.Fatalf("unexpected completion: %#v", response)
	}
	if response.Telemetry == nil || response.Telemetry.Status != domain.InferenceStatusCompleted || response.Telemetry.TotalTokens.Value == nil || *response.Telemetry.TotalTokens.Value != 10 || response.Telemetry.ContextTokens.Value == nil || *response.Telemetry.ContextTokens.Value != 10 {
		t.Fatalf("unexpected completion telemetry: %#v", response.Telemetry)
	}
}

func TestCompleteParsesLlamaTimingsAndCacheMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"answer"}}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":2}},"timings":{"cache_n":4,"prompt_n":8,"prompt_ms":12.5,"prompt_per_second":640,"predicted_n":5,"predicted_ms":50,"predicted_per_second":100}}`)
	}))
	defer server.Close()

	client := New(server.URL, "", "qwen-test", time.Second, 128, 0.2, 0.8, false)
	response, err := client.Complete(context.Background(), llm.ChatRequest{ContextWindow: 4096})
	if err != nil {
		t.Fatal(err)
	}
	telemetry := response.Telemetry
	if telemetry == nil {
		t.Fatal("completion telemetry is missing")
	}
	if got := *telemetry.PromptTokens.Value; got != 8 || telemetry.PromptTokens.Source != domain.MetricSourceExact {
		t.Errorf("prompt tokens = %d (%s), want exact 8", got, telemetry.PromptTokens.Source)
	}
	if got := *telemetry.OutputTokens.Value; got != 5 || telemetry.OutputTokens.Source != domain.MetricSourceExact {
		t.Errorf("output tokens = %d (%s), want exact 5", got, telemetry.OutputTokens.Source)
	}
	if got := *telemetry.TotalTokens.Value; got != 13 {
		t.Errorf("total tokens = %d, want 13", got)
	}
	if got := *telemetry.PromptMS.Value; got != 12.5 {
		t.Errorf("prompt_ms = %v, want 12.5", got)
	}
	if got := *telemetry.PromptTokensPerSecond.Value; got != 640 {
		t.Errorf("prompt rate = %v, want 640", got)
	}
	if got := *telemetry.GenerationMS.Value; got != 50 {
		t.Errorf("generation_ms = %v, want 50", got)
	}
	if got := *telemetry.OutputTokensPerSecond.Value; got != 100 {
		t.Errorf("output rate = %v, want 100", got)
	}
	if got := *telemetry.CachedPromptTokens.Value; got != 4 || telemetry.CachedPromptTokens.Source != domain.MetricSourceExact {
		t.Errorf("cached prompt tokens = %d (%s), want exact 4", got, telemetry.CachedPromptTokens.Source)
	}
	if got := *telemetry.ContextWindow.Value; got != 4096 || telemetry.ContextWindow.Source != domain.MetricSourceExact {
		t.Errorf("context window = %d (%s), want exact 4096", got, telemetry.ContextWindow.Source)
	}
}

func TestCompleteMissingAndInvalidMetricsRemainUnavailable(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"missing", `{"choices":[{"message":{"content":"ok"}}]}`},
		{"invalid", `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":"seven","completion_tokens":-1,"total_tokens":null,"prompt_tokens_details":"bad"},"timings":{"prompt_n":1.5,"prompt_ms":"slow","prompt_per_second":-2,"predicted_n":false,"predicted_ms":{},"predicted_per_second":"fast","cache_n":-3}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client := New(server.URL, "", "qwen-test", time.Second, 128, 0.2, 0.8, false)
			response, err := client.Complete(context.Background(), llm.ChatRequest{ContextWindow: 2048})
			if err != nil {
				t.Fatalf("invalid optional metrics failed a valid completion: %v", err)
			}
			telemetry := response.Telemetry
			if telemetry == nil || telemetry.Status != domain.InferenceStatusCompleted {
				t.Fatalf("missing completed telemetry: %#v", telemetry)
			}
			for name, metric := range map[string]domain.Metric[int64]{
				"prompt": telemetry.PromptTokens, "output": telemetry.OutputTokens,
				"total": telemetry.TotalTokens, "cache": telemetry.CachedPromptTokens,
			} {
				if metric.Source != domain.MetricSourceUnavailable || metric.Value != nil {
					t.Errorf("%s metric = %#v, want unavailable", name, metric)
				}
			}
			for name, metric := range map[string]domain.Metric[float64]{
				"prompt_ms": telemetry.PromptMS, "prompt_rate": telemetry.PromptTokensPerSecond,
				"generation_ms": telemetry.GenerationMS, "output_rate": telemetry.OutputTokensPerSecond,
			} {
				if metric.Source != domain.MetricSourceUnavailable || metric.Value != nil {
					t.Errorf("%s metric = %#v, want unavailable", name, metric)
				}
			}
			if telemetry.ContextWindow.Source != domain.MetricSourceExact || telemetry.ContextWindow.Value == nil || *telemetry.ContextWindow.Value != 2048 {
				t.Errorf("context window hint was not reported: %#v", telemetry.ContextWindow)
			}
		})
	}
}

func TestCompleteUsesStrictJSONSchemaResponseFormat(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string          `json:"name"`
					Strict bool            `json:"strict"`
					Schema json.RawMessage `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema.Name != "qagent_response" || !request.ResponseFormat.JSONSchema.Strict {
			t.Errorf("unexpected response format: %#v", request.ResponseFormat)
		}
		if string(request.ResponseFormat.JSONSchema.Schema) != string(schema) {
			t.Errorf("schema = %s, want %s", request.ResponseFormat.JSONSchema.Schema, schema)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "", "qwen-test", time.Second, 128, 0.2, 0.8, false)
	if _, err := client.Complete(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "hello"}}, JSONSchema: schema,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteSendsExplicitZeroSamplingValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request["temperature"] != float64(0) || request["top_p"] != float64(0) {
			t.Errorf("explicit zero values were not sent: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "", "qwen-test", time.Second, 128, 0.7, 0.8, false)
	_, err := client.Complete(context.Background(), llm.ChatRequest{
		Temperature: 0, TemperatureSet: true,
		TopP: 0, TopPSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStreamParsesSSEContentDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("stream Accept header = %q", r.Header.Get("Accept"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		options, ok := request["stream_options"].(map[string]any)
		if !ok || options["include_usage"] != true {
			t.Errorf("stream usage was not requested: %#v", request["stream_options"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "", "qwen-test", time.Second, 128, 0.2, 0.8, false)
	stream, err := client.Stream(context.Background(), llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	var finalTelemetry *llm.ChatTelemetry
	for event := range stream {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		content += event.Text
		if event.Telemetry != nil && event.Telemetry.Status == domain.InferenceStatusCompleted {
			finalTelemetry = event.Telemetry
		}
	}
	if content != "hello" {
		t.Fatalf("stream content = %q", content)
	}
	if finalTelemetry == nil {
		t.Fatal("final completed telemetry was not emitted")
	}
}
