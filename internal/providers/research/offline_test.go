package research

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineCorpusNeverContactsURLsAndEnforcesBounds(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "corpus.json")
	raw, _ := json.Marshal(map[string]any{"documents": []map[string]string{{"url": server.URL + "/source", "title": "Architecture", "text": "Protocol architecture has explicit compatibility limitations."}}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadOfflineCorpus(path)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := c.Search(context.Background(), SearchOptions{Query: "protocol architecture", MaxResults: 8})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	doc, err := c.Fetch(context.Background(), hits[0].URL, FetchOptions{MaxBytes: 1024})
	if err != nil || doc.Provider != "offline_corpus" || len(c.SHA256()) != 64 {
		t.Fatalf("doc=%+v err=%v", doc, err)
	}
	if _, err = c.Fetch(context.Background(), server.URL+"/not-imported", FetchOptions{MaxBytes: 1024}); err == nil {
		t.Fatal("unknown URL admitted")
	}
	if _, err = c.Fetch(context.Background(), hits[0].URL, FetchOptions{MaxBytes: 1}); err == nil {
		t.Fatal("oversize source admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Search(ctx, SearchOptions{Query: "protocol"}); err == nil {
		t.Fatal("ignored cancellation")
	}
	if calls != 0 {
		t.Fatalf("offline leaked %d requests", calls)
	}
}

func TestOfflineCorpusRejectsMalformedAndDuplicateImports(t *testing.T) {
	for _, raw := range []string{`{"documents":[]}`, `{"documents":[{"url":"file:///etc/passwd","text":"x"}]}`, `{"documents":[{"url":"https://a.invalid/x","text":"x"},{"url":"https://a.invalid/x","text":"y"}]}`, `{"documents":[{"url":"https://a.invalid","text":"x","unknown":1}]}`, `{"documents":[{"url":"https://a.invalid","text":"x"}]} {}`} {
		p := filepath.Join(t.TempDir(), "corpus.json")
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOfflineCorpus(p); err == nil {
			t.Fatalf("accepted invalid corpus: %s", raw)
		}
	}
}
