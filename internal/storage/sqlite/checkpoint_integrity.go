package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var ErrCheckpointInvalid = errors.New("checkpoint integrity invalid")

func verifiedCheckpoints(ctx context.Context, tx *sql.Tx, taskID string) ([]domain.Checkpoint, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.task_id,c.plan_version,c.state_json,c.last_event_seq,c.created_at,c.parent_id,c.state_schema_version,c.state_hash,c.context_snapshot_hash,EXISTS(SELECT 1 FROM events e WHERE e.task_id=c.task_id AND e.seq=c.last_event_seq) FROM checkpoints c WHERE c.task_id=? ORDER BY c.last_event_seq,c.rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.Checkpoint
	for rows.Next() {
		var item domain.Checkpoint
		var raw, created string
		var eventExists bool
		if err := rows.Scan(&item.ID, &item.TaskID, &item.PlanVersion, &raw, &item.LastEventSeq, &created, &item.ParentID, &item.StateSchemaVersion, &item.StateHash, &item.ContextSnapshotHash, &eventExists); err != nil {
			return nil, err
		}
		invalid := func() ([]domain.Checkpoint, error) { return nil, fmt.Errorf("%w: %s", ErrCheckpointInvalid, item.ID) }
		if !eventExists {
			return invalid()
		}
		if item.StateSchemaVersion == 0 {
			if item.StateHash != "" || item.ParentID != "" {
				return invalid()
			}
		} else if item.StateSchemaVersion == 1 {
			digest := sha256.Sum256([]byte(raw))
			if item.StateHash != hex.EncodeToString(digest[:]) || !eventExists {
				return invalid()
			}
			parent := ""
			if len(items) > 0 {
				parent = items[len(items)-1].ID
				if item.LastEventSeq <= items[len(items)-1].LastEventSeq {
					return invalid()
				}
			}
			if item.ParentID != parent {
				return invalid()
			}
		} else {
			return invalid()
		}
		if err := json.Unmarshal([]byte(raw), &item.State); err != nil {
			return invalid()
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CommitStepResult(ctx context.Context, taskID, stepID, tool, summary string, result any, state any) (domain.Checkpoint, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	defer tx.Rollback()
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return domain.Checkpoint{}, err
	}
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	if task.Status != domain.StatusRunning {
		return domain.Checkpoint{}, ErrConflict
	}
	state, err = completePendingStepInvocationTx(ctx, tx, taskID, stepID, tool, result, state)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE task_steps SET status='completed',updated_at=? WHERE task_id=? AND step_id=? AND status='running'`, stamp(time.Now()), taskID, stepID)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return domain.Checkpoint{}, err
	}
	if n != 1 {
		return domain.Checkpoint{}, ErrConflict
	}
	if _, err := appendEvent(ctx, tx, taskID, "tool.completed", task.Stage, "user", map[string]any{"step_id": stepID, "tool": tool, "summary": summary, "result": result}); err != nil {
		return domain.Checkpoint{}, err
	}
	seq, err := appendEvent(ctx, tx, taskID, "step.completed", task.Stage, "user", map[string]any{"step_id": stepID})
	if err != nil {
		return domain.Checkpoint{}, err
	}
	cp, err := saveCheckpointTx(ctx, tx, taskID, 1, seq, state)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return domain.Checkpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Checkpoint{}, err
	}
	return cp, nil
}

func sealInitialCheckpoint(ctx context.Context, tx *sql.Tx, taskID string) error {
	var id, raw string
	if err := tx.QueryRowContext(ctx, `SELECT id,state_json FROM checkpoints WHERE task_id=?`, taskID).Scan(&id, &raw); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(raw))
	_, err := tx.ExecContext(ctx, `UPDATE checkpoints SET state_schema_version=1,state_hash=? WHERE id=?`, hex.EncodeToString(digest[:]), id)
	return err
}
