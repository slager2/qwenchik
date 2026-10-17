package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/slager2/qwenchik/internal/config"
)

func TestEffectiveModelContextNeverExceedsServerRuntimeOrConfiguredLimit(t *testing.T) {
	for _, fixture := range []struct {
		body string
		want int
	}{
		{`{"data":[{"id":"local","meta":{"n_ctx":8192,"n_ctx_train":262144}}]}`, 8192},
		{`{"data":[{"id":"local","meta":{"n_ctx":131072}}]}`, 32768},
		{`{"data":[{"id":"local"}]}`, 32768},
		{`invalid`, 32768},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-local-key" {
				t.Error("runtime context probe changed endpoint or authentication")
			}
			fmt.Fprint(w, fixture.body)
		}))
		got := effectiveModelContext(context.Background(), config.Config{LLMBaseURL: server.URL + "/v1", LLMAPIKey: "test-local-key", LLMModel: "local", LLMContextLength: 32768})
		server.Close()
		if got != fixture.want {
			t.Errorf("effective context=%d want=%d", got, fixture.want)
		}
	}
}
