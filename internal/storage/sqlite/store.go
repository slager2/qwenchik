package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
	"github.com/slager2/qwenchik/internal/domain"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var (
	ErrNotFound                = errors.New("not found")
	ErrConflict                = errors.New("conflict")
	ErrIdempotency             = errors.New("idempotency key was used for another operation")
	ErrApprovalExpired         = errors.New("approval expired")
	ErrApprovalAlreadyConsumed = errors.New("approval was already consumed")
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	if path == ":memory:" {
		path = "file:sqlite-memory-" + newID() + "?mode=memory&cache=shared"
	} else {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve database path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		path = (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	dsn := path + separator + "_busy_timeout=10000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
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
	if message.Role == "user" && task.Status != domain.StatusInbox && task.Status != domain.StatusQueued && task.Status != domain.StatusPaused && task.Status != domain.StatusFailed {
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

func (s *Store) RecordMessageDelta(ctx context.Context, taskID, streamID, messageID string, chunkIndex int, delta string) (domain.Event, error) {
	if strings.TrimSpace(streamID) != streamID || streamID == "" || len(streamID) > 128 ||
		strings.TrimSpace(messageID) != messageID || messageID == "" || len(messageID) > 128 ||
		chunkIndex < 0 || delta == "" || len(delta) > 4<<10 || !utf8.ValidString(delta) {
		return domain.Event{}, errors.New("message delta fields are invalid or exceed their bounds")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return domain.Event{}, err
	}
	if task.Status != domain.StatusVerifying {
		return domain.Event{}, fmt.Errorf("%w: message deltas are only accepted during final synthesis", ErrConflict)
	}
	var previousPayload string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE task_id=? AND type='message.delta' ORDER BY seq DESC LIMIT 1`, taskID).Scan(&previousPayload)
	if err == nil {
		var previous struct {
			StreamID   string `json:"stream_id"`
			MessageID  string `json:"message_id"`
			ChunkIndex int    `json:"chunk_index"`
		}
		if err := json.Unmarshal([]byte(previousPayload), &previous); err != nil {
			return domain.Event{}, fmt.Errorf("decode previous message delta: %w", err)
		}
		if streamID == previous.StreamID {
			if messageID != previous.MessageID || chunkIndex != previous.ChunkIndex+1 {
				return domain.Event{}, fmt.Errorf("%w: message delta index or message ID is not consecutive", ErrConflict)
			}
		} else if chunkIndex != 0 {
			return domain.Event{}, fmt.Errorf("%w: a new message stream must start at chunk index zero", ErrConflict)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.Event{}, err
	} else if chunkIndex != 0 {
		return domain.Event{}, fmt.Errorf("%w: a message stream must start at chunk index zero", ErrConflict)
	}
	seq, err := appendEvent(ctx, tx, taskID, "message.delta", task.Stage, "user", map[string]any{
		"stream_id": streamID, "message_id": messageID, "chunk_index": chunkIndex, "delta": delta,
	})
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
		return domain.Event{}, errors.New("committed message delta could not be read back")
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
	if status != "pending" && status != "running" && status != "completed" && status != "failed" && status != "skipped" {
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

func (s *Store) SaveArtifact(ctx context.Context, artifact domain.Artifact, storagePath string) (domain.Artifact, error) {
	if artifact.ID == "" || artifact.TaskID == "" || !safeArtifactReference(storagePath) {
		return domain.Artifact{}, errors.New("invalid artifact metadata or storage reference")
	}
	if _, err := s.GetTask(ctx, artifact.TaskID); err != nil {
		return domain.Artifact{}, err
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Artifact{}, err
	}
	defer tx.Rollback()
	var existing domain.Artifact
	var existingPath string
	var created string
	err = tx.QueryRowContext(ctx, `SELECT id,task_id,name,mime_type,sha256,size_bytes,storage_path,created_at FROM artifacts WHERE task_id=? AND id=?`, artifact.TaskID, artifact.ID).
		Scan(&existing.ID, &existing.TaskID, &existing.Name, &existing.MIMEType, &existing.SHA256, &existing.SizeBytes, &existingPath, &created)
	if err == nil {
		if existingPath != storagePath || existing.SHA256 != artifact.SHA256 || existing.MIMEType != artifact.MIMEType || existing.SizeBytes != artifact.SizeBytes {
			return domain.Artifact{}, ErrConflict
		}
		existing.CreatedAt = parseTime(created)
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id,task_id,name,mime_type,sha256,size_bytes,storage_path,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		artifact.ID, artifact.TaskID, artifact.Name, artifact.MIMEType, artifact.SHA256, artifact.SizeBytes, storagePath, stamp(artifact.CreatedAt)); err != nil {
		return domain.Artifact{}, err
	}
	if _, err := appendEvent(ctx, tx, artifact.TaskID, "artifact.created", nil, "user", map[string]any{
		"artifact_id": artifact.ID, "name": artifact.Name, "mime_type": artifact.MIMEType,
		"size_bytes": artifact.SizeBytes, "sha256": artifact.SHA256,
	}); err != nil {
		return domain.Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) Artifacts(ctx context.Context, taskID string) ([]domain.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,name,mime_type,sha256,size_bytes,created_at FROM artifacts WHERE task_id=? ORDER BY created_at DESC LIMIT 500`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Artifact
	for rows.Next() {
		var artifact domain.Artifact
		var created string
		if err := rows.Scan(&artifact.ID, &artifact.TaskID, &artifact.Name, &artifact.MIMEType, &artifact.SHA256, &artifact.SizeBytes, &created); err != nil {
			return nil, err
		}
		artifact.CreatedAt = parseTime(created)
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func (s *Store) SaveEvidence(ctx context.Context, evidence domain.Evidence) (domain.Evidence, error) {
	if evidence.TaskID == "" || evidence.Provider == "" || evidence.CanonicalURL == "" || evidence.OriginalURL == "" || evidence.ContentSHA256 == "" {
		return domain.Evidence{}, errors.New("evidence is missing required provenance fields")
	}
	if _, err := s.GetTask(ctx, evidence.TaskID); err != nil {
		return domain.Evidence{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Evidence{}, err
	}
	defer tx.Rollback()
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM evidence WHERE task_id=? AND canonical_url=? AND content_sha256=?`, evidence.TaskID, evidence.CanonicalURL, evidence.ContentSHA256).Scan(&existingID)
	if err == nil {
		evidence.ID = existingID
		return evidence, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Evidence{}, err
	}
	if evidence.ID == "" {
		evidence.ID = newID()
	}
	if evidence.RetrievedAt.IsZero() {
		evidence.RetrievedAt = time.Now().UTC()
	}
	quotes, err := json.Marshal(evidence.QuoteSpans)
	if err != nil {
		return domain.Evidence{}, err
	}
	flags, err := json.Marshal(evidence.QualityFlags)
	if err != nil {
		return domain.Evidence{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evidence(id,task_id,provider,original_query,original_url,canonical_url,title,language,retrieved_at,http_status,content_sha256,extraction_method,raw_artifact_id,text_artifact_id,quote_spans_json,quality_flags_json,excerpt) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		evidence.ID, evidence.TaskID, evidence.Provider, evidence.OriginalQuery, evidence.OriginalURL, evidence.CanonicalURL,
		evidence.Title, evidence.Language, stamp(evidence.RetrievedAt), evidence.HTTPStatus, evidence.ContentSHA256,
		evidence.ExtractionMethod, nullableKey(evidence.RawArtifactID), nullableKey(evidence.TextArtifactID), string(quotes), string(flags), evidence.Excerpt); err != nil {
		return domain.Evidence{}, err
	}
	if _, err := appendEvent(ctx, tx, evidence.TaskID, "evidence.created", nil, "user", map[string]any{
		"evidence_id": evidence.ID, "title": evidence.Title, "canonical_url": evidence.CanonicalURL,
	}); err != nil {
		return domain.Evidence{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Evidence{}, err
	}
	if evidence.QuoteSpans == nil {
		evidence.QuoteSpans = []domain.QuoteSpan{}
	}
	if evidence.QualityFlags == nil {
		evidence.QualityFlags = []string{}
	}
	return evidence, nil
}

func (s *Store) Evidence(ctx context.Context, taskID string) ([]domain.Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,provider,original_query,original_url,canonical_url,title,language,retrieved_at,http_status,content_sha256,extraction_method,COALESCE(raw_artifact_id,''),COALESCE(text_artifact_id,''),quote_spans_json,quality_flags_json,excerpt FROM evidence WHERE task_id=? ORDER BY retrieved_at DESC LIMIT 500`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Evidence
	for rows.Next() {
		var item domain.Evidence
		var retrieved, quotes, flags string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Provider, &item.OriginalQuery, &item.OriginalURL, &item.CanonicalURL, &item.Title, &item.Language, &retrieved, &item.HTTPStatus, &item.ContentSHA256, &item.ExtractionMethod, &item.RawArtifactID, &item.TextArtifactID, &quotes, &flags, &item.Excerpt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(quotes), &item.QuoteSpans); err != nil {
			return nil, fmt.Errorf("decode evidence quote spans: %w", err)
		}
		if err := json.Unmarshal([]byte(flags), &item.QualityFlags); err != nil {
			return nil, fmt.Errorf("decode evidence quality flags: %w", err)
		}
		item.RetrievedAt = parseTime(retrieved)
		item.Taint = "untrusted_external_content"
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateApproval(ctx context.Context, approval domain.Approval, checkpointState any) (domain.Approval, error) {
	arguments, hash, err := normalizeApprovalArguments(approval.Arguments)
	if err != nil {
		return domain.Approval{}, err
	}
	if approval.TaskID == "" || approval.ToolName == "" || strings.TrimSpace(approval.RiskExplanation) == "" || len(arguments) > 64<<10 {
		return domain.Approval{}, errors.New("approval is missing required or bounded fields")
	}
	now := time.Now().UTC()
	if approval.ID == "" {
		approval.ID = newID()
	}
	if approval.ExpiresAt.IsZero() {
		approval.ExpiresAt = now.Add(10 * time.Minute)
	}
	if !approval.ExpiresAt.After(now) {
		return domain.Approval{}, errors.New("approval expiry must be in the future")
	}
	approval.Arguments = arguments
	approval.ArgumentsSHA256 = hash
	approval.Status = domain.ApprovalPending
	approval.CreatedAt = now
	if approval.Actor == "" {
		approval.Actor = "policy"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback()
	task, err := getTask(ctx, tx, approval.TaskID)
	if err != nil {
		return domain.Approval{}, err
	}
	if err := expireMatchingApprovalsTx(ctx, tx, approval.TaskID, approval.ToolName, hash, now); err != nil {
		return domain.Approval{}, err
	}
	var existing domain.Approval
	existing, err = scanApproval(tx.QueryRowContext(ctx, `SELECT id,task_id,tool_name,arguments_json,arguments_sha256,target,risk_explanation,expires_at,status,created_at,decision_at,actor,consumed_at FROM approvals WHERE task_id=? AND tool_name=? AND arguments_sha256=? AND status='pending' AND consumed_at IS NULL AND expires_at>? ORDER BY created_at DESC LIMIT 1`, approval.TaskID, approval.ToolName, hash, stamp(now)))
	if err == nil {
		if task.Status == domain.StatusRunning {
			if err := s.moveTaskForApproval(ctx, tx, task, now); err != nil {
				return domain.Approval{}, err
			}
			if err := saveApprovalCheckpoint(ctx, tx, approval.TaskID, checkpointState, existing.ID, string(existing.Status)); err != nil {
				return domain.Approval{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.Approval{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Approval{}, err
	}
	if task.Status != domain.StatusRunning {
		return domain.Approval{}, fmt.Errorf("%w: approval can only be requested while running", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approvals(id,task_id,tool_name,arguments_json,target,risk_explanation,expires_at,status,created_at,actor,arguments_sha256) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		approval.ID, approval.TaskID, approval.ToolName, string(approval.Arguments), approval.Target, approval.RiskExplanation, stamp(approval.ExpiresAt), approval.Status, stamp(approval.CreatedAt), approval.Actor, approval.ArgumentsSHA256); err != nil {
		return domain.Approval{}, err
	}
	if err := s.moveTaskForApproval(ctx, tx, task, now); err != nil {
		return domain.Approval{}, err
	}
	if _, err := appendEvent(ctx, tx, approval.TaskID, "approval.requested", nil, "user", map[string]any{
		"approval_id": approval.ID, "tool": approval.ToolName, "target": approval.Target,
		"risk_explanation": approval.RiskExplanation, "expires_at": stamp(approval.ExpiresAt),
	}); err != nil {
		return domain.Approval{}, err
	}
	if err := saveApprovalCheckpoint(ctx, tx, approval.TaskID, checkpointState, approval.ID, string(approval.Status)); err != nil {
		return domain.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Approval{}, err
	}
	return approval, nil
}

func (s *Store) Approval(ctx context.Context, id string) (domain.Approval, error) {
	approval, err := getApproval(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Approval{}, ErrNotFound
	}
	return approval, err
}

func (s *Store) ApprovalsForTask(ctx context.Context, taskID string) ([]domain.Approval, error) {
	return queryApprovals(ctx, s.db, `SELECT id,task_id,tool_name,arguments_json,arguments_sha256,target,risk_explanation,expires_at,status,created_at,decision_at,actor,consumed_at FROM approvals WHERE task_id=? ORDER BY created_at DESC LIMIT 100`, taskID)
}

func (s *Store) PendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	return queryApprovals(ctx, s.db, `SELECT id,task_id,tool_name,arguments_json,arguments_sha256,target,risk_explanation,expires_at,status,created_at,decision_at,actor,consumed_at FROM approvals WHERE status='pending' AND expires_at>? ORDER BY created_at LIMIT 100`, stamp(time.Now().UTC()))
}

func (s *Store) ExpireApprovals(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,task_id FROM approvals WHERE status='pending' AND expires_at<=? ORDER BY expires_at LIMIT 100`, stamp(now))
	if err != nil {
		return 0, err
	}
	type expiredApproval struct{ id, taskID string }
	var expired []expiredApproval
	for rows.Next() {
		var item expiredApproval
		if err := rows.Scan(&item.id, &item.taskID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		expired = append(expired, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, item := range expired {
		if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decision_at=?,actor='policy' WHERE id=? AND status='pending'`, domain.ApprovalExpired, stamp(now), item.id); err != nil {
			return 0, err
		}
		if _, err := appendEvent(ctx, tx, item.taskID, "approval.expired", nil, "user", map[string]any{"approval_id": item.id}); err != nil {
			return 0, err
		}
		task, err := getTask(ctx, tx, item.taskID)
		if err != nil {
			return 0, err
		}
		activeApproval, err := hasActivePendingApprovalTx(ctx, tx, item.taskID, now)
		if err != nil {
			return 0, err
		}
		if task.Status == domain.StatusWaitingApproval && !activeApproval {
			if err := transitionStatusTx(ctx, tx, task, domain.StatusFailed, nil, "task.failed", map[string]any{"error_code": "approval_expired"}, now); err != nil {
				return 0, err
			}
		}
		if err := saveApprovalCheckpoint(ctx, tx, item.taskID, nil, item.id, string(domain.ApprovalExpired)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(expired), nil
}

func (s *Store) DecideApproval(ctx context.Context, id string, approve bool, actor, idempotencyKey string) (domain.Approval, error) {
	operation := "approvals.reject"
	approvalStatus := domain.ApprovalRejected
	taskStatus := domain.StatusCancelled
	if approve {
		operation = "approvals.approve"
		approvalStatus = domain.ApprovalApproved
		taskStatus = domain.StatusQueued
	}
	if actor == "" {
		actor = "local_user"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback()
	if idempotencyKey != "" {
		resourceID, err := findIdempotency(ctx, tx, idempotencyKey, operation)
		if err == nil {
			return getApproval(ctx, tx, resourceID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Approval{}, err
		}
	}
	approval, err := getApproval(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if approval.Status != domain.ApprovalPending {
		if approval.Status != approvalStatus {
			return domain.Approval{}, fmt.Errorf("%w: approval already has a different decision", ErrConflict)
		}
		if idempotencyKey != "" {
			if err := insertApprovalIdempotency(ctx, tx, idempotencyKey, operation, id); err != nil {
				return domain.Approval{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.Approval{}, err
		}
		return approval, nil
	}
	now := time.Now().UTC()
	approval.Actor = actor
	approval.DecisionAt = &now
	if !now.Before(approval.ExpiresAt) {
		approval.Status = domain.ApprovalExpired
		if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decision_at=?,actor=? WHERE id=? AND status='pending'`, approval.Status, stamp(now), actor, id); err != nil {
			return domain.Approval{}, err
		}
		if _, err := appendEvent(ctx, tx, approval.TaskID, "approval.expired", nil, "user", map[string]any{"approval_id": id}); err != nil {
			return domain.Approval{}, err
		}
		task, err := getTask(ctx, tx, approval.TaskID)
		if err != nil {
			return domain.Approval{}, err
		}
		activeApproval, err := hasActivePendingApprovalTx(ctx, tx, approval.TaskID, now)
		if err != nil {
			return domain.Approval{}, err
		}
		if task.Status == domain.StatusWaitingApproval && !activeApproval {
			if err := transitionStatusTx(ctx, tx, task, domain.StatusFailed, nil, "task.failed", map[string]any{"error_code": "approval_expired"}, now); err != nil {
				return domain.Approval{}, err
			}
		}
		if err := saveApprovalCheckpoint(ctx, tx, approval.TaskID, nil, id, string(domain.ApprovalExpired)); err != nil {
			return domain.Approval{}, err
		}
		if idempotencyKey != "" {
			if err := insertApprovalIdempotency(ctx, tx, idempotencyKey, operation, id); err != nil {
				return domain.Approval{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.Approval{}, err
		}
		return approval, ErrApprovalExpired
	}
	approval.Status = approvalStatus
	if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decision_at=?,actor=? WHERE id=? AND status='pending'`, approvalStatus, stamp(now), actor, id); err != nil {
		return domain.Approval{}, err
	}
	task, err := getTask(ctx, tx, approval.TaskID)
	if err != nil {
		return domain.Approval{}, err
	}
	if task.Status != taskStatus {
		if !domain.CanTransition(task.Status, taskStatus) {
			return domain.Approval{}, fmt.Errorf("%w: task status %s cannot accept approval decision", ErrConflict, task.Status)
		}
		if err := transitionStatusTx(ctx, tx, task, taskStatus, nil, "task.status_changed", map[string]any{"approval_id": id}, now); err != nil {
			return domain.Approval{}, err
		}
	}
	decisionEvent := "approval.rejected"
	if approve {
		decisionEvent = "approval.approved"
	}
	if _, err := appendEvent(ctx, tx, approval.TaskID, decisionEvent, nil, "user", map[string]any{"approval_id": id, "actor": actor}); err != nil {
		return domain.Approval{}, err
	}
	if err := saveApprovalCheckpoint(ctx, tx, approval.TaskID, nil, id, string(approvalStatus)); err != nil {
		return domain.Approval{}, err
	}
	if idempotencyKey != "" {
		if err := insertApprovalIdempotency(ctx, tx, idempotencyKey, operation, id); err != nil {
			return domain.Approval{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Approval{}, err
	}
	return approval, nil
}

func (s *Store) ConsumeApproval(ctx context.Context, taskID, toolName string, arguments json.RawMessage) (string, bool, error) {
	_, hash, err := normalizeApprovalArguments(arguments)
	if err != nil {
		return "", false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var id string
	var status domain.ApprovalStatus
	var expires string
	var consumed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,status,expires_at,consumed_at FROM approvals WHERE task_id=? AND tool_name=? AND arguments_sha256=? ORDER BY created_at DESC LIMIT 1`, taskID, toolName, hash).Scan(&id, &status, &expires, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if status == domain.ApprovalPending {
		return "", false, nil
	}
	if status == domain.ApprovalExpired {
		return "", false, ErrApprovalExpired
	}
	if status != domain.ApprovalApproved {
		return "", false, ErrConflict
	}
	if consumed.Valid {
		return "", false, ErrApprovalAlreadyConsumed
	}
	now := time.Now().UTC()
	if !now.Before(parseTime(expires)) {
		if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decision_at=?,actor='policy' WHERE id=? AND status='approved' AND consumed_at IS NULL`, domain.ApprovalExpired, stamp(now), id); err != nil {
			return "", false, err
		}
		if _, err := appendEvent(ctx, tx, taskID, "approval.expired", nil, "user", map[string]any{"approval_id": id}); err != nil {
			return "", false, err
		}
		if err := tx.Commit(); err != nil {
			return "", false, err
		}
		return "", false, ErrApprovalExpired
	}
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET consumed_at=? WHERE id=? AND status='approved' AND consumed_at IS NULL`, stamp(now), id)
	if err != nil {
		return "", false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if count != 1 {
		return "", false, ErrApprovalAlreadyConsumed
	}
	if _, err := appendEvent(ctx, tx, taskID, "approval.consumed", nil, "audit", map[string]any{"approval_id": id, "tool": toolName}); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (s *Store) moveTaskForApproval(ctx context.Context, tx *sql.Tx, task domain.Task, now time.Time) error {
	if task.Status == domain.StatusWaitingApproval {
		return nil
	}
	if !domain.CanTransition(task.Status, domain.StatusWaitingApproval) {
		return fmt.Errorf("%w: task status %s cannot wait for approval", ErrConflict, task.Status)
	}
	return transitionStatusTx(ctx, tx, task, domain.StatusWaitingApproval, nil, "task.status_changed", nil, now)
}

func hasActivePendingApprovalTx(ctx context.Context, tx *sql.Tx, taskID string, now time.Time) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE task_id=? AND status='pending' AND expires_at>?`, taskID, stamp(now)).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func expireMatchingApprovalsTx(ctx context.Context, tx *sql.Tx, taskID, toolName, argumentsHash string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM approvals WHERE task_id=? AND tool_name=? AND arguments_sha256=? AND status='pending' AND consumed_at IS NULL AND expires_at<=?`, taskID, toolName, argumentsHash, stamp(now))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decision_at=?,actor='policy' WHERE id=? AND status='pending'`, domain.ApprovalExpired, stamp(now), id); err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, taskID, "approval.expired", nil, "user", map[string]any{"approval_id": id}); err != nil {
			return err
		}
	}
	return nil
}

func transitionStatusTx(ctx context.Context, tx *sql.Tx, task domain.Task, to domain.TaskStatus, stage *domain.Stage, eventType string, payload map[string]any, now time.Time) error {
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["from"] = task.Status
	payload["to"] = to
	if err := transitionStageTx(ctx, tx, task.ID, task.Stage, stage, closeStageStatus(to), now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,stage=?,version=version+1,updated_at=? WHERE id=?`, to, nullableStage(stage), stamp(now), task.ID); err != nil {
		return err
	}
	_, err := appendEvent(ctx, tx, task.ID, eventType, stage, "user", payload)
	return err
}

func saveApprovalCheckpoint(ctx context.Context, tx *sql.Tx, taskID string, state any, approvalID, status string) error {
	planVersion := 1
	if state == nil {
		var stateJSON string
		var latestVersion int
		err := tx.QueryRowContext(ctx, `SELECT state_json,plan_version FROM checkpoints WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, taskID).Scan(&stateJSON, &latestVersion)
		if err == nil {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(stateJSON), &decoded); err != nil {
				return err
			}
			decoded["approval_id"] = approvalID
			decoded["approval_status"] = status
			state = decoded
			planVersion = latestVersion
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		} else {
			state = map[string]any{"approval_id": approvalID, "approval_status": status}
		}
	}
	seq, err := appendEvent(ctx, tx, taskID, "checkpoint.saved", nil, "internal", map[string]any{"approval_id": approvalID, "status": status})
	if err != nil {
		return err
	}
	return insertCheckpoint(ctx, tx, taskID, planVersion, seq, state)
}

func insertApprovalIdempotency(ctx context.Context, tx *sql.Tx, key, operation, approvalID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(key,operation,resource_id,created_at) VALUES(?,?,?,?)`, key, operation, approvalID, stamp(time.Now().UTC()))
	return err
}

func normalizeApprovalArguments(raw json.RawMessage) (json.RawMessage, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, "", errors.New("approval arguments must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("approval arguments must contain one JSON object")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(hash[:]), nil
}

func queryApprovals(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]domain.Approval, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var approvals []domain.Approval
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

func getApproval(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.Approval, error) {
	return scanApproval(q.QueryRowContext(ctx, `SELECT id,task_id,tool_name,arguments_json,arguments_sha256,target,risk_explanation,expires_at,status,created_at,decision_at,actor,consumed_at FROM approvals WHERE id=?`, id))
}

func scanApproval(row scanner) (domain.Approval, error) {
	var approval domain.Approval
	var arguments, expires, created sql.NullString
	var decisionAt, consumedAt sql.NullString
	if err := row.Scan(&approval.ID, &approval.TaskID, &approval.ToolName, &arguments, &approval.ArgumentsSHA256, &approval.Target, &approval.RiskExplanation, &expires, &approval.Status, &created, &decisionAt, &approval.Actor, &consumedAt); err != nil {
		return domain.Approval{}, err
	}
	approval.Arguments = json.RawMessage(arguments.String)
	approval.ExpiresAt = parseTime(expires.String)
	approval.CreatedAt = parseTime(created.String)
	if decisionAt.Valid {
		value := parseTime(decisionAt.String)
		approval.DecisionAt = &value
	}
	if consumedAt.Valid {
		value := parseTime(consumedAt.String)
		approval.ConsumedAt = &value
	}
	return approval, nil
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
	messageID := newID()
	var latestDeltaPayload string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE task_id=? AND type='message.delta' ORDER BY seq DESC LIMIT 1`, taskID).Scan(&latestDeltaPayload)
	if err == nil {
		var latestDelta struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal([]byte(latestDeltaPayload), &latestDelta); err != nil || latestDelta.MessageID == "" {
			return domain.Task{}, errors.New("latest message delta has no valid message ID")
		}
		messageID = latestDelta.MessageID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, err
	}
	now := time.Now().UTC()
	stage := domain.StageFinalization
	if err := transitionStageTx(ctx, tx, taskID, task.Stage, &stage, "completed", now); err != nil {
		return domain.Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id,task_id,role,content,created_at) VALUES(?,?,?,?,?)`, messageID, taskID, "assistant", answer, stamp(now)); err != nil {
		return domain.Task{}, err
	}
	if _, err := appendEvent(ctx, tx, taskID, "message.created", &stage, "user", map[string]any{"message_id": messageID, "role": "assistant"}); err != nil {
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
	case domain.StatusWaitingApproval:
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

func safeArtifactReference(value string) bool {
	parts := strings.Split(filepath.ToSlash(value), "/")
	if len(parts) != 2 || filepath.IsAbs(value) {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, character := range part {
			if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' {
				return false
			}
		}
	}
	return true
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
