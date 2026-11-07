package provenance

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// callPDF mimics a call-for-applications PDF: a table of contents, the
// deadline on one page and eligibility several pages later.
func callPDF() (string, []string) {
	toc := "Contents\nIntroduction .......... 1\nDeadlines .......... 4\nEligibility .......... 9\nApplication .......... 12\n"
	filler := strings.Repeat("General background about the programme and its partner institutions. ", 60) + "\n"
	deadline := "The application deadline for the 2026 call is 15 January 2026, 14:00 CET. Late applications are not accepted."
	eligibility := "Applicants must hold a bachelor's degree and be citizens of a partner country. Applicants younger than 18 are not eligible."
	text := "Page 1\n\n" + toc + filler + "Page 4\n\n" + filler + deadline + " " + filler + "Page 9\n\n" + filler + eligibility + "\n" + filler
	chunks := []string{
		"Contents Introduction .......... 1 Deadlines .......... 4 Eligibility .......... 9",
		"The application deadline for the 2026 call is 15 January 2026, 14:00 CET.",
		"Applicants must hold a bachelor's degree and be citizens of a partner country.",
	}
	return text, chunks
}

func TestSelectMultiSpanExcerptKeepsSpreadFactsAndSkipsTOC(t *testing.T) {
	text, chunks := callPDF()
	excerpt, spans, ok := SelectMultiSpanExcerpt(text, chunks, MaxExcerptSpans, ExtractExcerptRunes)
	if !ok || len(spans) != 2 {
		t.Fatalf("ok=%v spans=%#v", ok, spans)
	}
	if strings.Contains(excerpt, "..........") {
		t.Fatalf("table of contents kept: %q", excerpt)
	}
	for _, want := range []string{"15 January 2026", "Late applications are not accepted.", "bachelor's degree", "younger than 18"} {
		if !strings.Contains(excerpt, want) {
			t.Fatalf("excerpt lacks %q: %q", want, excerpt)
		}
	}
	runes := []rune(text)
	for i, span := range spans {
		if string(runes[span.Start:span.End]) != span.Text {
			t.Fatalf("span %d is not exact", i)
		}
		if i > 0 && span.Start <= spans[i-1].End {
			t.Fatal("spans are not in document order")
		}
	}
	if !QuoteWithinExcerpt(excerpt, "15 January 2026, 14:00 CET") || QuoteWithinExcerpt(excerpt, "accepted."+ExcerptSpanSeparator) {
		t.Fatal("quote check across spans")
	}
}

func TestSelectMultiSpanExcerptRespectsBudgetAndMergesOverlaps(t *testing.T) {
	text, chunks := callPDF()
	excerpt, spans, ok := SelectMultiSpanExcerpt(text, chunks, MaxExcerptSpans, 400)
	if !ok || utf8.RuneCountInString(excerpt) > 400 || len(spans) == 0 {
		t.Fatalf("budget: ok=%v len=%d spans=%d", ok, utf8.RuneCountInString(excerpt), len(spans))
	}
	sentence := "The application deadline for the 2026 call is 15 January 2026, 14:00 CET."
	_, spans, _ = SelectMultiSpanExcerpt(text, []string{sentence, sentence + " Late applications are not accepted."}, MaxExcerptSpans, ExtractExcerptRunes)
	if len(spans) != 1 {
		t.Fatalf("overlapping chunks were not merged: %#v", spans)
	}
	if _, _, ok := SelectMultiSpanExcerpt(text, []string{"nothing like this occurs in the stored page text at all"}, 5, 8000); ok {
		t.Fatal("unmatched chunk located")
	}
}

func TestTableOfContentsNoise(t *testing.T) {
	for text, want := range map[string]bool{
		"Intro .......... 1\nScope .......... 2\nTerms .......... 3":                           true,
		"1. Introduction 3\n2. Eligibility 5\n3. Deadlines 7\n4. Contacts 9":                   true,
		"Содержание\nВведение 3\nСроки подачи 5":                                               true,
		"The deadline is 15 January 2026. Applicants must be at least 18 years old on 1 Sept.": false,
	} {
		if got := TableOfContentsNoise(text); got != want {
			t.Fatalf("%q: %v", text, got)
		}
	}
}

func TestFitExcerptBytesKeepsEverySpan(t *testing.T) {
	first, second := strings.Repeat("а", 3000), strings.Repeat("б", 3000)
	excerpt := first + ExcerptSpanSeparator + second
	fitted := FitExcerptBytes(excerpt, 6000)
	parts := ExcerptSpanTexts(fitted)
	if len(fitted) > 6000 || len(parts) != 2 || !strings.HasPrefix(first, parts[0]) || !strings.HasPrefix(second, parts[1]) || parts[1] == "" {
		t.Fatalf("fitted parts: %d bytes, %d parts", len(fitted), len(parts))
	}
	if got := FitExcerptBytes("short", 100); got != "short" {
		t.Fatal(got)
	}
	if got := FitExcerptBytes(strings.Repeat("x", 50), 10); got != strings.Repeat("x", 10) {
		t.Fatal(got)
	}
}

func TestExtractEvidenceTextAppendsOnlyMissingChunks(t *testing.T) {
	text, chunks := callPDF()
	beyond := "Scholarship holders receive a monthly stipend of 43 700 HUF and free tuition."
	stored := ExtractEvidenceText(text, append(chunks, beyond))
	if stored != strings.TrimRight(text, "\n")+ExcerptSpanSeparator+beyond {
		t.Fatalf("stored tail = %q", stored[len(stored)-200:])
	}
}
