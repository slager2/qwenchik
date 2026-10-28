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

func TestStrictContextRecountsFormatFallbackAndDetectsTemplateIdentityDrift(t *testing.T) {
	var rendered, posts atomic.Int32
	var template atomic.Value
	template.Store("template-A")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			fmt.Fprintf(w, `{"default_generation_settings":{"n_ctx":32768},"total_slots":1,"model_alias":"local","chat_template":%q,"build_info":"fixture-build","model_path":"fixture.gguf","model_ftype":"Q4_K"}`, template.Load().(string))
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"local","meta":{"n_ctx":32768,"size":1000}}]}`)
		case "/apply-template":
			rendered.Add(1)
			fmt.Fprint(w, `{"prompt":"complete rendered prompt"}`)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1,2,3]}`)
		case "/v1/chat/completions":
			if posts.Add(1) == 1 {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"code":"unsupported_json_schema","message":"json_schema unsupported"}}`)
				return
			}
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := New(server.URL+"/v1", "key", "local", time.Second, 4096, 0, 1, false).WithContextBudget(32768)
	request := llm.ChatRequest{ContextWindow: 32768, Messages: []llm.ChatMessage{{Role: "user", Content: "JSON please"}}, JSONSchema: json.RawMessage(`{"type":"object"}`)}
	d, err := c.InspectContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.RuntimeFingerprint = d.RuntimeFingerprint
	if _, err := c.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 || rendered.Load() != 2 {
		t.Fatal("fallback was not independently admitted")
	}
	template.Store("template-B")
	if _, err := c.Complete(context.Background(), request); !errors.Is(err, llm.ErrContextMismatch) {
		t.Fatalf("template drift dispatched: %v", err)
	}
	if posts.Load() != 2 {
		t.Fatal("identity drift reached completion endpoint")
	}
}
