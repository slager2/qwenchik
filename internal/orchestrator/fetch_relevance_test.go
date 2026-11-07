package orchestrator

import (
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestFetchCandidateRelevanceForLiveKazakhstanGoal(t *testing.T) {
	terms := goalRelevanceTerms(kzITGrantGoal)
	for _, test := range []struct {
		url, title string
		want       bool
	}{
		// Off-topic hits the live run fetched.
		{"https://ieeecompsac.computer.org/call-for-papers/", "CALL FOR PAPERS", false},
		{"https://ieeecompsac.computer.org/call-for-workshops/", "Call for Workshop Proposals", false},
		{"https://conf.researchr.org/home/fse-2027", "FSE 2027 - conf.researchr.org", false},
		// On-topic hits.
		{"https://www.opportunitynest.org/guides/turkiye-burslari-scholarship", "Türkiye Bursları (Türkiye Scholarships) 2027: Coverage, Eligibility", true},
		{"https://www.globaladmissions.com/scholarships-for/mongolia", "Global Korea Scholarship (GKS) 2027", true},
		{"https://www.studyinkorea.go.kr/en/main.do", "Study in Korea", true},
		{"https://stipendiumhungaricum.hu/apply/", "Apply", true},
		{"https://www.daad.de/en/", "DAAD - Deutscher Akademischer Austauschdienst", true},
		{"https://www.campuschina.org/", "Campus China", true},
		{"https://truescho.com/en/blog/fully-funded-bachelor-scholarships-2026", "Fully Funded Bachelor Scholarships 2026: Top 20", true},
	} {
		if got := fetchCandidateRelevant(test.url, test.title, terms); got != test.want {
			t.Errorf("%s %q: relevant=%v want %v", test.url, test.title, got, test.want)
		}
	}
	for _, term := range terms {
		if term == "computer" || term == "science" || term == "data" || term == "software" {
			t.Fatalf("generic field %q is a relevance term: %v", term, terms)
		}
	}
	if !fetchCandidateRelevant("https://conf.example.org/cfp", "Call for papers", nil) {
		t.Fatal("no terms must not gate")
	}
	english := goalRelevanceTerms("Compare Notion and Obsidian Sync pricing for small teams in 2026")
	if !fetchCandidateRelevant("https://obsidian.md/pricing", "Pricing - Obsidian", english) || fetchCandidateRelevant("https://conf.example.org/cfp", "Call for papers", english) {
		t.Fatalf("english goal terms = %v", english)
	}
}

func TestAvailableFetchURLsDefersOffTopicHits(t *testing.T) {
	registry := tools.NewRegistry()
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{}); err != nil {
		t.Fatal(err)
	}
	worker := &Worker{tools: registry}
	results := map[string]any{
		"search_1": []research.SearchHit{
			{Provider: "agentsearcher", Query: "q", URL: "https://ieeecompsac.computer.org/call-for-papers/", Title: "CALL FOR PAPERS"},
			{Provider: "agentsearcher", Query: "q", URL: "https://conf.researchr.org/home/fse-2027", Title: "FSE 2027 - conf.researchr.org"},
			{Provider: "agentsearcher", Query: "q", URL: "https://www.opportunitynest.org/guides/turkiye-burslari-scholarship", Title: "Türkiye Bursları 2027"},
			{Provider: "agentsearcher", Query: "q", URL: "https://www.globaladmissions.com/gks", Title: "Global Korea Scholarship (GKS) 2027"},
		},
	}
	urls := worker.availableFetchURLs("task-relevance", kzITGrantGoal, results, nil, nil, true)
	if len(urls) != 4 || strings.Contains(urls[0]+urls[1], "computer.org") || strings.Contains(urls[0]+urls[1], "researchr") {
		t.Fatalf("off-topic hits lead: %#v", urls)
	}
	// Without a goal the order is unchanged.
	if urls := worker.availableFetchURLs("task-relevance", "", results, nil, nil, false); !strings.Contains(urls[0], "computer.org") {
		t.Fatalf("ungated order = %#v", urls)
	}
}
