package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func (s *Store) RunRuntime(ctx context.Context, taskID string) (map[string]any, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"task_id": taskID, "status": task.Status, "stage": task.Stage, "terminal_outcome": task.TerminalOutcome, "pause_reason": task.PauseReason, "resource_reason": task.ResourceReason, "resume_after": task.ResumeAfter, "resource_probe_attempts": task.ResourceProbeAttempts, "updated_at": task.UpdatedAt}
	var cursor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE task_id=?`, taskID).Scan(&cursor); err != nil {
		return nil, err
	}
	result["cursor"] = cursor
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT state_json FROM checkpoints WHERE task_id=? ORDER BY last_event_seq DESC,rowid DESC LIMIT 1`, taskID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, ErrCheckpointInvalid
	}
	if raw != "" {
		var state struct {
			CurrentStep string                  `json:"current_step"`
			Completed   []string                `json:"completed_steps"`
			Completion  domain.CompletionState  `json:"completion"`
			Snapshot    *domain.ContextSnapshot `json:"context_snapshot"`
		}
		if json.Unmarshal([]byte(raw), &state) != nil {
			return nil, ErrCheckpointInvalid
		}
		result["step"], result["completed_steps"], result["completion"] = state.CurrentStep, len(state.Completed), state.Completion
		if state.Snapshot != nil {
			if err := validateSnapshot(*state.Snapshot); err != nil {
				return nil, err
			}
			result["context_snapshot_hash"], result["context_snapshot_number"] = state.Snapshot.Hash, state.Snapshot.Number
			result["contradictions"], result["open_questions"] = state.Snapshot.Contradictions, state.Snapshot.OpenQuestions
			result["accepted_evidence"], result["done_criteria"] = len(state.Snapshot.AcceptedEvidenceIDs), len(state.Snapshot.DoneCriteria)
		}
	}
	if session, err := researchSession(ctx, tx, taskID); err == nil {
		remaining, err := snapshotBudget(ctx, tx, session)
		if err != nil {
			return nil, err
		}
		result["budget"], result["counters"], result["budget_remaining"] = session.Budget, session.Counters, remaining
		result["deadline_at"], result["profile"], result["keep_awake_requested"] = session.DeadlineAt, session.Profile, session.Policy.KeepAwake
		result["remaining_seconds"] = max(int64(0), int64(time.Until(session.DeadlineAt).Seconds()))
		result["elapsed_seconds"] = max(int64(0), int64(time.Since(session.CreatedAt).Seconds()))
		if task.Status.Terminal() {
			result["remaining_seconds"] = int64(0)
			result["elapsed_seconds"] = max(int64(0), int64(task.UpdatedAt.Sub(session.CreatedAt).Seconds()))
		}
		result["max_continuations"] = session.Policy.MaxContinuations
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var compactions int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=? AND type='context.compacted'`, taskID).Scan(&compactions); err != nil {
		return nil, err
	}
	result["compactions"] = compactions
	var verification string
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.verdict') FROM events WHERE task_id=? AND type='verification.completed' ORDER BY seq DESC LIMIT 1`, taskID).Scan(&verification); err == nil {
		result["verdict"] = verification
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var notificationType, notificationRaw string
	if err := tx.QueryRowContext(ctx, `SELECT type,payload_json FROM events WHERE task_id=? AND type IN ('notification.attempted','notification.failed','notification.delivered','notification.abandoned') ORDER BY seq DESC LIMIT 1`, taskID).Scan(&notificationType, &notificationRaw); err == nil {
		var payload struct {
			Attempt int    `json:"attempt"`
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal([]byte(notificationRaw), &payload)
		result["notification"] = map[string]any{"status": notificationType[len("notification."):], "attempt": payload.Attempt, "channel": payload.Channel}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	} else if task.Status.Terminal() {
		var queued bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE task_id=? AND type IN ('task.completed','task.failed','task.cancelled','run.blocked','run.failed') AND json_valid(payload_json) AND json_extract(payload_json,'$.notification_schema_version')=1)`, taskID).Scan(&queued); err != nil {
			return nil, err
		}
		if queued {
			result["notification"] = map[string]any{"status": "pending"}
		}
	}
	return result, nil
}
