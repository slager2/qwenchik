package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIndexedCriticKeepsUnitsWithAnyVerifiedQuote(t *testing.T) {
	catalogue := []citationEvidence{
		{EvidenceID: "a", Excerpt: "Stipendium Hungaricum covers tuition fees and a monthly stipend of 43 700 HUF."},
		{EvidenceID: "b", Excerpt: "Applications close on 15 January 2026 at 14:00 CET for the 2026/2027 year."},
	}
	units := []reportUnit{
		{Claim: "Программа покрывает обучение и платит 43 700 HUF в месяц; заявки до 15 января 2026.", EvidenceIDs: []string{"a", "b"}},
		{Claim: "Выдуманный факт.", EvidenceIDs: []string{"a"}},
		{Claim: "Только второй источник подтверждён.", EvidenceIDs: []string{"a", "b"}},
	}
	results := []reportCriticResult{
		{Index: 1, Kind: "supported", Evidence: []reportCriticEvidence{{EvidenceID: "a", Quote: "covers tuition fees and a monthly stipend of 43 700 HUF"}, {EvidenceID: "b", Quote: "Applications close on 15 January 2026"}}},
		{Index: 2, Kind: "supported", Evidence: []reportCriticEvidence{{EvidenceID: "a", Quote: "an invented quote that is not in the excerpt"}}},
		{Index: 3, Kind: "supported", Evidence: []reportCriticEvidence{{EvidenceID: "a", Quote: "not present anywhere at all"}, {EvidenceID: "b", Quote: "for the 2026/2027 year"}}},
		{Index: 3, Kind: "unknown"}, // duplicate index: first verdict wins
	}
	kept, claims := filterIndexedReview(units, results, catalogue)
	if len(kept) != 2 || len(claims) != 2 {
		t.Fatalf("kept=%+v claims=%+v", kept, claims)
	}
	if strings.Join(kept[0].EvidenceIDs, ",") != "a,b" || strings.Join(kept[1].EvidenceIDs, ",") != "b" {
		t.Fatalf("unverified sources were not pruned: %+v", kept)
	}
	if claims[1].EvidenceID != "b" || claims[1].Claim != units[2].Claim || claims[1].Kind != "supported" {
		t.Fatalf("persisted claim has the wrong shape: %+v", claims[1])
	}
	// The persisted legacy review still validates on resume.
	if err := validateReportReview(kept, claims, catalogue); err != nil {
		t.Fatalf("converted review does not validate: %v", err)
	}
}

func TestIndexedCriticSchemaDoesNotAskForClaimCopies(t *testing.T) {
	schema := string(reportIndexedCriticSchema([]string{"goal.0"}, 3, []string{"1", "2"}))
	for _, want := range []string{`"index"`, `"maxItems":3`, `"maxLength":1000`, `"enum":["1","2"]`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema lacks %s: %s", want, schema)
		}
	}
	if strings.Contains(schema, `"claim"`) {
		t.Fatalf("indexed schema still asks for claim text: %s", schema)
	}
	reply := `{"claims":[{"index":1,"kind":"supported","evidence":[{"evidence_id":"2","quote":"` + strings.Repeat("x", 900) + `"}]}],"criteria_covered":["goal.0"]}`
	var review reportCriticReview
	if err := validateStructuredOutput(json.RawMessage(schema), reply, &review); err != nil {
		t.Fatalf("valid indexed reply rejected: %v", err)
	}
	// A persisted review may carry the longer quote.
	persisted := `{"claims":[{"claim":"c","evidence_id":"id","quote":"` + strings.Repeat("x", 900) + `","kind":"supported"}],"criteria_covered":[]}`
	if err := validateStructuredOutput(reportReviewSchema([]string{"goal.0"}, 3), persisted, &reportCitationReview{}); err != nil {
		t.Fatalf("persisted 900-character quote rejected: %v", err)
	}
}

func TestCriticPinnedLegacyDetection(t *testing.T) {
	if !reportCriticPinnedLegacy(`{"claims_to_check":[{"claim":"x","evidence_id":"1"}]}`) {
		t.Fatal("pre-index pinned input was not detected as legacy")
	}
	if reportCriticPinnedLegacy(`{"claims_to_check":[],"critic_format":"indexed"}`) {
		t.Fatal("indexed pinned input was treated as legacy")
	}
	// Legacy replies keep the claim-text matching path.
	legacy := legacyCriticClaims([]reportCriticResult{{Claim: "c", EvidenceID: "a", Quote: "q", Kind: "supported"}})
	if len(legacy) != 1 || legacy[0].Claim != "c" || legacy[0].EvidenceID != "a" {
		t.Fatalf("legacy conversion lost fields: %+v", legacy)
	}
	var review reportCriticReview
	if err := validateStructuredOutput(reportCriticSchema([]string{}, 2, []string{"1"}), `{"claims":[{"claim":"c","evidence_id":"1","quote":"some quote","kind":"supported"}],"criteria_covered":[]}`, &review); err != nil || review.Claims[0].Claim != "c" {
		t.Fatalf("legacy reply does not decode into the union result: %v %+v", err, review)
	}
}
