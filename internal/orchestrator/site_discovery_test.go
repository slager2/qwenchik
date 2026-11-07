package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
)

// officialSearchFixture adds an official (.gov) hit to every search.
type officialSearchFixture struct{ adaptiveSearchFixture }

func (s *officialSearchFixture) Search(ctx context.Context, options research.SearchOptions) ([]research.SearchHit, error) {
	hits, err := s.adaptiveSearchFixture.Search(ctx, options)
	return append(hits, research.SearchHit{Provider: "searxng", Query: options.Query, URL: "https://www.protocol.gov/overview", Title: "Protocol programme", Snippet: "Official overview", Taint: research.TaintExternal}), err
}

type siteMapperFixture struct {
	mu    sync.Mutex
	roots []string
	instr []string
}

func (m *siteMapperFixture) Map(_ context.Context, rawURL, instructions string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roots = append(m.roots, rawURL)
	m.instr = append(m.instr, instructions)
	return []string{"https://www.protocol.gov/", "https://www.protocol.gov/about/team", "https://www.protocol.gov/apply/2026-call-for-applications-deadline", "https://www.protocol.gov/news/2019-call-for-applications"}, nil
}

func (*siteMapperFixture) Budget() time.Duration { return time.Second }

func TestLaterRoundMapsOfficialSiteAndFetchesDiscoveredPageFirst(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	profile := domain.ResearchProfileOvernight
	f.submit(t, "Research protocol architecture and deployment limitations.", func(s *domain.ResearchSession) {
		s.Profile, s.Policy.Profile, s.Policy.Version = profile, profile, 2
		s.Policy.AdaptiveEnabled, s.Policy.SectionedReportEnabled = true, true
		s.Budget, _ = domain.RoundResearchBudgetForProfile(profile)
		s.Budget.WallTimeSeconds = 6 * 3600
		s.Policy.MaxContinuations = s.Budget.MaxRounds - 1
		s.Policy.Budget = s.Budget
	})
	files, err := artifacts.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	search, fetch, mapper := &officialSearchFixture{}, &adaptiveFetchFixture{}, &siteMapperFixture{}
	r := tools.NewRegistry()
	if err := tools.RegisterResearchTools(r, tools.ResearchTools{SearchProvider: search, Fetcher: fetch, Extractor: researchExtractorStub{}, Artifacts: files, Repository: f.store, SiteMapper: mapper}); err != nil {
		t.Fatal(err)
	}
	reviews := 0
	w := New(f.store, llm.NewGuardedClient(explorationModelFixture(t, &reviews, true)), r, Options{ReportArtifacts: files})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	if len(mapper.roots) != 1 || mapper.roots[0] != "https://www.protocol.gov/" || !strings.Contains(mapper.instr[0], "call for applications") {
		t.Fatalf("map calls roots=%q instructions=%q", mapper.roots, mapper.instr)
	}
	state, err := w.currentCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(state.MappedSites, "protocol.gov") || len(state.SiteDiscoveryRounds) == 0 || state.SiteDiscoveryRounds[0] != 2 {
		t.Fatalf("discovery state sites=%q rounds=%v", state.MappedSites, state.SiteDiscoveryRounds)
	}
	evidence, err := f.store.Evidence(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	fetchedCall, fetchedOld := false, false
	for _, item := range evidence {
		fetchedCall = fetchedCall || item.CanonicalURL == "https://www.protocol.gov/apply/2026-call-for-applications-deadline"
		fetchedOld = fetchedOld || strings.Contains(item.CanonicalURL, "2019")
	}
	if !fetchedCall || fetchedOld {
		t.Fatalf("discovered call page fetched=%v, outdated page fetched=%v", fetchedCall, fetchedOld)
	}
}

func TestDiscoveredFetchURLsRoundRobinAcrossSites(t *testing.T) {
	results := map[string]any{
		"discover_2_1": []research.SearchHit{{Provider: tools.SiteMapProvider, URL: "https://a.gov/1"}, {Provider: tools.SiteMapProvider, URL: "https://a.gov/2"}},
		"discover_2_2": []any{map[string]any{"provider": tools.SiteMapProvider, "url": "https://b.edu/1"}},
		"search_1":     []research.SearchHit{{Provider: "searxng", URL: "https://c.com/"}},
	}
	got := discoveredFetchURLs(results, map[string]struct{}{"https://a.gov/1": {}}, 3)
	if strings.Join(got, " ") != "https://a.gov/2 https://b.edu/1" {
		t.Fatalf("got %q", got)
	}
	if instructions := siteDiscoveryInstructions("Сравни Stipendium Hungaricum и GKS: дедлайны", "https://stipendiumhungaricum.hu", nil, searchParamsNow); instructions != "Stipendium Hungaricum deadline 2026 call for applications guidelines eligibility deadline PDF" {
		t.Fatalf("instructions = %q", instructions)
	}
}
