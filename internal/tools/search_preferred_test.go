package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestSearchPassesPreferredDomainsRecencyAndLanguage(t *testing.T) {
	var captured research.SearchOptions
	registry := NewRegistry()
	fetcher := &validationFetchStub{validate: func(context.Context, string) error { return nil }}
	if err := RegisterResearchTools(registry, ResearchTools{SearchProvider: searchStub{url: "https://stipendiumhungaricum.hu/apply/", options: &captured}, Fetcher: fetcher}); err != nil {
		t.Fatal(err)
	}
	ctx := WithTaskID(context.Background(), "preferred")
	if _, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(`{"query":"Stipendium Hungaricum 2026 deadline","recency":"year","language":"en","preferred_domains":["StipendiumHungaricum.hu","stipendiumhungaricum.hu","tempus.hu"]}`)); err != nil {
		t.Fatal(err)
	}
	if captured.Recency != "year" || captured.Language != "en" || len(captured.Domains) != 0 || len(captured.PreferredDomains) != 2 || captured.PreferredDomains[0] != "stipendiumhungaricum.hu" || captured.PreferredDomains[1] != "tempus.hu" {
		t.Fatalf("search options = %#v", captured)
	}
	for _, bad := range []string{`{"query":"x y","preferred_domains":["https://evil.example/path"]}`, `{"query":"x y","preferred_domains":["a.hu","b.hu","c.hu","d.hu","e.hu","f.hu","g.hu","h.hu","i.hu"]}`} {
		if _, err := registry.Execute(ctx, "researcher", "search", json.RawMessage(bad)); !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("%s: err=%v", bad, err)
		}
	}
}
