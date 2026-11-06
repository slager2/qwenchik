package provenance

import (
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestBuildEvidenceUsesSearchPassagesToSkipBoilerplate(t *testing.T) {
	boilerplate := "Email Bluesky Facebook LinkedIn Reddit Share\nFact-checked by Editorial Team\nNewsletter: sign up for bioinformatics roadmap news\n"
	article := "A practical bioinformatics roadmap starts with Python and R, then moves to sequence alignment with BLAST.\n" +
		"Next, learners study genome assembly, variant calling and RNA-seq differential expression.\n" +
		"Finally, workflow managers such as Snakemake and Nextflow make analyses reproducible."
	text := strings.Repeat(boilerplate, 30) + article + "\n" + strings.Repeat(boilerplate, 30)
	// agentSearcher text differs in whitespace, case and punctuation.
	passages := []string{"a practical bioinformatics roadmap starts with Python and R, then moves to sequence alignment with BLAST ... Next, learners study genome assembly, variant calling and RNA-seq differential expression."}
	evidence, err := BuildEvidenceWithPassages("task", "bioinformatics roadmap", research.FetchedDocument{
		OriginalURL: "https://example.org/roadmap", FinalURL: "https://example.org/roadmap", ContentSHA256: "hash",
	}, research.ExtractedDocument{Title: "Roadmap", Text: text, Method: "local_html_parser"}, "raw", "text", passages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidence.Excerpt, "sequence alignment with BLAST") || !strings.Contains(evidence.Excerpt, "differential expression") || strings.Contains(evidence.Excerpt, "Bluesky") {
		t.Fatalf("excerpt was not located by the passages: %q", evidence.Excerpt)
	}
	span := evidence.QuoteSpans[0]
	if span.Text != evidence.Excerpt || string([]rune(text)[span.Start:span.End]) != evidence.Excerpt || span.CoordinateSpace != "extracted_text_runes" {
		t.Fatalf("quote span is not a verbatim slice: %#v", span)
	}
	// Without passages the lexical selector still picks a query match.
	lexical, err := BuildEvidence("task", "bioinformatics roadmap", research.FetchedDocument{
		OriginalURL: "https://example.org/roadmap", FinalURL: "https://example.org/roadmap", ContentSHA256: "hash",
	}, research.ExtractedDocument{Title: "Roadmap", Text: text, Method: "local_html_parser"}, "raw", "text")
	if err != nil || lexical.Excerpt == "" || lexical.Excerpt == evidence.Excerpt {
		t.Fatalf("lexical fallback changed: %q err=%v", lexical.Excerpt, err)
	}
}

func TestSelectPassageExcerptWithSpanBoundsAndFallback(t *testing.T) {
	text := strings.Repeat("меню вход регистрация\n", 50) + "Геномика изучает структуру и функции генома организмов. Секвенирование нового поколения 😀 ускорило работу.\n" + strings.Repeat("подвал сайта\n", 50)
	for _, tc := range []struct {
		name     string
		passages []string
		limit    int
		ok       bool
	}{
		{"match", []string{"Геномика изучает структуру и функции генома организмов"}, 4000, true},
		{"tight limit", []string{"Геномика изучает структуру и функции генома организмов. Секвенирование нового поколения ускорило работу"}, 60, true},
		{"absent", []string{"квантовая хромодинамика описывает сильное взаимодействие кварков"}, 4000, false},
		{"too short", []string{"Геномика изучает"}, 4000, false},
		{"none", nil, 4000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			excerpt, start, end, ok := SelectPassageExcerptWithSpan(text, tc.passages, tc.limit)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v (%q)", ok, tc.ok, excerpt)
			}
			if !ok {
				return
			}
			runes := []rune(text)
			if start < 0 || end > len(runes) || end <= start || end-start > tc.limit || string(runes[start:end]) != excerpt {
				t.Fatalf("invalid span %d:%d %q", start, end, excerpt)
			}
			if !strings.Contains(excerpt, "Геномика") || strings.Contains(excerpt, "меню") {
				t.Fatalf("excerpt %q", excerpt)
			}
		})
	}
}
