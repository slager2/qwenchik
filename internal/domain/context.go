package domain

import "errors"

var ErrContextSnapshotTooLarge = errors.New("context snapshot exceeds size limit")

type ContextSnapshot struct {
	BundleHash          string            `json:"bundle_hash,omitempty"`
	Version             int               `json:"version"`
	PlanAttempt         int               `json:"plan_attempt,omitempty"`
	ID                  string            `json:"id"`
	Number              int               `json:"number"`
	Hash                string            `json:"hash"`
	Goal                string            `json:"goal"`
	Constraints         []string          `json:"constraints"`
	DoneCriteria        []string          `json:"done_criteria"`
	CompletedSteps      []string          `json:"completed_steps"`
	AcceptedEvidenceIDs []string          `json:"accepted_evidence_ids"`
	OpenQuestions       []string          `json:"open_questions"`
	Contradictions      []string          `json:"contradictions"`
	FailedAttempts      []string          `json:"failed_attempts"`
	BudgetRemaining     map[string]int64  `json:"budget_remaining"`
	NextAction          string            `json:"next_action"`
	Summary             string            `json:"summary"`
	FileChanges         []string          `json:"file_changes"`
	WorkingSet          map[string]string `json:"working_set"`
}
