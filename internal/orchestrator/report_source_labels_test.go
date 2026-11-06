package orchestrator

import (
	"strings"
	"testing"
)

func TestReportLabelsExtractedAndPDFSourcesAsFetched(t *testing.T) {
	for _, labels := range []reportLabels{reportLabelsRU, reportLabelsEN} {
		extracted := citationEvidence{EvidenceID: "a", Extracted: true}
		pdf := citationEvidence{EvidenceID: "b", Extracted: true, PDF: true}
		passage := citationEvidence{EvidenceID: "c", SourceText: "agentsearcher_passages"}
		if note := reportSourceNote(labels, extracted); note != "" {
			t.Fatalf("extracted page labelled %q", note)
		}
		if note := reportSourceNote(labels, pdf); note != " · "+labels.PDFSource || labels.PDFSource == "" {
			t.Fatalf("PDF note = %q", note)
		}
		if note := reportSourceNote(labels, passage); note != " · "+labels.PassageSource {
			t.Fatalf("passage note = %q", note)
		}
		notes := reportMethodNotes(labels, []citationEvidence{extracted, pdf})
		if len(notes) != 1 || notes[0] != labels.ExtractMethod || strings.Contains(notes[0], labels.PassageSource) {
			t.Fatalf("extract-only methodology = %q", notes)
		}
		if notes := reportMethodNotes(labels, []citationEvidence{extracted, passage}); len(notes) != 2 || notes[1] != labels.PassageMethod {
			t.Fatalf("mixed methodology = %q", notes)
		}
		if notes := reportMethodNotes(labels, []citationEvidence{{EvidenceID: "d"}}); len(notes) != 0 {
			t.Fatalf("direct-only methodology = %q", notes)
		}
	}
}
