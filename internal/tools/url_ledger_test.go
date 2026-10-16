package tools

import "testing"

func TestURLLedgerTracksOnlyTaskKnownSources(t *testing.T) {
	ledger := NewURLLedger()
	ledger.SeedText("task-1", "Please read https://docs.example.com/page?utm_source=chat#top.")
	if !ledger.Contains("task-1", "https://docs.example.com/page") {
		t.Fatal("user-provided URL was not seeded")
	}
	if ledger.Contains("task-1", "https://other.example/page") || ledger.Contains("task-2", "https://docs.example.com/page") {
		t.Fatal("URL ledger allowed an unknown URL or crossed task boundaries")
	}
	ledger.Remember("task-1", map[string]any{"url": "https://search.example.com/result"})
	if !ledger.Contains("task-1", "https://search.example.com/result") {
		t.Fatal("search result URL was not remembered")
	}
	urls := ledger.URLs("task-1")
	if len(urls) != 2 || urls[0] != "https://docs.example.com/page" || urls[1] != "https://search.example.com/result" {
		t.Fatalf("task URL snapshot = %#v", urls)
	}
	ledger.MarkFetched("task-1", "https://search.example.com/result?utm_source=search")
	fetched := ledger.FetchedURLs("task-1")
	if len(fetched) != 1 || fetched[0] != "https://search.example.com/result" {
		t.Fatalf("fetched URL snapshot = %#v", fetched)
	}
	ledger.Clear("task-1")
	if ledger.Contains("task-1", "https://docs.example.com/page") || len(ledger.URLs("task-1")) != 0 || len(ledger.FetchedURLs("task-1")) != 0 {
		t.Fatal("task ledger was not cleared")
	}
}

func TestRegistryTracksRequestedAndFinalFetchURLsAcrossRecovery(t *testing.T) {
	registry := NewRegistry()
	registry.urlLedger = NewURLLedger()
	registry.SeedTaskURLs("task-redirect", "Fetch https://source.example/start")
	result := map[string]any{
		"evidence_id":   "evidence-1",
		"requested_url": "https://source.example/start",
		"url":           "https://source.example/final",
	}
	registry.RememberURLs("task-redirect", result)
	registry.RememberFetchedURLs("task-redirect", result)
	for _, rawURL := range []string{"https://source.example/start", "https://source.example/final"} {
		if !registry.urlLedger.Contains("task-redirect", rawURL) {
			t.Errorf("URL %s was not retained in the task ledger", rawURL)
		}
	}
	fetched := registry.FetchedTaskURLs("task-redirect")
	if len(fetched) != 2 || fetched[0] != "https://source.example/final" || fetched[1] != "https://source.example/start" {
		t.Fatalf("requested/final fetched URLs = %#v", fetched)
	}
}
