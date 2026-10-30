package sqlite

import (
	"bytes"
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

type checkpointQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func verifiedCheckpoints(ctx context.Context, q checkpointQuery, taskID string) ([]domain.Checkpoint, error) {
	return verifiedCheckpointChain(ctx, q, taskID, false)
}

func decodeCheckpointState(raw string) (any, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		var value any
		err = json.Unmarshal([]byte(raw), &value)
		return value, err
	}
	if object == nil {
		return nil, nil
	}
	decoded := map[string]any{}
	for key, value := range object {
		if key == "context_snapshot" || key == "role_inputs" {
			decoded[key] = value
			continue
		}
		var child any
		if err := json.Unmarshal(value, &child); err != nil {
			return nil, err
		}
		decoded[key] = child
	}
	return decoded, nil
}

// Internal consumers need only the latest full state. History endpoints explicitly
// request full materialization; no checkpoint/history is removed from SQLite.
func verifiedCheckpointChain(ctx context.Context, q checkpointQuery, taskID string, full bool) ([]domain.Checkpoint, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id,c.task_id,c.plan_version,c.state_json,c.last_event_seq,c.created_at,c.parent_id,c.state_schema_version,c.state_hash,c.context_snapshot_hash,e.seq IS NOT NULL,COALESCE(e.type,''),COALESCE(e.visibility,''),COALESCE(e.payload_json,'') FROM checkpoints c LEFT JOIN events e ON e.task_id=c.task_id AND e.seq=c.last_event_seq WHERE c.task_id=? ORDER BY c.last_event_seq,c.rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.Checkpoint
	chainKey := ""
	var previous checkpointValidationRecord
	var lastRaw string
	for rows.Next() {
		var item domain.Checkpoint
		var stored, created string
		var eventExists bool
		var eventType, eventVisibility, eventPayload string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.PlanVersion, &stored, &item.LastEventSeq, &created, &item.ParentID, &item.StateSchemaVersion, &item.StateHash, &item.ContextSnapshotHash, &eventExists, &eventType, &eventVisibility, &eventPayload); err != nil {
			return nil, err
		}
		invalid := func() ([]domain.Checkpoint, error) { return nil, fmt.Errorf("%w: %s", ErrCheckpointInvalid, item.ID) }
		rawBytes, decodeErr := decodeCheckpointStorage(stored)
		if decodeErr != nil {
			return invalid()
		}
		raw := string(rawBytes)
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
		keyData, _ := json.Marshal([]any{chainKey, item.ID, item.TaskID, item.PlanVersion, item.LastEventSeq, created, item.ParentID, item.StateSchemaVersion, item.StateHash, item.ContextSnapshotHash, roleInputHash([]byte(raw)), eventType, eventVisibility, roleInputHash([]byte(eventPayload))})
		chainKey = roleInputHash(keyData)
		record, known := checkpointValidationKnown(chainKey)
		if !known || full {
			item.State, err = decodeCheckpointState(raw)
			if err != nil {
				return invalid()
			}
		}
		if !known {
			record, err = validateCheckpointRecord(item, previous, eventType, eventVisibility, eventPayload)
			if err != nil {
				return invalid()
			}
			rememberCheckpointValidation(chainKey, record)
		}
		previous = record
		lastRaw = raw
		if !full {
			item.State = nil
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !full && len(items) > 0 {
		items[len(items)-1].State, err = decodeCheckpointState(lastRaw)
		if err != nil {
			return nil, ErrCheckpointInvalid
		}
	}
	return items, nil
}

func preserveContextSnapshotJSON(state any, encoded []byte) {
	values, ok := state.(map[string]any)
	if !ok {
		return
	}
	object := checkpointObject(encoded)
	if raw, exists := object["context_snapshot"]; exists {
		values["context_snapshot"] = raw
	}
	if raw, exists := object["role_inputs"]; exists {
		values["role_inputs"] = raw
	}
}

func verifyContextSnapshotCheckpoint(item domain.Checkpoint, prior []domain.Checkpoint) error {
	snapshot, exists, err := checkpointSnapshot(item.State)
	if err != nil {
		return ErrCheckpointInvalid
	}
	var previous domain.ContextSnapshot
	if len(prior) > 0 {
		previous, _, err = checkpointSnapshot(prior[len(prior)-1].State)
		if err != nil {
			return ErrCheckpointInvalid
		}
	}
	if !exists {
		if item.ContextSnapshotHash != "" || previous.Number != 0 {
			return ErrCheckpointInvalid
		}
		return nil
	}
	hash, err := snapshotHash(snapshot)
	if err != nil || snapshot.Hash != hash || item.ContextSnapshotHash != hash || snapshot.ID != snapshotID(item.TaskID, hash) || snapshot.Number < 1 {
		return ErrCheckpointInvalid
	}
	if previous.Number == 0 {
		if snapshot.Number != 1 {
			return ErrCheckpointInvalid
		}
	} else if snapshot.Number == previous.Number {
		if snapshot.Hash != previous.Hash {
			return ErrCheckpointInvalid
		}
	} else if snapshot.Number != previous.Number+1 || snapshot.Hash == previous.Hash {
		return ErrCheckpointInvalid
	}
	return nil
}

func prepareContextSnapshotCheckpoint(encoded []byte, prior []domain.Checkpoint) ([]byte, error) {
	if len(prior) == 0 {
		return encoded, nil
	}
	last, err := json.Marshal(prior[len(prior)-1].State)
	if err != nil {
		return nil, err
	}
	previous := checkpointObject(last)
	raw, hasSnapshot := previous["context_snapshot"]
	_, hasPins := previous["role_inputs"]
	job, hasJob := previous["refinement_job"]
	if !hasSnapshot && !hasPins && !hasJob {
		return encoded, nil
	}
	values := checkpointObject(encoded)
	if values == nil {
		return nil, ErrCheckpointInvalid
	}
	if hasJob {
		if next, exists := values["refinement_job"]; !exists {
			values["refinement_job"] = job
		} else {
			var before, after domain.RefinementJob
			if json.Unmarshal(job, &before) != nil || json.Unmarshal(next, &after) != nil {
				return nil, ErrCheckpointInvalid
			}
			if before.CandidateHash != "" && before.CandidateHash != after.CandidateHash {
				return nil, ErrCheckpointInvalid
			}
			before.CandidateHash, after.CandidateHash = "", ""
			a, _ := json.Marshal(before)
			b, _ := json.Marshal(after)
			if !bytes.Equal(a, b) {
				return nil, ErrCheckpointInvalid
			}
		}
	}
	if _, exists := values["context_snapshot"]; !exists && hasSnapshot {
		values["context_snapshot"] = raw
	}
	if hasPins && !bytes.Equal(values["role_inputs"], previous["role_inputs"]) {
		if _, present := values["role_inputs"]; !present {
			values["role_inputs"] = previous["role_inputs"]
			return json.Marshal(values)
		}
		pins, err := decodeRoleInputs(values["role_inputs"])
		if err != nil {
			return nil, err
		}
		old, err := decodeRoleInputs(previous["role_inputs"])
		if err != nil {
			return nil, err
		}
		if err := mergeRoleInputs(pins, old); err != nil {
			return nil, err
		}
		values["role_inputs"], err = json.Marshal(pins)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(values)
}

func (s *Store) CommitStepResult(ctx context.Context, taskID, stepID, tool, summary string, result any, state any) (domain.Checkpoint, error) {
	tx, checkpointCtx, err := s.beginCheckpointTx(ctx)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	ctx = checkpointCtx
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
	state, err = s.completePendingStepInvocationTx(ctx, tx, taskID, stepID, tool, result, state)
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
	cp, err := s.saveCheckpointTx(ctx, tx, taskID, 1, seq, state)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return domain.Checkpoint{}, err
	}
	if err := s.commitCheckpointTx(ctx, tx); err != nil {
		return domain.Checkpoint{}, err
	}
	return cp, nil
}

func sealInitialCheckpoint(ctx context.Context, tx *sql.Tx, taskID string) error {
	var id, stored string
	if err := tx.QueryRowContext(ctx, `SELECT id,state_json FROM checkpoints WHERE task_id=?`, taskID).Scan(&id, &stored); err != nil {
		return err
	}
	raw, err := decodeCheckpointStorage(stored)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	_, err = tx.ExecContext(ctx, `UPDATE checkpoints SET state_schema_version=1,state_hash=? WHERE id=?`, hex.EncodeToString(digest[:]), id)
	return err
}
