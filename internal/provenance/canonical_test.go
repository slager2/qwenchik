package provenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
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
		strings.Repeat("Additional general protocol information without performance details. ", 120),
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
	if len(evidence.QuoteSpans) != 1 || evidence.QuoteSpans[0].Start == 0 || evidence.QuoteSpans[0].Text != evidence.Excerpt || string([]rune(text)[evidence.QuoteSpans[0].Start:evidence.QuoteSpans[0].End]) != evidence.Excerpt {
		t.Fatalf("quote span does not match the selected source excerpt: %#v", evidence.QuoteSpans)
	}
}

func TestSelectExcerptWithSpanExactCoordinates(t *testing.T) {
	for _, tc := range []struct {
		name, text, query string
		limit             int
		nonzero, prefix   bool
	}{
		{"multilingual", strings.Repeat("неважное 中文 😀\n", 100) + "\n  Пропускная способность 東京 throughput & recovery\n" + strings.Repeat("другое\n", 30), "throughput recovery", 90, true, false},
		{"line boundary", strings.Repeat("preface line\n", 100) + "\n\tneedle throughput\nrecovery boundary\n" + strings.Repeat("trailer\n", 30), "needle throughput", 70, true, false},
		{"not found", "  😀中文 начало\n" + strings.Repeat("данные ", 50), "absentkeyword", 20, false, true},
		{"empty query", "\n😀 начало " + strings.Repeat("данные ", 50), "", 20, false, true},
		{"trimmed short", " \n 😀中文 & данные \t", "данные", 100, true, false},
		{"empty", "", "query", 20, false, false},
		{"disabled", "text", "query", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			excerpt, start, end := SelectExcerptWithSpan(tc.text, tc.query, tc.limit)
			runes := []rune(tc.text)
			if start < 0 || end < start || end > len(runes) || string(runes[start:end]) != excerpt || (tc.limit > 0 && end-start > tc.limit) {
				t.Fatalf("invalid exact span %d:%d %q", start, end, excerpt)
			}
			if tc.nonzero && start == 0 || tc.prefix && (start != 0 || excerpt != string(runes[:tc.limit])) {
				t.Fatalf("unexpected window %d:%d %q", start, end, excerpt)
			}
			if SelectExcerpt(tc.text, tc.query, tc.limit) != excerpt || selectEvidenceExcerpt(tc.text, tc.query, tc.limit) != excerpt {
				t.Fatal("wrapper changed selected excerpt")
			}
		})
	}
}

func TestEvidenceSpanUsesNormalizedExtractedHTML(t *testing.T) {
	fetched := research.FetchedDocument{OriginalURL: "https://example.org/source", FinalURL: "https://example.org/source", ContentType: "text/html", ContentSHA256: "hash", Body: []byte("<html><body><p>" + strings.Repeat("Unrelated introduction. ", 300) + "</p><p>Пропускная throughput &amp; recovery&nbsp;東京 &#128512; &lt;exact&gt;</p><p>" + strings.Repeat("Other details. ", 300) + "</p></body></html>")}
	extracted, err := research.NewHTMLExtractorWithPDF(20000, nil).Extract(context.Background(), fetched)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := BuildEvidence("task", "throughput recovery", fetched, extracted, "raw", "text")
	if err != nil {
		t.Fatal(err)
	}
	span := evidence.QuoteSpans[0]
	if span.Start == 0 || span.CoordinateSpace != "extracted_text_runes" || string([]rune(extracted.Text)[span.Start:span.End]) != span.Text || !strings.Contains(span.Text, "throughput & recovery 東京 😀 <exact>") {
		t.Fatalf("normalized HTML span mismatch: %#v", span)
	}
}

func TestLegacyQuoteSpanKeepsUnknownCoordinateSpace(t *testing.T) {
	var span domain.QuoteSpan
	if err := json.Unmarshal([]byte(`{"start":0,"end":6,"text":"legacy"}`), &span); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(span)
	if err != nil || span.CoordinateSpace != "" || strings.Contains(string(encoded), "coordinate_space") {
		t.Fatalf("legacy coordinates fabricated: %s %v", encoded, err)
	}
}

func TestBuildEvidenceRetainsLargeMultilingualExcerptWithRuneOffsets(t *testing.T) {
	text := strings.Repeat("Проверяемые данные подтверждают архитектурное решение и фактический результат. ", 120)
	evidence, err := BuildEvidence("task", "архитектурное решение фактический результат", research.FetchedDocument{
		OriginalURL: "https://example.org/report", FinalURL: "https://example.org/report", ContentSHA256: "large-content-hash",
	}, research.ExtractedDocument{Title: "Технический отчёт", Text: text, Method: "local_html_parser"}, "raw", "text")
	if err != nil {
		t.Fatal(err)
	}
	runeCount := utf8.RuneCountInString(evidence.Excerpt)
	if runeCount <= 2500 || runeCount > 4000 {
		t.Fatalf("large evidence excerpt has %d runes, want 2501..4000", runeCount)
	}
	if len(evidence.QuoteSpans) != 1 || evidence.QuoteSpans[0].End-evidence.QuoteSpans[0].Start != runeCount || string([]rune(text)[evidence.QuoteSpans[0].Start:evidence.QuoteSpans[0].End]) != evidence.Excerpt || evidence.QuoteSpans[0].Text != evidence.Excerpt {
		t.Fatalf("large excerpt quote offsets do not use Unicode rune indices: %#v", evidence.QuoteSpans)
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
