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

func TestCompleteSendsSamplerOverridesOnlyWhenSet(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, request)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	client := New(server.URL, "", "qwen-test", time.Second, 128, 0.2, 0.8, false)
	if _, err := client.Complete(context.Background(), llm.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	topK, minP, presence := 20, 0.0, 1.5
	if _, err := client.Complete(context.Background(), llm.ChatRequest{TopK: &topK, MinP: &minP, PresencePenalty: &presence}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"top_k", "min_p", "presence_penalty"} {
		if _, ok := bodies[0][key]; ok {
			t.Errorf("unset %s was sent and would override the server default", key)
		}
	}
	if bodies[1]["top_k"] != float64(20) || bodies[1]["min_p"] != float64(0) || bodies[1]["presence_penalty"] != 1.5 {
		t.Fatalf("sampler overrides were not sent exactly (explicit zero min_p included): %#v", bodies[1])
	}
}
