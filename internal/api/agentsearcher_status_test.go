package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestProvidersReportsAgentSearcherStatusAndLastOutcome(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	server.SetDDGEnabled(false)
	server.SetAgentSearcher("http://127.0.0.1:8484", "advanced")
	server.SetSearchDiagnostics(func() []research.ProviderOutcome {
		return []research.ProviderOutcome{{Provider: "agentsearcher", State: "healthy", Code: "search_ok", CheckedAt: time.Now().UTC(), LatencyMS: 10600}}
	})
	request := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var body struct {
		Search struct {
			Status    string                    `json:"status"`
			Providers map[string]map[string]any `json:"providers"`
		} `json:"search"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	entry := body.Search.Providers["agentsearcher"]
	if body.Search.Status != "configured" || entry["enabled"] != true || entry["primary"] != true || entry["endpoint"] != "http://127.0.0.1:8484" || entry["search_depth"] != "advanced" || entry["health"] != "healthy" {
		t.Fatalf("agentSearcher status = %s", response.Body.String())
	}
}

func TestProvidersReportsAgentSearcherDisabledByDefault(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	result := server.searchProviderStatus()
	if entry := result["agentsearcher"]; entry == nil || entry["enabled"] != false || entry["status"] != "disabled" {
		t.Fatalf("default agentSearcher status = %#v", entry)
	}
}
