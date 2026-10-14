package provenance

import (
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
