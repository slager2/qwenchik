package domain

// Adaptive state belongs to the existing versioned task checkpoint. Session
// counters and immutable budgets remain the authority for admission.
type ResearchQuestion struct {
	ID           string `json:"id"`
	Text         string `json:"text"`
	Status       string `json:"status"`
	AcceptedOnce bool   `json:"accepted_once"`
}

type ResearchRound struct {
	AssessmentHash      string                        `json:"assessment_hash"`
	Number              int                           `json:"number"`
	Goal                string                        `json:"goal"`
	AcceptedEvidence    []string                      `json:"accepted_evidence_ids"`
	CompletedSteps      []string                      `json:"completed_steps"`
	CriticalMissing     []string                      `json:"unresolved_critical_criteria"`
	OpenQuestions       []string                      `json:"open_questions"`
	FailedSources       []string                      `json:"failed_sources"`
	SourceFailures      []map[string]any              `json:"source_failures"`
	ProgressFingerprint string                        `json:"progress_fingerprint"`
	RemainingBudgets    map[string]int64              `json:"remaining_budgets"`
	Frontier            []string                      `json:"frontier"`
	Outcome             string                        `json:"outcome"`
	Coverage            []VerificationCriterionResult `json:"coverage"`
}

type AdaptiveResearchState struct {
	Version        int                `json:"version"`
	CurrentRound   int                `json:"current_round"`
	Rounds         []ResearchRound    `json:"rounds"`
	Questions      []ResearchQuestion `json:"questions"`
	Frontier       []string           `json:"frontier"`
	StagnantRounds int                `json:"stagnant_rounds"`
	StopReason     string             `json:"stop_reason"`
}
