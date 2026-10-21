package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestQueryProjectionRetainsSafeActualQueriesAndRedactsSecrets(t *testing.T) {
	for _, query := range []string{"Алматы университеты IT гранты", "https://example.org/docs?q=llama", "Bearer private-fixture", "api_key=private-fixture", "token = private-fixture", "tvly-dev-private-fixture", "https://user:private-fixture@example.org/page", "https://example.org/?token=private-fixture"} {
		hash := sha256.Sum256([]byte(query))
		fingerprint := hex.EncodeToString(hash[:])
		raw, _ := json.Marshal(map[string]any{"step_id": "search_1", "query": query, "query_fingerprint": fingerprint, "api_key": "private-fixture"})
		event := safePublicEvent(domain.Event{Type: "query.updated", Visibility: "user", Payload: raw})
		var payload map[string]any
		if e := json.Unmarshal(event.Payload, &payload); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(event.Payload), "private-fixture") {
			t.Fatal(string(event.Payload))
		}
		if payload["query_fingerprint"] != fingerprint {
			t.Fatal("query fingerprint changed")
		}
		safe := tools.SafeSearchQuery(query)
		if safe == "" {
			if _, exists := payload["query"]; exists {
				t.Fatal("unsafe query field was included")
			}
		} else if payload["query"] != safe {
			t.Fatalf("safe executed query missing: %s", event.Payload)
		}
	}
}
