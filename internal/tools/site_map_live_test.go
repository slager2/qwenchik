package tools

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/providers/research"
)

// TestLiveSiteMapRanking maps two official sites through a running local
// agentSearcher and logs the ranked discoveries. Run with
// QAGENT_LIVE_AGENTSEARCHER=1 (AGENTSEARCHER_URL defaults to
// http://127.0.0.1:8484).
func TestLiveSiteMapRanking(t *testing.T) {
	if os.Getenv("QAGENT_LIVE_AGENTSEARCHER") != "1" {
		t.Skip("live agentSearcher test requires QAGENT_LIVE_AGENTSEARCHER=1")
	}
	baseURL := os.Getenv("AGENTSEARCHER_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8484"
	}
	mapper, err := research.NewAgentSearcherMapper(baseURL, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ root, instructions string }{
		{"https://stipendiumhungaricum.hu/", "Stipendium Hungaricum deadline requirements 2026 call for applications guidelines eligibility deadline PDF"},
		{"https://www.studyinkorea.go.kr/", "GKS scholarship deadline requirements 2026 call for applications guidelines eligibility deadline PDF"},
	} {
		started := time.Now()
		urls, err := mapper.Map(context.Background(), tc.root, tc.instructions)
		if err != nil {
			t.Fatalf("%s: %v", tc.root, err)
		}
		ranked := rankDiscoveredURLs(urls, tc.instructions, time.Now().UTC(), MaxSiteMapResults)
		if len(ranked) == 0 {
			t.Fatalf("%s: no ranked discoveries among %d", tc.root, len(urls))
		}
		t.Logf("%s: %d discovered in %s, ranked %q", tc.root, len(urls), time.Since(started).Round(time.Millisecond), ranked)
	}
}
