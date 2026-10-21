package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func (s *Store) PauseTaskForResource(ctx context.Context, taskID, reason string, nextProbe time.Time, state any) (domain.Task, error) {
	if reason == "" || len(reason) > 1024 || nextProbe.IsZero() {
		return domain.Task{}, errors.New("resource reason and next probe required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status.Terminal() {
		return task, nil
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return domain.Task{}, err
	}
	if task.Status == domain.StatusPaused {
		if task.PauseReason == "resource_guard" {
			return task, nil
		}
		return domain.Task{}, ErrConflict
	}
	if task.Status != domain.StatusPlanning && task.Status != domain.StatusRunning && task.Status != domain.StatusVerifying {
		return domain.Task{}, ErrConflict
	}
	if state == nil {
		prior, err := verifiedCheckpoints(ctx, tx, taskID)
		if err != nil {
			return domain.Task{}, err
		}
		if len(prior) > 0 {
			state = prior[len(prior)-1].State
		}
	}
	now := time.Now().UTC()
	if err := transitionStageTx(ctx, tx, taskID, task.Stage, nil, "paused", now); err != nil {
		return domain.Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='paused',stage=NULL,pause_reason='resource_guard',resource_reason=?,resume_after=?,resource_probe_attempts=0,version=version+1,updated_at=? WHERE id=? AND cancel_requested=0`, reason, stamp(nextProbe), stamp(now), taskID); err != nil {
		return domain.Task{}, err
	}
	var cancelled bool
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested FROM tasks WHERE id=?`, taskID).Scan(&cancelled); err != nil {
		return domain.Task{}, err
	}
	if cancelled {
		return task, ErrConflict
	}
	if err := updateResearchSessionStatus(ctx, tx, taskID, domain.StatusPaused, "paused_resource_guard", false, "", now); err != nil {
		return domain.Task{}, err
	}
	seq, err := appendEvent(ctx, tx, taskID, "run.paused", nil, "user", map[string]any{"status": domain.StatusPaused, "pause_reason": "resource_guard", "resource_reason": reason, "resume_after": nextProbe, "resource_probe_attempts": 0})
	if err != nil {
		return domain.Task{}, err
	}
	if err := insertCheckpoint(ctx, tx, taskID, 1, seq, state); err != nil {
		return domain.Task{}, err
	}
	if err := clearLease(ctx, tx, taskID); err != nil {
		return domain.Task{}, err
	}
	task, err = getTask(ctx, tx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func (s *Store) ResourcePausedTasks(ctx context.Context, now time.Time, limit int) ([]domain.Task, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE status='paused' AND pause_reason='resource_guard' AND cancel_requested=0 AND tasklease_owner='' AND julianday(resume_after)<=julianday(?) ORDER BY resume_after,id LIMIT ?`, stamp(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []domain.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) ResolveResourceProbe(ctx context.Context, taskID string, healthy bool, reason string, nextProbe time.Time, maxAttempts int) (domain.Task, error) {
	if maxAttempts < 1 || maxAttempts > 1000 || len(reason) > 1024 || !healthy && !nextProbe.After(time.Now()) {
		return domain.Task{}, errors.New("invalid resource probe policy")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status != domain.StatusPaused || task.PauseReason != "resource_guard" {
		return task, nil
	}
	var cancelled bool
	var deadline *string
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested,deadline_at FROM tasks WHERE id=?`, taskID).Scan(&cancelled, &deadline); err != nil {
		return domain.Task{}, err
	}
	if cancelled {
		return task, nil
	}
	now := time.Now().UTC()
	expired := deadline != nil && !now.Before(parseTime(*deadline))
	session, sessionErr := researchSession(ctx, tx, taskID)
	if sessionErr != nil && !errors.Is(sessionErr, ErrNotFound) {
		return domain.Task{}, sessionErr
	}
	if sessionErr == nil && !now.Before(session.DeadlineAt) {
		expired = true
	}
	if task.ResumeAfter != nil && now.Before(*task.ResumeAfter) && !expired {
		return task, nil
	}
	status := domain.StatusPaused
	outcome, code, eventType := "", "", "run.resource_probe"
	attempts := task.ResourceProbeAttempts
	if expired {
		status = domain.StatusFailed
		outcome = "budget_exhausted"
		code = "deadline_exceeded"
		eventType = "run.failed"
	} else if healthy {
		status = domain.StatusQueued
		eventType = "run.resumed"
	} else {
		attempts++
		if attempts >= maxAttempts {
			status = domain.StatusFailed
			outcome = "blocked"
			code = "resource_guard_exhausted"
			eventType = "run.blocked"
		}
	}
	pauseReason := "resource_guard"
	var resume any = stamp(nextProbe)
	if status != domain.StatusPaused {
		pauseReason = ""
		resume = nil
	}
	if status == domain.StatusQueued {
		reason = ""
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,pause_reason=?,resource_reason=?,resume_after=?,resource_probe_attempts=?,terminal_outcome=?,error_code=?,version=version+1,updated_at=? WHERE id=?`, status, pauseReason, reason, resume, attempts, outcome, code, stamp(now), taskID); err != nil {
		return domain.Task{}, err
	}
	if err := clearLease(ctx, tx, taskID); err != nil {
		return domain.Task{}, err
	}
	payload := map[string]any{"status": status, "pause_reason": pauseReason, "resource_reason": reason, "resource_probe_attempts": attempts, "healthy": healthy, "terminal_outcome": outcome, "error_code": code}
	if resume != nil {
		payload["resume_after"] = nextProbe
	}
	if _, err := appendEvent(ctx, tx, taskID, eventType, nil, "user", payload); err != nil {
		return domain.Task{}, err
	}
	if status.Terminal() {
		if err := syncConversationTerminal(ctx, tx, taskID, status, payload); err != nil {
			return domain.Task{}, err
		}
	}
	if err := updateResearchSessionStatus(ctx, tx, taskID, status, string(status), status.Terminal(), code, now); err != nil {
		return domain.Task{}, err
	}
	task, err = getTask(ctx, tx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}
