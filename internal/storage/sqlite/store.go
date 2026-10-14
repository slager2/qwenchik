package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/slager2/qwenchik/internal/domain"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var (
	ErrNotFound    = errors.New("not found")
	ErrConflict    = errors.New("conflict")
	ErrIdempotency = errors.New("idempotency key was used for another operation")
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve database path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		path = (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
	}
	dsn := path + "?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect SQLite database: %w", err)
	}
	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		migration, err := migrationFiles.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		var applied int
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE name=?`, entry.Name()).Scan(&applied)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, string(migration)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name,applied_at) VALUES(?,?)`, entry.Name(), stamp(time.Now().UTC())); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, task domain.Task, idempotencyKey string) (domain.Task, error) {
	if task.ID == "" {
		return domain.Task{}, errors.New("task id is required")
	}
	now := time.Now().UTC()
	task.Status = domain.StatusInbox
	task.Version = 1
	task.CreatedAt = now
	task.UpdatedAt = now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	if idempotencyKey != "" {
		existing, err := findIdempotency(ctx, tx, idempotencyKey, "tasks.create")
		if err == nil {
			return getTask(ctx, tx, existing)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Task{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,title,description,status,priority,version,created_at,updated_at,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?)`,
		task.ID, task.Title, task.Description, task.Status, task.Priority, task.Version, stamp(now), stamp(now), nullableKey(idempotencyKey))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return domain.Task{}, ErrConflict
		}
		return domain.Task{}, fmt.Errorf("insert task: %w", err)
	}
	if idempotencyKey != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(key,operation,resource_id,created_at) VALUES(?,?,?,?)`, idempotencyKey, "tasks.create", task.ID, stamp(now)); err != nil {
			return domain.Task{}, fmt.Errorf("save idempotency key: %w", err)
		}
	}
	if _, err := appendEvent(ctx, tx, task.ID, "task.created", nil, "user", map[string]any{"status": task.Status, "title": task.Title}); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func (s *Store) GetTask(ctx context.Context, id string) (domain.Task, error) {
	return getTask(ctx, s.db, id)
}

func (s *Store) ListTasks(ctx context.Context, limit int) ([]domain.Task, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,description,status,stage,priority,plan_json,version,created_at,updated_at FROM tasks ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

func (s *Store) TransitionTask(ctx context.Context, id string, to domain.TaskStatus, stage *domain.Stage, eventType string, visibility string, payload map[string]any) (domain.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status == to {
		return task, nil
	}
	if !domain.CanTransition(task.Status, to) {
		return domain.Task{}, fmt.Errorf("%w: %s -> %s", domain.ErrInvalidTransition, task.Status, to)
	}
	if !to.Valid() {
		return domain.Task{}, fmt.Errorf("unknown task status %q", to)
	}
	if eventType == "" {
		eventType = statusEventType(to)
	}
	if visibility == "" {
		visibility = "user"
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["from"] = task.Status
	payload["to"] = to
	updated := time.Now().UTC()
	if err := transitionStageTx(ctx, tx, id, task.Stage, stage, closeStageStatus(to), updated); err != nil {
		return domain.Task{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET status=?,stage=?,version=version+1,updated_at=? WHERE id=?`, to, nullableStage(stage), stamp(updated), id)
	if err != nil {
		return domain.Task{}, fmt.Errorf("update task status: %w", err)
	}
	if _, err := appendEvent(ctx, tx, id, eventType, stage, visibility, payload); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	task.Status = to
	task.Stage = stage
	task.Version++
	task.UpdatedAt = updated
	return task, nil
}

func (s *Store) QueueTask(ctx context.Context, id, idempotencyKey string) (domain.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	if idempotencyKey != "" {
		resourceID, err := findIdempotency(ctx, tx, idempotencyKey, "tasks.run")
		if err == nil {
			return getTask(ctx, tx, resourceID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Task{}, err
		}
	}
	task, err := getTask(ctx, tx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status != domain.StatusQueued {
		if !domain.CanTransition(task.Status, domain.StatusQueued) {
			return domain.Task{}, fmt.Errorf("%w: %s -> %s", domain.ErrInvalidTransition, task.Status, domain.StatusQueued)
		}
		now := time.Now().UTC()
		if err := transitionStageTx(ctx, tx, id, task.Stage, nil, "completed", now); err != nil {
			return domain.Task{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,stage=NULL,version=version+1,updated_at=? WHERE id=?`, domain.StatusQueued, stamp(now), id); err != nil {
			return domain.Task{}, err
		}
		if _, err := appendEvent(ctx, tx, id, "task.status_changed", nil, "user", map[string]any{"from": task.Status, "to": domain.StatusQueued}); err != nil {
			return domain.Task{}, err
		}
		task.Status = domain.StatusQueued
		task.Stage = nil
		task.Version++
		task.UpdatedAt = now
	}
	if idempotencyKey != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(key,operation,resource_id,created_at) VALUES(?,?,?,?)`, idempotencyKey, "tasks.run", id, stamp(time.Now().UTC())); err != nil {
			return domain.Task{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func (s *Store) AddMessage(ctx context.Context, message domain.Message, idempotencyKey string) (domain.Message, error) {
	if idempotencyKey != "" {
		var existing domain.Message
		var created string
		err := s.db.QueryRowContext(ctx, `SELECT id,task_id,role,content,created_at FROM messages WHERE idempotency_key=?`, idempotencyKey).
			Scan(&existing.ID, &existing.TaskID, &existing.Role, &existing.Content, &created)
		if err == nil {
			if existing.TaskID != message.TaskID || existing.Role != message.Role {
				return domain.Message{}, ErrIdempotency
			}
			existing.CreatedAt = parseTime(created)
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Message{}, err
		}
	}
	message.CreatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, message.TaskID)
	if err != nil {
		return domain.Message{}, err
	}
	if message.Role == "user" && task.Status != domain.StatusInbox && task.Status != domain.StatusQueued && task.Status != domain.StatusPaused {
		return domain.Message{}, fmt.Errorf("%w: task is not accepting user messages", ErrConflict)
	}
	if message.Role == "user" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE task_id=? AND role='user'`, message.TaskID).Scan(&count); err != nil {
			return domain.Message{}, err
		}
		if count >= 100 {
			return domain.Message{}, fmt.Errorf("%w: task message budget is exhausted", ErrConflict)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id,task_id,role,content,created_at,idempotency_key) VALUES(?,?,?,?,?,?)`, message.ID, message.TaskID, message.Role, message.Content, stamp(message.CreatedAt), nullableKey(idempotencyKey)); err != nil {
		return domain.Message{}, fmt.Errorf("insert message: %w", err)
	}
	if _, err := appendEvent(ctx, tx, message.TaskID, "message.created", nil, "user", map[string]any{"message_id": message.ID, "role": message.Role}); err != nil {
		return domain.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (s *Store) Messages(ctx context.Context, taskID string) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,role,content,created_at FROM (SELECT id,task_id,role,content,created_at FROM messages WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 200) ORDER BY created_at,id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Message
	for rows.Next() {
		var message domain.Message
		var created string
		if err := rows.Scan(&message.ID, &message.TaskID, &message.Role, &message.Content, &created); err != nil {
			return nil, err
		}
		message.CreatedAt = parseTime(created)
		result = append(result, message)
	}
	return result, rows.Err()
}

func (s *Store) EventsAfter(ctx context.Context, taskID string, after int64, limit int) ([]domain.Event, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,seq,type,schema_version,created_at,stage,visibility,payload_json FROM events WHERE task_id=? AND seq>? ORDER BY seq LIMIT ?`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Event
	for rows.Next() {
		var event domain.Event
		var created, stage sql.NullString
		var payload string
		if err := rows.Scan(&event.ID, &event.TaskID, &event.Seq, &event.Type, &event.SchemaVersion, &created, &stage, &event.Visibility, &payload); err != nil {
			return nil, err
		}
		event.CreatedAt = parseTime(created.String)
		event.Payload = json.RawMessage(payload)
		if stage.Valid {
			value := domain.Stage(stage.String)
			event.Stage = &value
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) RecordEvent(ctx context.Context, taskID, eventType string, stage *domain.Stage, visibility string, payload map[string]any) (domain.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, err
	}
	defer tx.Rollback()
	if _, err := getTask(ctx, tx, taskID); err != nil {
		return domain.Event{}, err
	}
	if visibility == "" {
		visibility = "user"
	}
	seq, err := appendEvent(ctx, tx, taskID, eventType, stage, visibility, payload)
	if err != nil {
		return domain.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Event{}, err
	}
	events, err := s.EventsAfter(ctx, taskID, seq-1, 1)
	if err != nil {
		return domain.Event{}, err
	}
	if len(events) != 1 {
		return domain.Event{}, errors.New("committed event could not be read back")
	}
	return events[0], nil
}

func (s *Store) SavePlan(ctx context.Context, taskID string, plan domain.Plan, checkpointState any) (int64, error) {
	if err := domain.ValidatePlan(plan, 12); err != nil {
		return 0, err
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var status domain.TaskStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if status != domain.StatusPlanning {
		return 0, fmt.Errorf("%w: plan can only be saved while planning", ErrConflict)
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET plan_json=?,version=version+1,updated_at=? WHERE id=?`, string(planJSON), stamp(time.Now().UTC()), taskID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_steps WHERE task_id=?`, taskID); err != nil {
		return 0, err
	}
	for index, step := range plan.Subtasks {
		dependencies, err := json.Marshal(step.Dependencies)
		if err != nil {
			return 0, err
		}
		criteria, err := json.Marshal(step.SuccessCriteria)
		if err != nil {
			return 0, err
		}
		now := stamp(time.Now().UTC())
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_steps(id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			newID(), taskID, step.ID, index, step.Kind, step.Description, string(dependencies), step.ExpectedOutput, string(criteria), "pending", now, now); err != nil {
			return 0, err
		}
	}
	seq, err := appendEvent(ctx, tx, taskID, "plan.created", stagePointer(domain.StagePlanning), "user", map[string]any{"steps": len(plan.Subtasks), "goal": plan.Goal})
	if err != nil {
		return 0, err
	}
	if err := insertCheckpoint(ctx, tx, taskID, 1, seq, checkpointState); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) SetStepStatus(ctx context.Context, taskID, stepID, status string) error {
	if status != "pending" && status != "running" && status != "completed" && status != "failed" {
		return fmt.Errorf("invalid step status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE task_steps SET status=?,attempts=attempts+CASE WHEN ?='running' THEN 1 ELSE 0 END,updated_at=? WHERE task_id=? AND step_id=?`, status, status, stamp(time.Now().UTC()), taskID, stepID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Steps(ctx context.Context, taskID string) ([]domain.TaskStep, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,attempts,created_at,updated_at FROM task_steps WHERE task_id=? ORDER BY position`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.TaskStep
	for rows.Next() {
		var step domain.TaskStep
		var dependencies, criteria, created, updated string
		if err := rows.Scan(&step.ID, &step.TaskID, &step.StepID, &step.Position, &step.Kind, &step.Description, &dependencies, &step.ExpectedOutput, &criteria, &step.Status, &step.Attempts, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dependencies), &step.Dependencies); err != nil {
			return nil, fmt.Errorf("decode step dependencies: %w", err)
		}
		if err := json.Unmarshal([]byte(criteria), &step.SuccessCriteria); err != nil {
			return nil, fmt.Errorf("decode step criteria: %w", err)
		}
		step.CreatedAt = parseTime(created)
		step.UpdatedAt = parseTime(updated)
		result = append(result, step)
	}
	return result, rows.Err()
}

func (s *Store) SaveCheckpoint(ctx context.Context, taskID string, planVersion int, state any) (domain.Checkpoint, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	defer tx.Rollback()
	if _, err := getTask(ctx, tx, taskID); err != nil {
		return domain.Checkpoint{}, err
	}
	seq, err := appendEvent(ctx, tx, taskID, "checkpoint.saved", nil, "internal", map[string]any{"plan_version": planVersion})
	if err != nil {
		return domain.Checkpoint{}, err
	}
	checkpoint := domain.Checkpoint{ID: newID(), TaskID: taskID, PlanVersion: planVersion, State: state, LastEventSeq: seq, CreatedAt: time.Now().UTC()}
	stateJSON, err := json.Marshal(checkpoint.State)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints(id,task_id,plan_version,state_json,last_event_seq,created_at) VALUES(?,?,?,?,?,?)`, checkpoint.ID, checkpoint.TaskID, checkpoint.PlanVersion, string(stateJSON), checkpoint.LastEventSeq, stamp(checkpoint.CreatedAt)); err != nil {
		return domain.Checkpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Checkpoint{}, err
	}
	return checkpoint, nil
}

func (s *Store) Checkpoints(ctx context.Context, taskID string) ([]domain.Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,plan_version,state_json,last_event_seq,created_at FROM (SELECT id,task_id,plan_version,state_json,last_event_seq,created_at FROM checkpoints WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 500) ORDER BY created_at,id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Checkpoint
	for rows.Next() {
		var item domain.Checkpoint
		var stateJSON, created string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.PlanVersion, &stateJSON, &item.LastEventSeq, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(stateJSON), &item.State); err != nil {
			return nil, fmt.Errorf("decode checkpoint %s: %w", item.ID, err)
		}
		item.CreatedAt = parseTime(created)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) LatestCheckpoint(ctx context.Context, taskID string) (domain.Checkpoint, error) {
	var item domain.Checkpoint
	var stateJSON, created string
	err := s.db.QueryRowContext(ctx, `SELECT id,task_id,plan_version,state_json,last_event_seq,created_at FROM checkpoints WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, taskID).
		Scan(&item.ID, &item.TaskID, &item.PlanVersion, &stateJSON, &item.LastEventSeq, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Checkpoint{}, ErrNotFound
	}
	if err != nil {
		return domain.Checkpoint{}, err
	}
	if err := json.Unmarshal([]byte(stateJSON), &item.State); err != nil {
		return domain.Checkpoint{}, fmt.Errorf("decode checkpoint %s: %w", item.ID, err)
	}
	item.CreatedAt = parseTime(created)
	return item, nil
}

func (s *Store) CompleteTask(ctx context.Context, taskID, answer string, checkpointState any) (domain.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status == domain.StatusCompleted {
		return task, nil
	}
	if task.Status != domain.StatusVerifying {
		return domain.Task{}, fmt.Errorf("%w: task must be verifying before finalization", ErrConflict)
	}
	now := time.Now().UTC()
	stage := domain.StageFinalization
	if err := transitionStageTx(ctx, tx, taskID, task.Stage, &stage, "completed", now); err != nil {
		return domain.Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id,task_id,role,content,created_at) VALUES(?,?,?,?,?)`, newID(), taskID, "assistant", answer, stamp(now)); err != nil {
		return domain.Task{}, err
	}
	if _, err := appendEvent(ctx, tx, taskID, "message.created", &stage, "user", map[string]any{"role": "assistant"}); err != nil {
		return domain.Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,stage=?,version=version+1,updated_at=? WHERE id=?`, domain.StatusCompleted, stage, stamp(now), taskID); err != nil {
		return domain.Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_stages SET status='completed',finished_at=?,updated_at=? WHERE task_id=? AND stage=? AND status='running'`, stamp(now), stamp(now), taskID, stage); err != nil {
		return domain.Task{}, err
	}
	seq, err := appendEvent(ctx, tx, taskID, "task.completed", &stage, "user", map[string]any{"status": domain.StatusCompleted})
	if err != nil {
		return domain.Task{}, err
	}
	if err := insertCheckpoint(ctx, tx, taskID, 1, seq, checkpointState); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	task.Status = domain.StatusCompleted
	task.Stage = &stage
	task.Version++
	task.UpdatedAt = now
	return task, nil
}

func (s *Store) ClaimNext(ctx context.Context) (domain.Task, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE status=? ORDER BY created_at,id LIMIT 1`, domain.StatusQueued).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, false, nil
	}
	if err != nil {
		return domain.Task{}, false, err
	}
	task, err := getTask(ctx, tx, id)
	if err != nil {
		return domain.Task{}, false, err
	}
	stage := domain.StagePlanning
	now := time.Now().UTC()
	if err := transitionStageTx(ctx, tx, id, task.Stage, &stage, "completed", now); err != nil {
		return domain.Task{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,stage=?,version=version+1,updated_at=? WHERE id=? AND status=?`, domain.StatusPlanning, stage, stamp(now), id, domain.StatusQueued); err != nil {
		return domain.Task{}, false, err
	}
	if _, err := appendEvent(ctx, tx, id, "task.status_changed", &stage, "user", map[string]any{"from": task.Status, "to": domain.StatusPlanning}); err != nil {
		return domain.Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, false, err
	}
	task.Status = domain.StatusPlanning
	task.Stage = &stage
	task.Version++
	task.UpdatedAt = now
	return task, true, nil
}

func (s *Store) RecoverInterrupted(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,status,stage FROM tasks WHERE status IN (?,?,?)`, domain.StatusPlanning, domain.StatusRunning, domain.StatusVerifying)
	if err != nil {
		return 0, err
	}
	type interrupted struct {
		id     string
		status domain.TaskStatus
		stage  sql.NullString
	}
	var tasks []interrupted
	for rows.Next() {
		var item interrupted
		if err := rows.Scan(&item.id, &item.status, &item.stage); err != nil {
			_ = rows.Close()
			return 0, err
		}
		tasks = append(tasks, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, item := range tasks {
		now := time.Now().UTC()
		var previousStage *domain.Stage
		if item.stage.Valid {
			value := domain.Stage(item.stage.String)
			previousStage = &value
		}
		if err := transitionStageTx(ctx, tx, item.id, previousStage, nil, "paused", now); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,stage=NULL,version=version+1,updated_at=? WHERE id=?`, domain.StatusQueued, stamp(now), item.id); err != nil {
			return 0, err
		}
		if _, err := appendEvent(ctx, tx, item.id, "task.recovered", nil, "user", map[string]any{"previous_status": item.status, "status": domain.StatusQueued}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(tasks)), nil
}

func getTask(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.Task, error) {
	row := q.QueryRowContext(ctx, `SELECT id,title,description,status,stage,priority,plan_json,version,created_at,updated_at FROM tasks WHERE id=?`, id)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrNotFound
	}
	return task, err
}

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (domain.Task, error) {
	var task domain.Task
	var stage, planJSON, created, updated sql.NullString
	if err := row.Scan(&task.ID, &task.Title, &task.Description, &task.Status, &stage, &task.Priority, &planJSON, &task.Version, &created, &updated); err != nil {
		return domain.Task{}, err
	}
	if stage.Valid {
		value := domain.Stage(stage.String)
		task.Stage = &value
	}
	if planJSON.Valid {
		var plan domain.Plan
		if err := json.Unmarshal([]byte(planJSON.String), &plan); err != nil {
			return domain.Task{}, fmt.Errorf("decode task plan: %w", err)
		}
		task.Plan = &plan
	}
	task.CreatedAt = parseTime(created.String)
	task.UpdatedAt = parseTime(updated.String)
	return task, nil
}

func appendEvent(ctx context.Context, tx *sql.Tx, taskID, eventType string, stage *domain.Stage, visibility string, payload any) (int64, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE task_id=?`, taskID).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(id,task_id,seq,type,schema_version,created_at,stage,visibility,payload_json) VALUES(?,?,?,?,?,?,?,?,?)`, newID(), taskID, seq, eventType, 1, stamp(time.Now().UTC()), nullableStage(stage), visibility, string(encoded)); err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}
	return seq, nil
}

func transitionStageTx(ctx context.Context, tx *sql.Tx, taskID string, previous, next *domain.Stage, closeStatus string, now time.Time) error {
	if previous != nil && (next == nil || *previous != *next) {
		if _, err := tx.ExecContext(ctx, `UPDATE task_stages SET status=?,finished_at=?,updated_at=? WHERE task_id=? AND stage=? AND status='running'`, closeStatus, stamp(now), stamp(now), taskID, *previous); err != nil {
			return err
		}
	}
	if next != nil && (previous == nil || *previous != *next) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_stages(id,task_id,stage,status,started_at,updated_at) VALUES(?,?,?,?,?,?)`, newID(), taskID, *next, "running", stamp(now), stamp(now)); err != nil {
			return err
		}
	}
	return nil
}

func closeStageStatus(status domain.TaskStatus) string {
	switch status {
	case domain.StatusPaused:
		return "paused"
	case domain.StatusFailed:
		return "failed"
	case domain.StatusCancelled:
		return "cancelled"
	default:
		return "completed"
	}
}

func insertCheckpoint(ctx context.Context, tx *sql.Tx, taskID string, planVersion int, seq int64, state any) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO checkpoints(id,task_id,plan_version,state_json,last_event_seq,created_at) VALUES(?,?,?,?,?,?)`, newID(), taskID, planVersion, string(encoded), seq, stamp(time.Now().UTC()))
	return err
}

func findIdempotency(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key, operation string) (string, error) {
	var actualOperation, resourceID string
	if err := q.QueryRowContext(ctx, `SELECT operation,resource_id FROM idempotency_keys WHERE key=?`, key).Scan(&actualOperation, &resourceID); err != nil {
		return "", err
	}
	if actualOperation != operation {
		return "", ErrIdempotency
	}
	return resourceID, nil
}

func statusEventType(status domain.TaskStatus) string {
	switch status {
	case domain.StatusCompleted:
		return "task.completed"
	case domain.StatusFailed:
		return "task.failed"
	case domain.StatusCancelled:
		return "task.cancelled"
	default:
		return "task.status_changed"
	}
}

func nullableStage(stage *domain.Stage) any {
	if stage == nil {
		return nil
	}
	return string(*stage)
}

func stagePointer(stage domain.Stage) *domain.Stage { return &stage }

func nullableKey(key string) any {
	if key == "" {
		return nil
	}
	return key
}

func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func newID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic("cryptographic random source unavailable: " + err.Error())
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16])
}
