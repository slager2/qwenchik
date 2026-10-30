package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/slager2/qwenchik/internal/domain"
)

// checkpointHistoryCache is process-local acceleration only. The SQLite
// generation is advanced by triggers for every event/checkpoint mutation, so
// cached history is never trusted once the durable chain has changed. The
// cached checkpoints are immutable after verification; only the last item
// carries materialized state, which is needed to preserve context snapshots
// and role-input pins while appending the next checkpoint.
type checkpointHistoryCache struct {
	generation  int64
	checkpoints []domain.Checkpoint
}

// checkpointTxState carries the checkpoints written by one transaction until
// its commit has succeeded.  Publishing a speculative tail from inside a
// transaction is unsafe: the caller may roll the transaction back and a
// later writer can reuse the same durable generation.  Keeping this state in
// the transaction's context lets the write path publish exactly once, after
// Commit returns nil, without making sql.Tx part of the Store's global state.
type checkpointTxState struct {
	tx          *sql.Tx
	checkpoints map[string]domain.Checkpoint
}

type checkpointTxStateKey struct{}

func (s *Store) beginCheckpointTx(ctx context.Context) (*sql.Tx, context.Context, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ctx, err
	}
	state := &checkpointTxState{tx: tx, checkpoints: make(map[string]domain.Checkpoint)}
	return tx, context.WithValue(ctx, checkpointTxStateKey{}, state), nil
}

func checkpointTxStateFor(ctx context.Context, tx *sql.Tx) *checkpointTxState {
	state, _ := ctx.Value(checkpointTxStateKey{}).(*checkpointTxState)
	if state == nil || state.tx != tx {
		return nil
	}
	return state
}

func (s *Store) rememberCheckpointAfterCommit(ctx context.Context, tx *sql.Tx, checkpoint domain.Checkpoint) {
	state := checkpointTxStateFor(ctx, tx)
	if state == nil {
		return
	}
	prior, exists := state.checkpoints[checkpoint.TaskID]
	if !exists || checkpoint.LastEventSeq >= prior.LastEventSeq {
		state.checkpoints[checkpoint.TaskID] = checkpoint
	}
}

// commitCheckpointTx is used by transactional writers that append a
// checkpoint. Cache publication is best-effort after a successful durable
// commit; a failed verification simply leaves the generation mismatch in
// place, causing the next reader to rebuild the chain fail-closed.
func (s *Store) commitCheckpointTx(ctx context.Context, tx *sql.Tx) error {
	state := checkpointTxStateFor(ctx, tx)
	if err := tx.Commit(); err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	for taskID, checkpoint := range state.checkpoints {
		publishCtx := context.WithoutCancel(ctx)
		generation, err := checkpointChainGeneration(publishCtx, s.db, taskID)
		if err != nil {
			continue
		}
		var latestID string
		if err := s.db.QueryRowContext(publishCtx, `SELECT id FROM checkpoints WHERE task_id=? ORDER BY last_event_seq DESC,rowid DESC LIMIT 1`, taskID).Scan(&latestID); err != nil || latestID != checkpoint.ID {
			continue
		}
		s.rememberCheckpointHistory(taskID, generation, []domain.Checkpoint{checkpoint})
	}
	return nil
}

type checkpointGenerationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func checkpointChainGeneration(ctx context.Context, q checkpointGenerationQuery, taskID string) (int64, error) {
	var generation int64
	err := q.QueryRowContext(ctx, `SELECT generation FROM checkpoint_chain_state WHERE task_id=?`, taskID).Scan(&generation)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return generation, err
}

func (s *Store) cachedVerifiedCheckpoints(ctx context.Context, taskID string) ([]domain.Checkpoint, int64, error) {
	return s.cachedVerifiedCheckpointsQuery(ctx, s.db, taskID)
}

func (s *Store) cachedVerifiedCheckpointsQuery(ctx context.Context, q checkpointGenerationQuery, taskID string) ([]domain.Checkpoint, int64, error) {
	for attempt := 0; attempt < 2; attempt++ {
		generation, err := checkpointChainGeneration(ctx, q, taskID)
		if err != nil {
			return nil, 0, err
		}
		s.checkpointMu.Lock()
		cached, ok := s.checkpointCache[taskID]
		s.checkpointMu.Unlock()
		if ok && cached.generation == generation {
			return cached.checkpoints, generation, nil
		}

		checkpoints, err := verifiedCheckpoints(ctx, q, taskID)
		if err != nil {
			return nil, 0, err
		}
		stableGeneration, err := checkpointChainGeneration(ctx, q, taskID)
		if err != nil {
			return nil, 0, err
		}
		if generation != stableGeneration {
			continue
		}
		// A write transaction may have uncommitted checkpoint rows visible to
		// this query. Never publish that speculative chain; its owner will
		// publish the committed tail through commitCheckpointTx.
		if tx, ok := q.(*sql.Tx); !ok || checkpointTxStateFor(ctx, tx) == nil {
			s.rememberCheckpointHistory(taskID, generation, checkpoints)
		}
		return checkpoints, generation, nil
	}
	return nil, 0, ErrConflict
}

func (s *Store) rememberCheckpointHistory(taskID string, generation int64, checkpoints []domain.Checkpoint) {
	var tail []domain.Checkpoint
	if len(checkpoints) > 0 {
		item, err := cloneCheckpoint(checkpoints[len(checkpoints)-1])
		if err != nil {
			return
		}
		tail = []domain.Checkpoint{item}
	}
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	if len(s.checkpointCache) >= 64 {
		s.checkpointCache = make(map[string]checkpointHistoryCache)
	}
	s.checkpointCache[taskID] = checkpointHistoryCache{generation: generation, checkpoints: tail}
}

func cloneCheckpoint(item domain.Checkpoint) (domain.Checkpoint, error) {
	encoded, err := json.Marshal(item.State)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	cloned, err := decodeCheckpointState(string(encoded))
	if err != nil {
		return domain.Checkpoint{}, err
	}
	item.State = cloned
	return item, nil
}
