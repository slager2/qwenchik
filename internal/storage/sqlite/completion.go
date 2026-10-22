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
	if expected < 0 || expected >= 2 || !roleContextSlot.MatchString(fingerprint) || !roleContextSlot.MatchString(failure) || len(missing) > 20 {
		return ErrCheckpointInvalid
	}
	for _, value := range missing {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 500 {
			return ErrCheckpointInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return err
	}
	session, err := researchSession(ctx, tx, taskID)
	if err != nil {
		return err
	}
	state, version, err := invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=? AND type='continuation.requested'`, taskID).Scan(&count); err != nil {
		return err
	}
	if count != expected || count >= min(1, session.Policy.MaxContinuations) {
		return ErrConflict
	}
	var prior domain.CompletionState
	raw, _ := json.Marshal(state["completion"])
	if err := json.Unmarshal(raw, &prior); err != nil {
		return ErrCheckpointInvalid
	}
	if prior.Fingerprint == fingerprint || prior.FailureHash == failure {
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
	if len(plan.Subtasks)+2 > min(12, session.Budget.MaxSteps) {
		return ErrResearchBudgetExceeded
	}
	search := fmt.Sprintf("continue_%d_search", count+1)
	steps := []domain.PlanStep{
		{ID: search, Kind: "search", Description: "Address the unmet criteria in context_snapshot.open_questions with a new independent targeted query", SuccessCriteria: []string{"A new targeted discovery query is recorded"}},
		{ID: fmt.Sprintf("continue_%d_fetch", count+1), Kind: "fetch", Description: "Inspect a new source addressing the unmet criteria in context_snapshot.open_questions", Dependencies: []string{search}, ExpectedOutput: "Task-owned extracted evidence", SuccessCriteria: []string{"New source evidence is stored"}},
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
	if _, err := saveCheckpointTx(ctx, tx, taskID, version, seq, state); err != nil {
		return err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CommitVerification(ctx context.Context, taskID, verdict, summary string, missing []string) error {
	if verdict != "PASS" && verdict != "REPAIR" && verdict != "RESEARCH" && verdict != "UNCERTAIN" && verdict != "BLOCK" {
		return ErrCheckpointInvalid
	}
	if len(summary) > 4000 || len(missing) > 20 {
		return ErrCheckpointInvalid
	}
	for _, value := range missing {
		if utf8.RuneCountInString(value) > 500 {
			return ErrCheckpointInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return err
	}
	state, version, err := invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return err
	}
	var completion domain.CompletionState
	raw, _ := json.Marshal(state["completion"])
	if err := json.Unmarshal(raw, &completion); err != nil {
		return ErrCheckpointInvalid
	}
	completion.Verdict = verdict
	state["completion"] = completion
	seq, err := appendEvent(ctx, tx, taskID, "verification.completed", stagePointer(domain.StageVerification), "user", map[string]any{"verdict": verdict, "phase": "evidence_readiness", "summary": summary, "missing": missing})
	if err != nil {
		return err
	}
	if _, err := saveCheckpointTx(ctx, tx, taskID, version, seq, state); err != nil {
		return err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	return tx.Commit()
}
