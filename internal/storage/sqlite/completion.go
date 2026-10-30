package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

func (s *Store) RequestResearchContinuation(ctx context.Context, taskID string, expected int, fingerprint, failure string, missing []string) error {
	if expected < 0 || expected >= 10 || !roleContextSlot.MatchString(fingerprint) || !roleContextSlot.MatchString(failure) || len(missing) > 20 {
		return ErrCheckpointInvalid
	}
	for _, value := range missing {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 500 {
			return ErrCheckpointInvalid
		}
	}
	tx, checkpointCtx, err := s.beginCheckpointTx(ctx)
	if err != nil {
		return err
	}
	ctx = checkpointCtx
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return err
	}
	session, err := researchSession(ctx, tx, taskID)
	if err != nil {
		return err
	}
	state, version, err := s.invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=? AND type='continuation.requested'`, taskID).Scan(&count); err != nil {
		return err
	}
	continuationLimit := min(1, session.Policy.MaxContinuations)
	stepCount := 2
	if session.Policy.AdaptiveEnabled {
		continuationLimit = min(9, min(session.Policy.MaxContinuations, session.Budget.MaxRounds-1))
		stepCount = 4
	}
	if count != expected || count >= continuationLimit {
		return ErrConflict
	}
	if session.Policy.Version == 2 {
		return requestBoundedRoundTx(ctx, s, tx, session, state, version, count, fingerprint, failure, missing)
	}
	var prior domain.CompletionState
	raw, _ := json.Marshal(state["completion"])
	if err := json.Unmarshal(raw, &prior); err != nil {
		return ErrCheckpointInvalid
	}
	if !session.Policy.AdaptiveEnabled && (prior.Fingerprint == fingerprint || prior.FailureHash == failure) {
		return ErrConflict
	}
	if session.Counters.SearchQueries >= session.Budget.MaxSearchQueries || session.Counters.Fetches >= session.Budget.MaxFetches || session.Counters.LLMCalls+3 > session.Budget.MaxLLMCalls || session.Counters.Evidence >= session.Budget.MaxEvidence || session.Counters.FetchedBytes >= session.Policy.TaskFetchBudgetBytes || session.CurrentRound+1 >= session.Budget.MaxRounds {
		return ErrResearchBudgetExceeded
	}
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if task.Plan == nil {
		return ErrConflict
	}
	if pending, _ := state["pending_tool"].(string); pending != "" {
		return ErrConflict
	}
	if pending, _ := state["pending_approval"].(bool); pending {
		return ErrConflict
	}
	plan := *task.Plan
	if len(plan.Subtasks)+stepCount > min(12, session.Budget.MaxSteps) {
		return ErrResearchBudgetExceeded
	}
	search := fmt.Sprintf("continue_%d_search", count+1)
	steps := []domain.PlanStep{
		{ID: search, Kind: "search", Description: "Address the unmet criteria in context_snapshot.open_questions with a new independent targeted query", SuccessCriteria: []string{"A new targeted discovery query is recorded"}},
		{ID: fmt.Sprintf("continue_%d_fetch", count+1), Kind: "fetch", Description: "Inspect a new source addressing the unmet criteria in context_snapshot.open_questions", Dependencies: []string{search}, ExpectedOutput: "Task-owned extracted evidence", SuccessCriteria: []string{"New source evidence is stored"}},
	}
	if session.Policy.AdaptiveEnabled {
		adaptive, err := adaptiveState(state)
		if err != nil {
			return err
		}
		if adaptive.StopReason != "" || len(adaptive.Frontier) == 0 {
			return ErrResearchBudgetExceeded
		}
		if session.Counters.SearchQueries+2 > session.Budget.MaxSearchQueries || session.Counters.Fetches+2 > session.Budget.MaxFetches || session.Counters.LLMCalls+7 > session.Budget.MaxLLMCalls || session.Counters.Evidence+2 > session.Budget.MaxEvidence {
			return ErrResearchBudgetExceeded
		}
		second := fmt.Sprintf("continue_%d_search_2", count+1)
		steps = []domain.PlanStep{
			{ID: search, Kind: "search", Description: "Address the critical open questions with a new independent targeted query focused on missing direct evidence", SuccessCriteria: []string{"A new targeted discovery query is recorded"}},
			{ID: second, Kind: "search", Description: "Address the critical open questions with a different independent targeted query seeking corroboration or contradictions", SuccessCriteria: []string{"A distinct corroborating discovery query is recorded"}},
			{ID: fmt.Sprintf("continue_%d_fetch", count+1), Kind: "fetch", Description: "Inspect a new source addressing the critical open questions", Dependencies: []string{search, second}, ExpectedOutput: "Task-owned extracted evidence", SuccessCriteria: []string{"New source evidence is stored"}},
			{ID: fmt.Sprintf("continue_%d_fetch_2", count+1), Kind: "fetch", Description: "Inspect a different new source addressing the critical open questions", Dependencies: []string{search, second}, ExpectedOutput: "Task-owned extracted evidence", SuccessCriteria: []string{"New independent source evidence is stored"}},
		}
	}
	position := len(plan.Subtasks)
	for _, step := range plan.Subtasks {
		if strings.HasPrefix(step.ID, "continue_") && count == 0 {
			return ErrConflict
		}
	}
	plan.Subtasks = append(append([]domain.PlanStep{}, plan.Subtasks...), steps...)
	plan.MaxSteps = max(plan.MaxSteps, len(plan.Subtasks))
	if err := domain.ValidatePlan(plan, session.Budget.MaxSteps); err != nil {
		return err
	}
	now := time.Now().UTC()
	for index, step := range steps {
		dependencies, _ := json.Marshal(step.Dependencies)
		criteria, _ := json.Marshal(step.SuccessCriteria)
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_steps(id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'pending',?,?)`, newID(), taskID, step.ID, position+index, step.Kind, step.Description, string(dependencies), step.ExpectedOutput, string(criteria), stamp(now), stamp(now)); err != nil {
			return err
		}
	}
	encoded, _ := json.Marshal(plan)
	stage := domain.StageSearch
	if err := transitionStageTx(ctx, tx, taskID, task.Stage, &stage, "running", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='running',stage=?,plan_json=?,version=version+1,updated_at=? WHERE id=?`, stage, string(encoded), stamp(now), taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE research_sessions SET current_round=current_round+1,current_phase=?,status='running',updated_at=? WHERE task_id=?`, stage, stamp(now), taskID); err != nil {
		return err
	}
	state["completion"] = domain.CompletionState{Continuations: count + 1, Fingerprint: fingerprint, FailureHash: failure, Verdict: "RESEARCH", Missing: missing}
	state["current_round"] = session.CurrentRound + 1
	seq, err := appendEvent(ctx, tx, taskID, "continuation.requested", &stage, "user", map[string]any{"attempt": count + 1, "max_continuations": session.Policy.MaxContinuations, "progress_fingerprint": fingerprint, "failure_hash": failure, "missing": missing, "new_steps": []string{steps[0].ID, steps[1].ID}})
	if err != nil {
		return err
	}
	if _, err := s.saveCheckpointTx(context.WithValue(ctx, adaptiveMutationKey{}, session.Policy.AdaptiveEnabled), tx, taskID, version, seq, state); err != nil {
		return err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	return s.commitCheckpointTx(ctx, tx)
}

