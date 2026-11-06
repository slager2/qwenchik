package orchestrator

import "testing"

// Regression: the live 4B critic answered evidence_id `{"evidence_id": "2"}`,
// which resolved to no source and dropped every supported claim.
func TestReportCriticSchemaRestrictsEvidenceIDs(t *testing.T) {
	schema := reportCriticSchema(nil, 4, []string{"1", "2"})
	var review reportCitationReview
	good := `{"claims":[{"claim":"x","evidence_id":"2","quote":"exact quote","kind":"supported"}],"criteria_covered":[]}`
	if err := validateStructuredOutput(schema, good, &review); err != nil {
		t.Fatalf("valid alias rejected: %v", err)
	}
	bad := `{"claims":[{"claim":"x","evidence_id":"{\"evidence_id\": \"2\"}","quote":"exact quote","kind":"supported"}],"criteria_covered":[]}`
	if err := validateStructuredOutput(schema, bad, &review); err == nil {
		t.Fatal("nested evidence_id object accepted")
	}
	// Persisted reviews hold resolved UUIDs and keep the unrestricted schema.
	if err := validateStructuredOutput(reportReviewSchema(nil, 4), `{"claims":[{"claim":"x","evidence_id":"0b9a3f7e-1111-4222-8333-444455556666","quote":"exact quote","kind":"supported"}],"criteria_covered":[]}`, &review); err != nil {
		t.Fatalf("persisted review schema changed: %v", err)
	}
}
