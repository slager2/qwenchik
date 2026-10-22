package orchestrator

import (
	"fmt"

	"github.com/slager2/qwenchik/internal/domain"
)

type verificationCriterion struct {
	ID          string   `json:"id"`
	StepID      string   `json:"step_id"`
	Index       int      `json:"criterion_index"`
	Text        string   `json:"text"`
	Kind        string   `json:"kind"`
	ResultKey   string   `json:"result_key"`
	EvidenceIDs []string `json:"available_evidence_ids"`
}

type criterionVerdict struct {
	Met         bool     `json:"met"`
	EvidenceIDs []string `json:"evidence_ids"`
	Rationale   string   `json:"rationale"`
}

type verificationResponse struct {
	Contradictions []string                    `json:"contradictions,omitempty"`
	Complete       bool                        `json:"ready_to_answer"`
	Summary        string                      `json:"summary"`
	Missing        []string                    `json:"missing"`
	Assessments    map[string]criterionVerdict `json:"criterion_assessments"`
}

func verificationCatalog(plan domain.Plan, state checkpointState) []verificationCriterion {
	catalog := make([]verificationCriterion, 0)
	for _, step := range plan.Subtasks {
		if contains(state.SkippedSteps, step.ID) {
			continue
		}
		for index, text := range step.SuccessCriteria {
			catalog = append(catalog, verificationCriterion{ID: fmt.Sprintf("c%d", len(catalog)), StepID: step.ID, Index: index, Text: text, Kind: step.Kind, ResultKey: step.ID, EvidenceIDs: collectEvidenceIDs(state.ToolResults[step.ID])})
		}
	}
	return catalog
}

func addressedVerification(response verificationResponse, catalog []verificationCriterion) (verification, error) {
	result := verification{Complete: response.Complete, Summary: response.Summary, Missing: response.Missing}
	if len(response.Assessments) != len(catalog) {
		return verification{}, fmt.Errorf("%w: verifier assessment coverage does not match the criterion catalog", errInvalidModelOutput)
	}
	for _, criterion := range catalog {
		verdict, exists := response.Assessments[criterion.ID]
		if !exists {
			return verification{}, fmt.Errorf("%w: verifier omitted criterion %s", errInvalidModelOutput, criterion.ID)
		}
		result.CriterionAssessments = append(result.CriterionAssessments, criterionAssessment{StepID: criterion.StepID, Criterion: criterion.Index, Met: verdict.Met, EvidenceIDs: verdict.EvidenceIDs, Rationale: verdict.Rationale})
	}
	return result, nil
}
