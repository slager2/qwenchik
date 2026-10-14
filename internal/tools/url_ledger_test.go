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
	ledger.Clear("task-1")
	if ledger.Contains("task-1", "https://docs.example.com/page") {
		t.Fatal("task ledger was not cleared")
	}
}
