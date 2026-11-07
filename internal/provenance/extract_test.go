package provenance

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

func extractTestPage() (string, []string) {
	filler := strings.Repeat("Navigation menu item and related links about other topics.\n", 120)
	first := "In this work we employ h = 8 parallel attention layers, or heads. For each of these we use dk = dv = dmodel/h = 64."
	second := "Multi-head attention allows the model to jointly attend to information from different representation subspaces at different positions."
	text := filler + first + "\n" + strings.Repeat("Intermediate prose about training. ", 30) + "\n" + second + "\n" + filler
	// agentSearcher chunks re-wrap lines and differ in whitespace.
	chunks := []string{"In this work we employ h = 8 parallel attention layers,\nor heads. For each of these we use dk = dv = dmodel/h = 64.", "Multi-head attention allows the model to jointly attend to information from different representation\nsubspaces at different positions."}
	return text, chunks
}

func TestBuildExtractEvidenceKeepsRankedChunksAsVerbatimSpan(t *testing.T) {
	text, chunks := extractTestPage()
	evidence, err := BuildExtractEvidence(ExtractEvidenceInput{
		TaskID: "task", Query: "attention heads", URL: "https://arxiv.org/pdf/1706.03762", Text: text, Chunks: chunks,
		PDF: true, ContentSHA256: "hash", RawArtifactID: "raw", TextArtifactID: "txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Provider != "agentsearcher" || evidence.ExtractionMethod != ExtractPDFEvidenceMethod || evidence.HTTPStatus != 200 || len(evidence.QualityFlags) != 0 || evidence.Title != "1706.03762" {
		t.Fatalf("evidence provenance = %#v", evidence)
	}
	if !strings.Contains(evidence.Excerpt, "h = 8 parallel attention layers") || !strings.Contains(evidence.Excerpt, "jointly attend") || utf8.RuneCountInString(evidence.Excerpt) > ExtractExcerptRunes {
		t.Fatalf("excerpt does not keep both chunks: %q", evidence.Excerpt)
	}
	if len(evidence.QuoteSpans) != 2 {
		t.Fatalf("want one span per distant chunk, got %#v", evidence.QuoteSpans)
	}
	parts := []string{}
	for _, span := range evidence.QuoteSpans {
		if string([]rune(text)[span.Start:span.End]) != span.Text || span.CoordinateSpace != "extracted_text_runes" {
			t.Fatalf("span %#v is not a verbatim slice", span)
		}
		parts = append(parts, span.Text)
	}
	if strings.Join(parts, ExcerptSpanSeparator) != evidence.Excerpt {
		t.Fatalf("excerpt is not the joined spans: %q", evidence.Excerpt)
	}
	if !IsExtractEvidence(evidence) || !IsPDFEvidence(evidence) || IsPassageEvidence(evidence) {
		t.Fatal("extract evidence classification")
	}
}

func TestBuildExtractEvidenceFallsBackToSearchPassagesThenQuery(t *testing.T) {
	text, chunks := extractTestPage()
	evidence, err := BuildExtractEvidence(ExtractEvidenceInput{TaskID: "task", Query: "attention", URL: "https://example.org/a", Title: " Paper ", Text: text, Passages: chunks[1:], ContentSHA256: "hash"})
	if err != nil || !strings.Contains(evidence.Excerpt, "jointly attend") || evidence.ExtractionMethod != ExtractEvidenceMethod || evidence.Title != "Paper" {
		t.Fatalf("evidence=%#v err=%v", evidence, err)
	}
	evidence, err = BuildExtractEvidence(ExtractEvidenceInput{TaskID: "task", Query: "jointly attend representation", URL: "https://example.org/a", Text: text, ContentSHA256: "hash"})
	if err != nil || !strings.Contains(evidence.Excerpt, "jointly attend") || evidence.Title != "Navigation menu item and related links about other topics." {
		t.Fatalf("lexical evidence=%#v err=%v", evidence, err)
	}
	if _, err := BuildExtractEvidence(ExtractEvidenceInput{TaskID: "task", URL: "https://example.org/a", Text: " ", ContentSHA256: "hash"}); err == nil {
		t.Fatal("empty text accepted")
	}
}

func TestExtractEvidenceTextAppendsChunksBeyondTheCut(t *testing.T) {
	text, chunks := extractTestPage()
	if got := ExtractEvidenceText(text, chunks); got != text {
		t.Fatal("located chunks must not change the stored text")
	}
	cut := strings.Repeat("Navigation menu item and related links about other topics.\n", 50)
	stored := ExtractEvidenceText(cut, chunks)
	if !strings.HasPrefix(stored, cut[:len(cut)-1]+"\n\n[...]\n\n") || !strings.HasSuffix(stored, chunks[1]) {
		t.Fatalf("stored text = %q", stored[len(stored)-300:])
	}
	evidence, err := BuildExtractEvidence(ExtractEvidenceInput{TaskID: "task", URL: "https://example.org/a", Text: stored, Chunks: chunks, ContentSHA256: "hash"})
	if err != nil || !strings.Contains(evidence.Excerpt, "parallel attention layers") {
		t.Fatalf("evidence=%#v err=%v", evidence, err)
	}
}

func TestIsPDFEvidenceRecognisesLocalExtractors(t *testing.T) {
	for method, want := range map[string]bool{"isolated_poppler_pdfinfo_pdftotext": true, "pure_go_pdf_subprocess": true, "local_html_parser": false, ExtractEvidenceMethod: false} {
		if got := IsPDFEvidence(domain.Evidence{ExtractionMethod: method}); got != want {
			t.Fatalf("%s: %v", method, got)
		}
	}
}
