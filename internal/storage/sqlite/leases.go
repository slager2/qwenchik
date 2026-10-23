package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var ErrLeaseLost = errors.New("execution lease lost")

const taskColumns = "id,title,description,status,stage,priority,plan_json,version,created_at,updated_at,tasklease_owner,tasklease_epoch,tasklease_expires_at,pause_reason,resource_reason,resume_after,resource_probe_attempts,terminal_outcome"

func (s *Store) EnableWorkerLeases(owner string, ttl time.Duration) error {
	if strings.TrimSpace(owner) == "" || len(owner) > 256 || ttl < 5*time.Second || ttl > 300*time.Second {
		return errors.New("lease requires owner and TTL between 5 and 300 seconds")
	}
	s.leaseOwner, s.leaseTTL = owner, ttl
	return nil
}

func fenceExecution(ctx context.Context, q conversationQuery, id string) error {
	var owner string
	var epoch int64
	var expiry sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT tasklease_owner,tasklease_epoch,tasklease_expires_at FROM tasks WHERE id=?`, id).Scan(&owner, &epoch, &expiry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	lease, present := domain.ExecutionLeaseFromContext(ctx)
	if owner == "" {
		if present {
			return ErrLeaseLost
		}
		return nil
	}
	if !present || owner != lease.Owner || epoch != lease.Epoch || !time.Now().Before(parseTime(expiry.String)) {
		return ErrLeaseLost
	}
	return nil
}

func clearLease(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET tasklease_owner='',tasklease_epoch=tasklease_epoch+1,tasklease_expires_at=NULL WHERE id=?`, id)
	return err
}

func (s *Store) renewLeaseTx(ctx context.Context, tx *sql.Tx, id string) error {
	if err := fenceExecution(ctx, tx, id); err != nil {
		return err
	}
	lease, ok := domain.ExecutionLeaseFromContext(ctx)
	if !ok {
		if s.leaseOwner != "" {
			return ErrLeaseLost
		}
		return nil
	}
	ttl := s.leaseTTL
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET tasklease_expires_at=? WHERE id=? AND tasklease_owner=? AND tasklease_epoch=?`, stamp(time.Now().Add(ttl)), id, lease.Owner, lease.Epoch)
	return err
}

func (s *Store) RenewTaskLease(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.renewLeaseTx(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReleaseTaskLease(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fenceExecution(ctx, tx, id); err != nil {
		return err
	}
	if err = clearLease(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func executionEvent(kind string) bool {
	for _, prefix := range []string{"tool.", "step.", "invocation.", "inference.", "source.", "research.", "verification.", "context.", "llm.", "plan.", "chat.", "search.", "query."} {
		if strings.HasPrefix(kind, prefix) {
			return true
		}
	}
	return kind == "message.delta" || kind == "memory.retrieved" || kind == "policy.decision"
}
