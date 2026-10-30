package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/llm"
)

func TestRuntimeMetadataCacheHitAndTTL(t *testing.T) {
	var props, models atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/props":
			props.Add(1)
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":32768},"total_slots":1,"model_alias":"local","chat_template":"fixture","build_info":"build-a","model_path":"fixture.gguf","model_ftype":"Q4_K"}`)
		case "/v1/models":
			models.Add(1)
			fmt.Fprint(w, `{"data":[{"id":"local","meta":{"n_ctx":32768,"n_ctx_train":32768,"size":1000}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "key", "local", time.Second, 128, 0, 1, false).
		WithContextBudget(32768).
		WithRuntimeMetadataTTL(20 * time.Millisecond)
	request := llm.ChatRequest{ContextWindow: 32768}
	if _, err := client.InspectContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InspectContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := props.Load(); got != 1 {
		t.Fatalf("props cache miss: %d", got)
	}
	if got := models.Load(); got != 1 {
		t.Fatalf("models cache miss: %d", got)
	}

	time.Sleep(30 * time.Millisecond)
	if _, err := client.InspectContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := props.Load(); got != 2 {
		t.Fatalf("props TTL was not enforced: %d", got)
	}
	if got := models.Load(); got != 2 {
		t.Fatalf("models TTL was not enforced: %d", got)
	}
}

func TestRuntimeMetadataCacheInvalidatesAfterMetadataError(t *testing.T) {
	var fail atomic.Bool
	var props atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			props.Add(1)
			if fail.Load() {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":32768},"total_slots":1,"model_alias":"local","chat_template":"fixture","build_info":"build-a","model_path":"fixture.gguf","model_ftype":"Q4_K"}`)
			return
		}
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"local","meta":{"n_ctx":32768,"n_ctx_train":32768,"size":1000}}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "key", "local", time.Second, 128, 0, 1, false).
		WithContextBudget(32768).
		WithRuntimeMetadataTTL(time.Minute)
	request := llm.ChatRequest{ContextWindow: 32768}
	d, err := client.InspectContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	request.RuntimeFingerprint = d.RuntimeFingerprint
	if _, err := client.InspectContext(context.Background(), request); !errors.Is(err, llm.ErrContextUnverified) {
		t.Fatalf("metadata outage was not surfaced: %v", err)
	}
	fail.Store(false)
	if _, err := client.InspectContext(context.Background(), llm.ChatRequest{ContextWindow: 32768}); err != nil {
		t.Fatalf("cache was not invalidated after metadata error: %v", err)
	}
	if got := props.Load(); got != 3 {
		t.Fatalf("unexpected props calls after invalidation: %d", got)
	}
}

func TestRuntimeMetadataCacheRefreshesPinnedFingerprint(t *testing.T) {
	var template atomic.Value
	template.Store("fixture-a")
	var props atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/props":
			props.Add(1)
			fmt.Fprintf(w, `{"default_generation_settings":{"n_ctx":32768},"total_slots":1,"model_alias":"local","chat_template":%q,"build_info":"build-a","model_path":"fixture.gguf","model_ftype":"Q4_K"}`, template.Load())
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"local","meta":{"n_ctx":32768,"n_ctx_train":32768,"size":1000}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "key", "local", time.Second, 128, 0, 1, false).
		WithContextBudget(32768).
		WithRuntimeMetadataTTL(time.Minute)
	d, err := client.InspectContext(context.Background(), llm.ChatRequest{ContextWindow: 32768})
	if err != nil {
		t.Fatal(err)
	}
	template.Store("fixture-b")
	request := llm.ChatRequest{ContextWindow: 32768, RuntimeFingerprint: d.RuntimeFingerprint}
	if _, err := client.InspectContext(context.Background(), request); !errors.Is(err, llm.ErrContextMismatch) {
		t.Fatalf("fingerprint drift was not detected: %v", err)
	}
	if got := props.Load(); got != 2 {
		t.Fatalf("pinned fingerprint did not force refresh: %d", got)
	}
	if _, err := client.InspectContext(context.Background(), llm.ChatRequest{ContextWindow: 32768}); err != nil {
		t.Fatalf("fresh metadata was not published after refresh: %v", err)
	}
	if got := props.Load(); got != 2 {
		t.Fatalf("fresh metadata was not cached: %d", got)
	}
}
