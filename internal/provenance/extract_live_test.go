package provenance

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/providers/research"
)

// TestLiveAgentSearcherExtract reads a bot-protected page and a PDF through
// a running local agentSearcher. Run with QAGENT_LIVE_AGENTSEARCHER=1
// (AGENTSEARCHER_URL defaults to http://127.0.0.1:8484).
func TestLiveAgentSearcherExtract(t *testing.T) {
	if os.Getenv("QAGENT_LIVE_AGENTSEARCHER") != "1" {
		t.Skip("live agentSearcher test requires QAGENT_LIVE_AGENTSEARCHER=1")
	}
	baseURL := os.Getenv("AGENTSEARCHER_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8484"
	}
	extractor, err := research.NewAgentSearcherExtractor(baseURL, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url, query, want string
		pdf              bool
	}{
		// Answers a plain Go HTTP client with 402/403.
		{"https://www.investopedia.com/terms/b/bitcoin.asp", "who created bitcoin", "Nakamoto", false},
		{"https://arxiv.org/pdf/1706.03762", "number of attention heads", "parallel attention layers", true},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), extractor.Budget())
		page, err := extractor.Extract(ctx, tc.url, tc.query)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		text := ExtractEvidenceText(page.Text, page.Chunks)
		evidence, err := BuildExtractEvidence(ExtractEvidenceInput{TaskID: "live", Query: tc.query, URL: tc.url, Text: text, Chunks: page.Chunks, PDF: page.PDF, ContentSHA256: "live", RetrievedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		span := evidence.QuoteSpans[0]
		if page.PDF != tc.pdf || string([]rune(text)[span.Start:span.End]) != evidence.Excerpt || !strings.Contains(evidence.Excerpt, tc.want) {
			t.Fatalf("%s: pdf=%v chunks=%d method=%s excerpt=%q", tc.url, page.PDF, len(page.Chunks), evidence.ExtractionMethod, evidence.Excerpt)
		}
		t.Logf("%s: %d runes text, %d chunks, method %s, excerpt %d runes", tc.url, len([]rune(text)), len(page.Chunks), evidence.ExtractionMethod, len([]rune(evidence.Excerpt)))
	}
}
