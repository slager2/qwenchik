package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/slager2/qwenchik/internal/config"
)

func TestModelContextDiscoveryNeverSubstitutesConfiguredCapacity(t *testing.T) {
	for _, fixture := range []struct {
		body   string
		want   int
		server int
		status string
	}{
		{`{"data":[{"id":"local","meta":{"n_ctx":32768,"n_ctx_train":262144,"size":1000}}]}`, 32768, 32768, "matched"},
		{`{"data":[{"id":"local","meta":{"n_ctx":8192,"n_ctx_train":262144,"size":1000}}]}`, 0, 8192, "context_mismatch"},
		{`{"data":[{"id":"local","meta":{"n_ctx":131072,"size":1000}}]}`, 0, 131072, "context_mismatch"},
		{`{"data":[{"id":"other"}]}`, 0, 32768, "unavailable"},
		{`invalid`, 0, 32768, "unavailable"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if (r.URL.Path != "/v1/models" && r.URL.Path != "/props") || r.Header.Get("Authorization") != "Bearer test-local-key" {
				t.Error("runtime context probe changed endpoint or authentication")
			}
			if r.URL.Path == "/props" {
				fmt.Fprintf(w, `{"default_generation_settings":{"n_ctx":%d},"total_slots":1,"model_alias":"local","chat_template":"fixture","build_info":"fixture-build","model_path":"fixture.gguf","model_ftype":"Q4_K"}`, fixture.server)
				return
			}
			fmt.Fprint(w, fixture.body)
		}))
		cfg := config.Config{LLMBaseURL: server.URL + "/v1", LLMAPIKey: "test-local-key", LLMModel: "local", LLMContextLength: 32768, LLMMaxOutputTokens: 4096}
		got := effectiveModelContext(context.Background(), cfg)
		d, err := discoverModelContext(context.Background(), cfg)
		if d.ContextStatus != fixture.status || (fixture.status == "matched") != (err == nil) {
			t.Fatalf("diagnostics=%+v err=%v", d, err)
		}
		if fixture.status == "unavailable" && d.ServerReportedContext != nil {
			t.Fatal("unknown metadata became configured capacity")
		}
		server.Close()
		if got != fixture.want {
			t.Errorf("effective context=%d want=%d", got, fixture.want)
		}
	}
}

func TestExperimental64KContextRequiresMatchingLiveRuntime(t *testing.T) {
	for _, fixture := range []struct {
		name          string
		serverContext int
		want          int
		status        string
	}{
		{name: "matched", serverContext: 65536, want: 65536, status: "matched"},
		{name: "server_still_32k", serverContext: 32768, want: 0, status: "context_mismatch"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/props" {
					fmt.Fprintf(w, `{"default_generation_settings":{"n_ctx":%d},"total_slots":1,"model_alias":"local","chat_template":"fixture","build_info":"fixture-build","model_path":"fixture.gguf","model_ftype":"Q4_K"}`, fixture.serverContext)
					return
				}
				fmt.Fprintf(w, `{"data":[{"id":"local","meta":{"n_ctx":%d,"n_ctx_train":262144,"size":1000}}]}`, fixture.serverContext)
			}))
			defer server.Close()

			cfg := config.Config{LLMBaseURL: server.URL + "/v1", LLMAPIKey: "test-local-key", LLMModel: "local", LLMContextLength: 65536, LLMMaxOutputTokens: 4096}
			got := effectiveModelContext(context.Background(), cfg)
			diagnostic, err := discoverModelContext(context.Background(), cfg)
			if diagnostic.ContextStatus != fixture.status || (fixture.status == "matched") != (err == nil) {
				t.Fatalf("diagnostics=%+v err=%v", diagnostic, err)
			}
			if got != fixture.want {
				t.Fatalf("effective context=%d want=%d", got, fixture.want)
			}
		})
	}
}
