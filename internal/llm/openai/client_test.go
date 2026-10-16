package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestStreamParsesSSEContentDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("stream Accept header = %q", r.Header.Get("Accept"))
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
	for event := range stream {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		content += event.Text
	}
	if content != "hello" {
		t.Fatalf("stream content = %q", content)
	}
}
