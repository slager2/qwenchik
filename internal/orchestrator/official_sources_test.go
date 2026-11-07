package orchestrator

import (
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestOfficialFirstRanksProgrammeAndGovernmentSites(t *testing.T) {
	hits := []research.SearchHit{
		{URL: "https://scholarship-blog.example/turkiye-burslari-guide", Query: "Türkiye Burslari 2026 deadline"},
		{URL: "https://www.turkiyeburslari.gov.tr/announcements/2026", Query: "Türkiye Burslari 2026 deadline"},
		{URL: "https://www.gov.kz/news/1", Query: "Türkiye Burslari 2026 deadline"},
		{URL: "https://another-blog.example/post", Query: "Türkiye Burslari 2026 deadline"},
	}
	urls := []string{}
	for _, h := range hits {
		urls = append(urls, h.URL)
	}
	got := officialFirst(hits, urls)
	want := []string{"https://www.turkiyeburslari.gov.tr/announcements/2026", "https://www.gov.kz/news/1", "https://scholarship-blog.example/turkiye-burslari-guide", "https://another-blog.example/post"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
	for host, official := range map[string]bool{"https://studyinkorea.go.kr/x": true, "https://www.ox.ac.uk/a": true, "https://mit.edu/a": true, "https://education.ec.europa.eu/x": true, "https://govtrack-fan.example/x": false, "https://scribd.com/x": false} {
		if (officialSourceScore(host, "") >= 2) != official {
			t.Errorf("%s official=%v", host, !official)
		}
	}
}
