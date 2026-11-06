package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestConfiguredSearchProviderPolicy(t *testing.T) {
	for _, policy := range []string{"", "local_primary", "parallel"} {
		for _, ddg := range []bool{false, true} {
			cfg := config.Config{SearchProviderPolicy: policy, SearXNGURL: "http://127.0.0.1:8081", TavilyAPIKey: "test-key", SearchEnableDDG: ddg, FetchTempDir: filepath.Join(t.TempDir(), "fetch-temp")}
			provider, err := configuredSearchProvider(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if policy == "parallel" {
				if _, ok := provider.(*research.CompositeSearch); !ok {
					t.Fatalf("provider=%T", provider)
				}
			} else if _, ok := provider.(*research.LocalPrimarySearch); !ok {
				t.Fatalf("provider=%T", provider)
			}
			health := provider.(interface {
				HealthSnapshot() []research.ProviderOutcome
			}).HealthSnapshot()
			want := 2
			if ddg {
				want++
			}
			if len(health) != want {
				t.Fatalf("health=%+v", health)
			}
			for _, outcome := range health {
				if outcome.State != "configured" || outcome.Code != "provider_not_checked" {
					t.Fatalf("health=%+v", health)
				}
			}
		}
	}
}

func TestConfiguredSearchProviderPutsAgentSearcherFirst(t *testing.T) {
	cfg := config.Config{AgentSearcherURL: "http://127.0.0.1:8484", AgentSearcherDepth: "advanced", AgentSearcherTimeoutSeconds: 45, SearXNGURL: "http://127.0.0.1:8081", SearchEnableDDG: true, FetchTempDir: filepath.Join(t.TempDir(), "fetch-temp")}
	provider, err := configuredSearchProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	primary, ok := provider.(*research.LocalPrimarySearch)
	if !ok {
		t.Fatalf("provider=%T", provider)
	}
	health := primary.HealthSnapshot()
	if len(health) != 3 || health[0].Provider != "agentsearcher" || health[1].Provider != "searxng" || health[2].Provider != "duckduckgo" {
		t.Fatalf("provider order = %+v", health)
	}
	if research.SearchTimeBudget(provider) < 45*time.Second {
		t.Fatalf("search budget too short for agentSearcher: %v", research.SearchTimeBudget(provider))
	}
	cfg.AgentSearcherURL = "http://10.1.2.3:8484"
	if _, err := configuredSearchProvider(cfg); err == nil {
		t.Fatal("non-loopback agentSearcher accepted")
	}
}
