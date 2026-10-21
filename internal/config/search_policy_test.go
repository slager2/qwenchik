package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSearchProviderPolicyLoad(t *testing.T) {
	for _, value := range []string{"default", "local_primary", "parallel", "", "unknown"} {
		t.Run(value, func(t *testing.T) {
			isolatedResearchEnv(t)
			t.Setenv("SEARCH_PROVIDER_POLICY", value)
			if value == "default" {
				if err := os.Unsetenv("SEARCH_PROVIDER_POLICY"); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load()
			if value == "" || value == "unknown" {
				if err == nil || !strings.Contains(err.Error(), "SEARCH_PROVIDER_POLICY") {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := value
			if value == "default" {
				want = "local_primary"
			}
			if cfg.SearchProviderPolicy != want {
				t.Fatalf("policy=%q", cfg.SearchProviderPolicy)
			}
			before := cfg.ResearchPolicies()
			cfg.SearchProviderPolicy = "parallel"
			if !reflect.DeepEqual(before, cfg.ResearchPolicies()) {
				t.Fatal("startup strategy changed immutable profiles")
			}
			cfg.SearchProviderPolicy = "invalid"
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "SEARCH_PROVIDER_POLICY") {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}