func (s *Store) CommitVerification(ctx context.Context, taskID, verdict, summary string, missing, supplementaryMissing []string, criteria []domain.VerificationCriterionResult) error {
	if verdict != "PASS" && verdict != "PARTIAL" && verdict != "REPAIR" && verdict != "RESEARCH" && verdict != "UNCERTAIN" && verdict != "BLOCK" {
		return ErrCheckpointInvalid
	}
	if len(summary) > 4000 || len(missing) > 20 || len(supplementaryMissing) > 20 || len(criteria) > 120 {
		return ErrCheckpointInvalid
	}
	for _, values := range [][]string{missing, supplementaryMissing} {
		for _, value := range values {
			if utf8.RuneCountInString(value) > 500 {
				return ErrCheckpointInvalid
			}
		}
	}
	evidenceIDs := []string{}
	for _, criterion := range criteria {
		if len(criterion.StepID) == 0 || len(criterion.StepID) > 64 || criterion.Criterion < 0 || criterion.Criterion >= 10 || criterion.Classification != "critical" && criterion.Classification != "supplementary" || len(criterion.EvidenceIDs) > 16 {
			return ErrCheckpointInvalid
		}
		for _, evidenceID := range criterion.EvidenceIDs {
			if evidenceID == "" || len(evidenceID) > 128 {
				return ErrCheckpointInvalid
			}
			evidenceIDs = append(evidenceIDs, evidenceID)
		}
	}
	tx, checkpointCtx, err := s.beginCheckpointTx(ctx)
	if err != nil {
		return err
	}
	ctx = checkpointCtx
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return err
	}
	ownedEvidence := make([]string, 0, len(evidenceIDs))
	seenEvidence := map[string]bool{}
	for _, evidenceID := range evidenceIDs {
		if !seenEvidence[evidenceID] {
			seenEvidence[evidenceID] = true
			ownedEvidence = append(ownedEvidence, evidenceID)
		}
	}
	if err := validateSnapshotEvidence(ctx, tx, taskID, ownedEvidence); err != nil {
		return err
	}
	state, version, err := s.invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if session, sessionErr := researchSession(ctx, tx, taskID); sessionErr == nil && session.Policy.AdaptiveEnabled {
		adaptive, stateErr := adaptiveState(state)
		if stateErr != nil {
			return stateErr
		}
		if len(adaptive.Rounds) > 0 && adaptive.Rounds[len(adaptive.Rounds)-1].Number == session.CurrentRound+1 {
			if adaptive.Rounds[len(adaptive.Rounds)-1].AssessmentHash != adaptiveAssessmentHash(verdict, summary, missing, supplementaryMissing, criteria) {
				return ErrConflict
			}
			return s.commitCheckpointTx(ctx, tx)
		}
	}
	var completion domain.CompletionState
	raw, _ := json.Marshal(state["completion"])
	if err := json.Unmarshal(raw, &completion); err != nil {
		return ErrCheckpointInvalid
	}
	completion.Verdict = verdict
	state["completion"] = completion
	if err := assessAdaptiveRound(ctx, tx, taskID, state, verdict, summary, missing, supplementaryMissing, criteria); err != nil {
		return err
	}
	payload := map[string]any{
		"verdict": verdict, "phase": "evidence_readiness", "summary": summary,
		"missing": missing, "supplementary_missing": supplementaryMissing, "criteria": criteria,
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil || len(encodedPayload) > 512<<10 {
		return ErrCheckpointInvalid
	}
	seq, err := appendEvent(ctx, tx, taskID, "verification.completed", stagePointer(domain.StageVerification), "user", payload)
	if err != nil {
		return err
	}
	if _, err := s.saveCheckpointTx(context.WithValue(ctx, adaptiveMutationKey{}, true), tx, taskID, version, seq, state); err != nil {
		return err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	return s.commitCheckpointTx(ctx, tx)
}
