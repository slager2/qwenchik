package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestEvidenceAliasesAreDeterministicAndRoundTrip(t *testing.T) {
	a := newEvidenceAliases([]string{"b3600000-0000-4000-8000-000000000002", "57100000-0000-4000-8000-000000000001"}, []string{"b3600000-0000-4000-8000-000000000002", ""})
	b := newEvidenceAliases([]string{"57100000-0000-4000-8000-000000000001", "b3600000-0000-4000-8000-000000000002"})
	if strings.Join(a.aliasList(a.ids), ",") != "E1,E2" || a.toAlias["57100000-0000-4000-8000-000000000001"] != b.toAlias["57100000-0000-4000-8000-000000000001"] {
		t.Fatalf("aliases are not deterministic: %#v %#v", a.toAlias, b.toAlias)
	}
	input := `{"evidence_id":"57100000-0000-4000-8000-000000000001","ids":["b3600000-0000-4000-8000-000000000002"]}`
	aliased := a.apply(input)
	if aliased != `{"evidence_id":"E1","ids":["E2"]}` || a.mentionsOriginal(aliased) || !a.mentionsOriginal(input) {
		t.Fatalf("unexpected aliased input %s", aliased)
	}
	if got := a.resolveList([]string{"E2", "unknown"}); got[0] != "b3600000-0000-4000-8000-000000000002" || got[1] != "unknown" {
		t.Fatalf("aliases did not resolve: %#v", got)
	}
}

func TestVerifierSeesEvidenceAliasesAndReturnsOriginalIDs(t *testing.T) {
	const id = "571e19f8-285f-41f1-b9c1-755a59fac957"
	plan := domain.Plan{Goal: "Inspect source", FinalDeliverable: "Supported answer", Subtasks: []domain.PlanStep{
		{ID: "fetch_1", Kind: "fetch", Description: "Inspect source", SuccessCriteria: []string{"evidence recorded"}},
	}}
	state := checkpointState{CompletedSteps: []string{"fetch_1"}, ToolResults: map[string]any{
		"fetch_1": map[string]any{"evidence_id": id, "url": "https://one.example/", "excerpt": "Source text."},
	}}
	response := `{"ready_to_answer":true,"summary":"Supported","missing":[],"supplementary_criteria":[],"criterion_assessments":{"c0":{"met":true,"evidence_ids":["E1"],"rationale":"Fetched source text is stored."}}}`
	model := &scriptedLLM{responses: []string{response}}
	worker := New(nil, model, nil, Options{MaxInputBytes: 64 << 10})
	result, err := worker.verify(context.Background(), domain.Task{Title: "Inspect source"}, "Inspect the supplied URL", "", plan, state)
	if err != nil {
		t.Fatal(err)
	}
	request := model.seen[0]
	if strings.Contains(request.Messages[1].Content, id) || !strings.Contains(request.Messages[1].Content, `"E1"`) {
		t.Fatalf("verifier input was not aliased: %s", request.Messages[1].Content)
	}
	if strings.Contains(string(request.JSONSchema), id) || !strings.Contains(string(request.JSONSchema), `"E1"`) {
		t.Fatalf("verifier schema does not constrain evidence to aliases: %s", request.JSONSchema)
	}
	if len(result.CriterionAssessments) != 1 || len(result.CriterionAssessments[0].EvidenceIDs) != 1 || result.CriterionAssessments[0].EvidenceIDs[0] != id {
		t.Fatalf("verifier result kept the alias instead of the owned evidence ID: %#v", result.CriterionAssessments)
	}
}
