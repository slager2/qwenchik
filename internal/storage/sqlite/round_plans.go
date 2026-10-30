package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/slager2/qwenchik/internal/domain"
	"time"
)

func requestBoundedRoundTx(ctx context.Context, store *Store, tx *sql.Tx, s domain.ResearchSession, state map[string]any, version, count int, fingerprint, failure string, missing []string) error {
	var plans domain.ResearchPlans
	raw, _ := json.Marshal(state["research_plans"])
	if json.Unmarshal(raw, &plans) != nil {
		return ErrCheckpointInvalid
	}
	if _, err := plans.Current(); err != nil {
		return ErrCheckpointInvalid
	}
	if plans.CurrentRound != count+1 || s.CurrentRound != count {
		return ErrConflict
	}
	if pending, _ := state["pending_tool"].(string); pending != "" {
		return ErrConflict
	}
	if pending, _ := state["pending_approval"].(bool); pending {
		return ErrConflict
	}
	if pending, _ := state["pending_invocation_id"].(string); pending != "" {
		return ErrConflict
	}
	if s.Policy.AdaptiveEnabled {
		a, err := adaptiveState(state)
		if err != nil {
			return err
		}
		if a.StopReason != "" || len(a.Frontier) == 0 {
			return ErrResearchBudgetExceeded
		}
		if len(a.Rounds) != plans.CurrentRound {
			return ErrConflict
		}
	}
	_, searches, fetches, reserve := domain.NextResearchRoundShape(s)
	steps := []domain.PlanStep{}
	dependencies := []string{}
	for i := 0; i < searches; i++ {
		id := fmt.Sprintf("continue_%d_search", count+1)
		if i > 0 {
			id += fmt.Sprintf("_%d", i+1)
		}
		dependencies = append(dependencies, id)
		steps = append(steps, domain.PlanStep{ID: id, Kind: "search", Description: fmt.Sprintf("Address the critical open questions with a new independent targeted query focused on missing direct evidence, distinct discovery angle %d", i+1), SuccessCriteria: []string{"A new targeted discovery query is recorded"}})
	}
	for i := 0; i < fetches; i++ {
		id := fmt.Sprintf("continue_%d_fetch", count+1)
		if i > 0 {
			id += fmt.Sprintf("_%d", i+1)
		}
		steps = append(steps, domain.PlanStep{ID: id, Kind: "fetch", Description: fmt.Sprintf("Inspect a different new source addressing the critical open questions, source %d", i+1), Dependencies: append([]string{}, dependencies...), ExpectedOutput: "Task-owned extracted evidence", SuccessCriteria: []string{"New source evidence is stored"}})
	}
	if len(steps) > s.Budget.MaxRoundSteps || domain.ContinuationStopReason(s, len(steps), searches, fetches, reserve, time.Now()) != "" {
		return ErrResearchBudgetExceeded
	}
	p := domain.Plan{SchemaVersion: 1, Goal: plans.InitialPlan.Goal, FinalDeliverable: plans.InitialPlan.FinalDeliverable, DeliverableCriteria: plans.InitialPlan.DeliverableCriteria, Subtasks: steps, MaxSteps: len(steps)}
	if domain.ValidatePlan(p, s.Budget.MaxRoundSteps) != nil {
		return ErrCheckpointInvalid
	}
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM task_steps WHERE task_id=?`, s.TaskID).Scan(&position); err != nil {
		return err
	}
	var unfinished int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_steps WHERE task_id=? AND status IN ('pending','running')`, s.TaskID).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished > 0 {
		return ErrConflict
	}
	now := time.Now().UTC()
	task, err := getTask(ctx, tx, s.TaskID)
	if err != nil {
		return err
	}
	stage := domain.StageSearch
	if err := transitionStageTx(ctx, tx, s.TaskID, task.Stage, &stage, "running", now); err != nil {
		return err
	}
	for i, step := range steps {
		deps, _ := json.Marshal(step.Dependencies)
		criteria, _ := json.Marshal(step.SuccessCriteria)
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_steps(id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'pending',?,?)`, newID(), s.TaskID, step.ID, position+i, step.Kind, step.Description, string(deps), step.ExpectedOutput, string(criteria), stamp(now), stamp(now)); err != nil {
			return err
		}
	}
	plans.CurrentRound++
	plans.Rounds = append(plans.Rounds, domain.RoundPlan{Number: plans.CurrentRound, Plan: p})
	state["research_plans"] = plans
	state["completion"] = domain.CompletionState{Continuations: count + 1, Fingerprint: fingerprint, FailureHash: failure, Verdict: "RESEARCH", Missing: missing}
	state["current_round"] = count + 1
	s.Counters.Steps += len(steps)
	if err := writeResearchCounters(ctx, tx, s.TaskID, s.Counters); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE research_sessions SET current_round=current_round+1,current_phase='search',status='running',updated_at=? WHERE task_id=?`, stamp(now), s.TaskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='running',stage='search',version=version+1,updated_at=? WHERE id=?`, stamp(now), s.TaskID); err != nil {
		return err
	}
	ids := []string{}
	for _, step := range steps {
		ids = append(ids, step.ID)
	}
	seq, err := appendEvent(ctx, tx, s.TaskID, "continuation.requested", stagePointer(domain.StageSearch), "user", map[string]any{"attempt": count + 1, "round": plans.CurrentRound, "max_continuations": s.Policy.MaxContinuations, "progress_fingerprint": fingerprint, "failure_hash": failure, "missing": missing, "new_steps": ids, "steps_reserved": s.Counters.Steps})
	if err != nil {
		return err
	}
	if _, err := store.saveCheckpointTx(context.WithValue(ctx, adaptiveMutationKey{}, true), tx, s.TaskID, version, seq, state); err != nil {
		return err
	}
	if err := fenceExecution(ctx, tx, s.TaskID); err != nil {
		return err
	}
	return store.commitCheckpointTx(ctx, tx)
}
