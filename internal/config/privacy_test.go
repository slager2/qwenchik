package config

import (
	"github.com/slager2/qwenchik/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivacyModesAreTrustedAndOfflineCorpusIsPinned(t *testing.T) {
	isolatedResearchEnv(t)
	t.Setenv("RESEARCH_PRIVACY_MODE", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("unknown privacy accepted")
	}
	t.Setenv("RESEARCH_PRIVACY_MODE", domain.PrivacyLocalSearXNG)
	t.Setenv("SEARXNG_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing gateway accepted")
	}
	t.Setenv("SEARXNG_URL", "http://127.0.0.1:8081")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESEARCH_PRIVACY_MODE", domain.PrivacyOffline)
	if _, err := Load(); err == nil {
		t.Fatal("missing corpus accepted")
	}
	p := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(p, []byte(`{"documents":[{"url":"https://docs.example.org/source","title":"Source","text":"Imported source text."}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESEARCH_OFFLINE_CORPUS", p)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cfg.ResearchProfile(domain.ResearchProfileStandard)
	if err != nil || policy.PrivacyMode != domain.PrivacyOffline || len(policy.OfflineCorpusSHA256) != 64 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	t.Setenv("NOTIFICATION_CHANNEL", "ntfy")
	if _, err := Load(); err == nil {
		t.Fatal("offline external notifications admitted")
	}
}
