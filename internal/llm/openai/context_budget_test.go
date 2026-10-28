package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/slager2/qwenchik/internal/llm"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStrictContextAdmissionCountsRenderedTokensAndRejectsDrift(t *testing.T) {
	var serverContext, tokenCount, completions atomic.Int32
	serverContext.Store(8192)
	tokenCount.Store(2048)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/props":
			fmt.Fprintf(w, `{"default_generation_settings":{"n_ctx":%d},"total_slots":1,"model_alias":"local","chat_template":"fixed-template","build_info":"fixture-build","model_path":"fixture.gguf","model_ftype":"Q4_K"}`, serverContext.Load())
		case "/v1/models":
			fmt.Fprintf(w, `{"data":[{"id":"local","meta":{"n_ctx":%d,"n_ctx_train":262144,"size":1000}}]}`, serverContext.Load())
		case "/apply-template":
			fmt.Fprint(w, `{"prompt":"rendered with system and generation prefix"}`)
		case "/tokenize":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, int(tokenCount.Load()))})
		case "/v1/chat/completions":
			completions.Add(1)
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(server.URL+"/v1", "key", "local", time.Second, 4096, 0, 1, false).WithContextBudget(8192)
	req := llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "Текст, JSON, code and URLs"}}, ContextWindow: 8192, ContextOutputReserve: 4096, MaxTokens: 24}
	d, err := client.CheckContextRequest(context.Background(), req)
	if err != nil || d.PromptTokens == nil || *d.PromptTokens != 2048 || d.CountMethod != "server_template_tokenize" || d.Decision != "fits" {
		t.Fatalf("exact boundary: %+v %v", d, err)
	}
	if _, err := client.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tokenCount.Store(2049)
	if _, err := client.Complete(context.Background(), req); !errors.Is(err, llm.ErrContextBudget) {
		t.Fatalf("oversized prompt dispatched: %v", err)
	}
	tokenCount.Store(1)
	serverContext.Store(4096)
	if _, err := client.Complete(context.Background(), req); !errors.Is(err, llm.ErrContextMismatch) {
		t.Fatalf("server shrink dispatched: %v", err)
	}
	if completions.Load() != 1 {
		t.Fatalf("blocked requests reached inference: %d", completions.Load())
	}
}

func TestStrictContextUnsupportedTokenizerNeverFallsBackToBytes(t *testing.T) {
	var completions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":32768},"total_slots":1,"model_alias":"local","chat_template":"fixture","build_info":"fixture-build","model_path":"fixture.gguf","model_ftype":"Q4_K"}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"local","meta":{"n_ctx":32768,"size":1000}}]}`)
		case "/v1/chat/completions":
			completions.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(server.URL+"/v1", "key", "local", time.Second, 4096, 0, 1, false).WithContextBudget(32768)
	d, err := client.CheckContextRequest(context.Background(), llm.ChatRequest{ContextWindow: 32768, Messages: []llm.ChatMessage{{Role: "user", Content: "short"}}})
	if !errors.Is(err, llm.ErrContextUnverified) || d.PromptTokens != nil || d.CountMethod != "unavailable" || completions.Load() != 0 {
		t.Fatalf("invented token fit: %+v %v", d, err)
	}
}

func TestThinkingSnapshotOverridesClientDefault(t *testing.T) {
	c := New("http://127.0.0.1:8000/v1", "key", "local", time.Second, 4096, 0, 1, true)
	no := false
	body := c.prepare(llm.ChatRequest{EnableThinking: &no}, false)
	if body.ChatTemplateKwargs["enable_thinking"] {
		t.Fatal("current client thinking overrode saved setting")
	}
}
