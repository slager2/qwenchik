package provenance

import (
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestCanonicalURLNormalizesHostTrackingAndFragment(t *testing.T) {
	canonical, err := CanonicalURL("HTTPS://EXAMPLE.COM:443/path?utm_source=news&b=2&a=1#section")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "https://example.com/path?a=1&b=2" {
		t.Fatalf("canonical URL = %q", canonical)
	}
}

func TestBuildEvidenceSelectsContiguousQueryRelevantExcerpt(t *testing.T) {
	text := strings.Join([]string{
		"Navigation: Home | Main Menu | Language options",
		"NVMe over Fabrics exposes storage through queues over a network fabric.",
		"Queue pairs allow hosts and controllers to exchange commands; measured throughput reached 12 GB/s in a PCIe 4.0 configuration.",
		"Limitations include discovery setup, transport-specific recovery, and congestion under load.",
		strings.Repeat("Additional general protocol information without performance details. ", 40),
	}, "\n")
	evidence, err := BuildEvidence("task", "NVMe over Fabrics queue pairs throughput limitations", research.FetchedDocument{
		OriginalURL: "https://example.org/protocol", FinalURL: "https://example.org/protocol", ContentSHA256: "content-hash",
	}, research.ExtractedDocument{Title: "Protocol documentation", Text: text, Method: "local_html_parser"}, "raw", "text")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidence.Excerpt, "Queue pairs") || !strings.Contains(evidence.Excerpt, "12 GB/s") || strings.Contains(evidence.Excerpt, "Navigation:") {
		t.Fatalf("evidence excerpt did not prioritize the query-relevant source passage: %q", evidence.Excerpt)
	}
	if len(evidence.QuoteSpans) != 1 || evidence.QuoteSpans[0].Text != evidence.Excerpt || evidence.QuoteSpans[0].End != len([]rune(evidence.Excerpt)) {
		t.Fatalf("quote span does not match the selected source excerpt: %#v", evidence.QuoteSpans)
	}
}

func TestBuildEvidenceCarriesHashTaintQuotesAndArtifactReferences(t *testing.T) {
	evidence, err := BuildEvidence("task_01", "original query", research.FetchedDocument{
		Provider: "direct_http", OriginalURL: "https://EXAMPLE.com/page?utm_source=x#top",
		FinalURL: "https://example.com/page?utm_source=x#top", HTTPStatus: 200,
		ContentSHA256: "body-hash", RetrievedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}, research.ExtractedDocument{Title: "Source", Text: "Evidence excerpt", Method: "local_html_parser", Taint: research.TaintExternal}, "raw-id", "text-id")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.CanonicalURL != "https://example.com/page" || evidence.RawArtifactID != "raw-id" || evidence.TextArtifactID != "text-id" || evidence.Taint != research.TaintExternal {
		t.Fatalf("unexpected evidence record: %#v", evidence)
	}
	if len(evidence.QuoteSpans) != 1 || evidence.QuoteSpans[0].Text != "Evidence excerpt" {
		t.Fatalf("quote provenance missing: %#v", evidence.QuoteSpans)
	}
}
