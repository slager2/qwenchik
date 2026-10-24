package orchestrator

import (
	"fmt"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
)

type verificationOutcome string

const (
	verificationOutcomeComplete verificationOutcome = "complete"
	verificationOutcomePartial  verificationOutcome = "partial"
	verificationOutcomeBlocked  verificationOutcome = "blocked"
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
	Supplementary  []string                    `json:"supplementary_criteria"`
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
	supplementary := make(map[string]bool, len(response.Supplementary))
	known := make(map[string]bool, len(catalog))
	for _, criterion := range catalog {
		known[criterion.ID] = true
	}
	for _, id := range response.Supplementary {
		if !known[id] || supplementary[id] {
			return verification{}, fmt.Errorf("%w: verifier returned an unknown or duplicate supplementary criterion", errInvalidModelOutput)
		}
		supplementary[id] = true
	}
	for _, criterion := range catalog {
		verdict, exists := response.Assessments[criterion.ID]
		if !exists {
			return verification{}, fmt.Errorf("%w: verifier omitted criterion %s", errInvalidModelOutput, criterion.ID)
		}
		classification := "critical"
		if supplementary[criterion.ID] {
			classification = "supplementary"
		}
		result.CriterionAssessments = append(result.CriterionAssessments, criterionAssessment{StepID: criterion.StepID, Criterion: criterion.Index, Classification: classification, Met: verdict.Met, EvidenceIDs: verdict.EvidenceIDs, Rationale: verdict.Rationale})
	}
	if response.Complete {
		result.Status = verificationOutcomeComplete
	}
	return result, nil
}

func filterVerificationEvidence(assessments []criterionAssessment, evidence []domain.Evidence) ([]criterionAssessment, []string) {
	usable := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		if item.HTTPStatus >= 200 && item.HTTPStatus < 300 && len(item.QualityFlags) == 0 && strings.TrimSpace(item.Excerpt) != "" {
			usable[item.ID] = true
		}
	}
	filtered := make([]criterionAssessment, len(assessments))
	copy(filtered, assessments)
	var missing []string
	for index := range filtered {
		ids := make([]string, 0, len(filtered[index].EvidenceIDs))
		invalid := false
		for _, id := range filtered[index].EvidenceIDs {
			if !usable[id] {
				invalid = true
				continue
			}
			ids = append(ids, id)
		}
		if invalid {
			missing = appendVerificationMissing(missing, fmt.Sprintf("Acceptance criterion %q.%d cites evidence that is unavailable or unsuitable for verification.", filtered[index].StepID, filtered[index].Criterion))
		}
		filtered[index].EvidenceIDs = ids
	}
	return filtered, missing
}
