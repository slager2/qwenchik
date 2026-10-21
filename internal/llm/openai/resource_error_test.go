package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/runtime/resources"
)

func TestHTTPResourceClassificationIsBoundedAndPrivate(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		oom    bool
	}{
		{"oom", 500, `{"error":{"code":"out_of_memory","message":"private-fixture"}}`, true},
		{"unavailable_oom", 503, `{"error":{"message":"CUDA out of memory private-fixture"}}`, true},
		{"insufficient_memory", 507, `{"error":{"type":"out_of_memory"}}`, true},
		{"auth", 401, `{"error":{"message":"out of memory private-fixture"}}`, false},
		{"rate", 429, `{"error":{"code":"out_of_memory"}}`, false},
		{"validation", 422, `{"error":{"code":"out_of_memory"}}`, false},
		{"other", 500, `{"error":{"message":"private-fixture internal error"}}`, false},
		{"malformed", 503, `out of memory private-fixture`, false},
		{"oversized", 500, `{"error":{"message":"out of memory ` + strings.Repeat("x", 4096) + `"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := New(server.URL+"/v1", "", "local", time.Second, 64, 0, 1, false)
			for _, stream := range []bool{false, true} {
				var err error
				request := llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "hello"}}}
				if stream {
					_, err = client.Stream(context.Background(), request)
				} else {
					_, err = client.Complete(context.Background(), request)
				}
				var httpError *HTTPError
				if !errors.As(err, &httpError) || httpError.StatusCode != test.status || errors.Is(err, resources.ErrModelOOM) != test.oom || errors.Is(err, resources.ErrModelResource) != test.oom {
					t.Fatalf("classification: %v", err)
				}
				if strings.Contains(err.Error(), "private-fixture") || strings.Contains(err.Error(), "CUDA") {
					t.Fatal("model error body leaked")
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("unexpected automatic retry: %d", calls.Load())
			}
		})
	}
}
