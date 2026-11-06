package orchestrator

import (
	"strings"
	"testing"
)

const (
	reportTestFirst  = "11111111-1111-4111-8111-111111111111"
	reportTestSecond = "22222222-2222-4222-8222-222222222222"
)

func reportTestCatalogue() ([]citationEvidence, *evidenceAliases) {
	catalogue := []citationEvidence{{EvidenceID: reportTestFirst, Excerpt: "Python and R are the core languages for bioinformatics."}, {EvidenceID: reportTestSecond, Excerpt: "Learning Linux basics takes about one month."}}
	aliases := newNumberedEvidenceAliases([]string{reportTestFirst, reportTestSecond})
	return catalogue, &aliases
}

func TestReportDraftAcceptsListItemsBoldAndMultipleMarkers(t *testing.T) {
	catalogue, aliases := reportTestCatalogue()
	draft := "Основные языки — **Python** и R [1].\n\n- Linux осваивают примерно за месяц [2].\n* Оба навыка нужны с самого начала [1][2].\n2) Python применяют в анализе данных [1]."
	units, problems := parseReportDraft(draft, reportCatalogueResolver(catalogue, aliases), 4)
	if len(problems) != 0 || len(units) != 4 {
		t.Fatalf("units=%+v problems=%v", units, problems)
	}
	if units[0].Prefix != "" || units[0].Claim != "Основные языки — **Python** и R." || units[0].EvidenceIDs[0] != reportTestFirst {
		t.Fatalf("paragraph unit = %+v", units[0])
	}
	if units[2].Prefix != "- " || len(units[2].EvidenceIDs) != 2 || units[2].EvidenceIDs[1] != reportTestSecond {
		t.Fatalf("multi-marker list unit = %+v", units[2])
	}
	body := renderReportUnits(units)
	want := "Основные языки — **Python** и R. [" + reportTestFirst + "]\n\n- Linux осваивают примерно за месяц. [" + reportTestSecond + "]\n- Оба навыка нужны с самого начала. [" + reportTestFirst + "][" + reportTestSecond + "]\n2) Python применяют в анализе данных. [" + reportTestFirst + "]"
	if body != want {
		t.Fatalf("canonical body is not list-shaped with evidence IDs: %q", body)
	}
	if parsed, err := parseReportBody(body, catalogue, 4); err != nil || len(parsed) != 4 {
		t.Fatalf("canonical body does not round-trip: %v %+v", err, parsed)
	}
}

func TestReportDraftDropsInvalidLinesIndividually(t *testing.T) {
	catalogue, aliases := reportTestCatalogue()
	draft := "## Heading\nUncited sentence.\nA link https://x.example [1]\nUnknown source [7]\nMarker in the middle [1] then text\nKept fact [2]"
	units, problems := parseReportDraft(draft, reportCatalogueResolver(catalogue, aliases), 4)
	if len(units) != 1 || units[0].Claim != "Kept fact" || len(problems) != 5 {
		t.Fatalf("units=%+v problems=%v", units, problems)
	}
	units, problems = parseReportDraft("A [1]\nB [1]\nC [2]", reportCatalogueResolver(catalogue, aliases), 2)
	if len(units) != 2 || len(problems) != 1 {
		t.Fatalf("claim limit must keep the first units: %+v %v", units, problems)
	}
	if message := reportDraftProblem("No markers here.", reportCatalogueResolver(catalogue, aliases), 4); !strings.Contains(message, "[1]") {
		t.Fatalf("correction message is not precise: %q", message)
	}
}

func TestReportBodyAcceptsLegacyEvidenceIDParagraphs(t *testing.T) {
	catalogue, _ := reportTestCatalogue()
	legacy := "Python and R are core languages. [" + reportTestFirst + "]\n\nLinux takes a month [" + reportTestSecond + "]"
	units, err := parseReportBody(legacy, catalogue, 4)
	if err != nil || len(units) != 2 {
		t.Fatalf("legacy body rejected: %v", err)
	}
	review := []citationClaim{{Claim: "Python and R are core languages.", EvidenceID: reportTestFirst, Quote: "Python and R are the core languages", Kind: "supported"}, {Claim: "Linux takes a month", EvidenceID: reportTestSecond, Quote: "about one month", Kind: "supported"}}
	if err := validateReportReview(units, review, catalogue); err != nil {
		t.Fatalf("legacy review rejected: %v", err)
	}
	if _, err := parseReportBody("Uncited\n\nFact ["+reportTestFirst+"]", catalogue, 4); err == nil {
		t.Fatal("persisted body with an uncited line was accepted")
	}
}

func TestReportReviewKeepsOnlySupportedUnits(t *testing.T) {
	catalogue, aliases := reportTestCatalogue()
	units, _ := parseReportDraft("- Python и R — основные языки [1]\n- Linux учат за месяц [2]\n- Выдуманный факт [1]", reportCatalogueResolver(catalogue, aliases), 4)
	claims := []citationClaim{
		{Claim: "Python и R — основные языки", EvidenceID: reportTestFirst, Quote: "Python and R are the core languages", Kind: "supported"},
		{Claim: "Linux учат  за месяц (rephrased)", EvidenceID: reportTestSecond, Quote: "about one month", Kind: "supported"},
		{Claim: "Выдуманный факт", EvidenceID: reportTestFirst, Quote: "", Kind: "unknown"},
	}
	kept, keptClaims := filterReportReview(units, claims, catalogue)
	if len(kept) != 2 || kept[1].Claim != "Linux учат за месяц" || keptClaims[1].Claim != kept[1].Claim {
		t.Fatalf("kept=%+v claims=%+v", kept, keptClaims)
	}
	if err := validateReportReview(kept, keptClaims, catalogue); err != nil {
		t.Fatalf("kept units do not re-validate: %v", err)
	}
	// A quote from a source the unit does not cite is not support.
	wrong := []citationClaim{{Claim: units[0].Claim, EvidenceID: reportTestSecond, Quote: "about one month", Kind: "supported"}}
	if kept, _ := filterReportReview(units[:1], wrong, catalogue); len(kept) != 0 {
		t.Fatal("claim kept with a quote from an uncited source")
	}
}
