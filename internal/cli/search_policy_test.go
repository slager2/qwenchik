package cli

import (
	"testing"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestConfiguredSearchProviderPolicy(t *testing.T) {
	for _, policy := range []string{"", "local_primary", "parallel"} {
		for _, ddg := range []bool{false, true} {
			cfg := config.Config{SearchProviderPolicy: policy, SearXNGURL: "http://127.0.0.1:8081", TavilyAPIKey: "test-key", SearchEnableDDG: ddg}
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
