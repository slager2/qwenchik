package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestLabHoldResourcePausesDefaultsAndOfflineValidation(t *testing.T) {
	isolatedResearchEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LabHoldResourcePauses || cfg.ContextProbeEnabled {
		t.Fatal("laboratory holds and context probes must default off")
	}
	t.Setenv("LAB_HOLD_RESOURCE_PAUSES", "invalid")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LAB_HOLD_RESOURCE_PAUSES") {
		t.Fatalf("invalid boolean: %v", err)
	}
	t.Setenv("LAB_HOLD_RESOURCE_PAUSES", "true")
	for _, mode := range []string{domain.PrivacyExternalSearch, domain.PrivacyLocalSearXNG} {
		t.Setenv("RESEARCH_PRIVACY_MODE", mode)
		t.Setenv("SEARXNG_URL", "http://127.0.0.1:8081")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LAB_HOLD_RESOURCE_PAUSES requires RESEARCH_PRIVACY_MODE=offline") {
			t.Fatalf("hold accepted in %s: %v", mode, err)
		}
	}
	corpus := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(corpus, []byte(`{"documents":[{"url":"https://docs.example.org/source","title":"Source","text":"Imported text."}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESEARCH_PRIVACY_MODE", domain.PrivacyOffline)
	t.Setenv("RESEARCH_OFFLINE_CORPUS", corpus)
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LabHoldResourcePauses || cfg.ContextProbeEnabled {
		t.Fatal("offline hold must not enable the context load endpoint")
	}
	cfg.ResearchPrivacyMode = domain.PrivacyExternalSearch
	if err := cfg.Validate(); err == nil {
		t.Fatal("direct config validation accepted ordinary laboratory holds")
	}
}
