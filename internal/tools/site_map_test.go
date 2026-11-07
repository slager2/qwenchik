package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/providers/research"
)

type siteMapperStub struct {
	urls []string
	root string
}

func (s *siteMapperStub) Map(_ context.Context, rawURL, _ string) ([]string, error) {
	s.root = rawURL
	return s.urls, nil
}

func (s *siteMapperStub) Budget() time.Duration { return time.Minute }

func TestRankDiscoveredURLsPrefersCurrentCallPagesAndPDFs(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	urls := []string{
		"https://stipendiumhungaricum.hu/",
		"https://stipendiumhungaricum.hu/news/application-for-sh-2024/",
		"https://stipendiumhungaricum.hu/news/application-is-on-for-the-2026-2027-stipendium-hungaricum-scholarship/",
		"https://stipendiumhungaricum.hu/about/team/",
		"https://stipendiumhungaricum.hu/uploads/2026/call-for-applications.pdf",
		"https://www.studyinkorea.go.kr/ko/plan/scholarship.do?tab=gks-tab1",
		"https://www.studyinkorea.go.kr/ko/plan/scholarship.do?tab=gks-tab2",
		"https://www.studyinkorea.go.kr/ko/plan/scholarship.do?tab=gks-tab3",
	}
	got := rankDiscoveredURLs(urls, "GKS scholarship 2026 call deadline eligibility PDF", now, 5)
	want := []string{urls[4], urls[2], urls[5], urls[6]}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %q", got)
	}
}

func TestMapSiteValidatesURLsAndRemembersTargets(t *testing.T) {
	mapper := &siteMapperStub{urls: []string{"https://stipendiumhungaricum.hu/apply/call-2026.pdf", "http://10.0.0.1/admin/apply-2026", "https://stipendiumhungaricum.hu/news/deadline-2026/"}}
	fetcher := &validationFetchStub{validate: func(_ context.Context, rawURL string) error {
		if strings.Contains(rawURL, "10.0.0.1") {
			return research.ErrUnsafeURL
		}
		return nil
	}}
	registry := NewRegistry()
	if err := RegisterResearchTools(registry, ResearchTools{Fetcher: fetcher, SiteMapper: mapper}); err != nil {
		t.Fatal(err)
	}
	ctx := WithTaskID(context.Background(), "map-task")
	result, err := registry.Execute(ctx, "researcher", "map_site", json.RawMessage(`{"url":"https://stipendiumhungaricum.hu/","instructions":"2026 call deadline"}`))
	if err != nil {
		t.Fatal(err)
	}
	hits := result.([]research.SearchHit)
	if len(hits) != 2 || hits[0].Provider != SiteMapProvider || hits[0].Query != "2026 call deadline" || hits[0].Title != "call 2026 pdf" {
		t.Fatalf("hits = %#v", hits)
	}
	if !registry.urlLedger.Contains("map-task", hits[1].URL) || registry.urlLedger.Contains("map-task", "http://10.0.0.1/admin/apply-2026") {
		t.Fatal("ledger did not record exactly the validated discoveries")
	}
	if _, err := registry.Execute(ctx, "researcher", "map_site", json.RawMessage(`{"url":"http://10.0.0.1/"}`)); !errors.Is(err, research.ErrUnsafeURL) && !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("unsafe root err = %v", err)
	}
	restricted := NewRegistry()
	if err := RegisterResearchTools(restricted, ResearchTools{Fetcher: fetcher, SiteMapper: mapper, AllowedDomains: []string{"example.com"}}); err != nil {
		t.Fatal(err)
	}
	if restricted.Has("map_site") {
		t.Fatal("map_site registered with a domain allow-list")
	}
}
