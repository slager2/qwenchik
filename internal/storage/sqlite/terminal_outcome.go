package sqlite

import (
	"context"
	"database/sql"

	"github.com/slager2/qwenchik/internal/domain"
)

func setTerminalOutcome(ctx context.Context, tx *sql.Tx, id string, status domain.TaskStatus, payload map[string]any) error {
	outcome := string(status)
	code, _ := payload["error_code"].(string)
	switch code {
	case "task_timeout", "run_deadline", "deadline_exceeded", "research_plan_budget", "research_budget_exceeded", "llm_budget_exceeded", "search_budget_exceeded", "fetch_budget_exceeded", "task_fetch_budget_exceeded", "context_budget_exceeded", "budget_exhausted":
		outcome = "budget_exhausted"
	case "resource_guard_exhausted", "failed_recovery_required", "fetch_outcome_unknown", "completion_blocked", "verification_incomplete", "invalid_model_output":
		outcome = "blocked"
	}
	if status == domain.StatusCompleted {
		var verification string
		err := tx.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.status') FROM events WHERE task_id=? AND type='verification.updated' AND visibility='user' AND json_valid(payload_json) AND json_extract(payload_json,'$.status') IN ('supported','fallback_excerpts') ORDER BY seq DESC LIMIT 1`, id).Scan(&verification)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if verification == "fallback_excerpts" {
			outcome = "partial"
		}
	}
	if payload != nil {
		payload["terminal_outcome"] = outcome
	}
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET terminal_outcome=? WHERE id=?`, outcome, id)
	return err
}
