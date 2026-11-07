package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

// Regression: an official 356 KB GKS guideline was rejected as
// pdf_text_limit_exceeded; long documents are now kept truncated.
func TestFetchToolTruncatesOverlongExtractedText(t *testing.T) {
	body := []byte("<html>guideline</html>")
	fetcher := &fileFetchStub{doc: research.FetchedDocument{Body: body, SizeBytes: int64(len(body)), ContentSHA256: strings.Repeat("a", 64), OriginalURL: "https://example.org/guide", FinalURL: "https://example.org/guide", ContentType: "text/html", HTTPStatus: 200, EffectiveLimitBytes: 1 << 20}}
	store, err := artifacts.New(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fileResearchRepo{}
	registry := NewRegistry()
	pdfPolicy := pdfextract.DefaultPolicy()
	pdfPolicy.MaxTextRunes = 1000
	long := strings.Repeat("Требования к кандидатам GKS. ", 200)
	extractor := fileExtractorFunc(func(context.Context, research.FetchedDocument) (research.ExtractedDocument, error) {
		return research.ExtractedDocument{Title: "GKS", Text: long, Method: "fake_html"}, nil
	})
	if err := RegisterResearchTools(registry, ResearchTools{Fetcher: fetcher, Extractor: extractor, Artifacts: store, Repository: repo, PDFPolicy: pdfPolicy}); err != nil {
		t.Fatal(err)
	}
	ctx := WithFetchByteLimit(WithTaskID(context.Background(), "task"), 1<<20)
	registry.RememberURLs("task", []research.SearchHit{{URL: "https://example.org/guide"}})
	if _, err := registry.Execute(ctx, "researcher", "fetch_url", json.RawMessage(`{"url":"https://example.org/guide"}`)); err != nil {
		t.Fatalf("long document rejected: %v", err)
	}
	if len(repo.evidence) != 1 || utf8.RuneCountInString(repo.evidence[0].Excerpt) > 1000 || !strings.HasPrefix(long, repo.evidence[0].Excerpt[:20]) {
		t.Fatalf("evidence: %#v", repo.evidence)
	}
}
