package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type conversationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var ErrWebDisabled = errors.New("web research is disabled for this conversation")

func conversation(ctx context.Context, q conversationQuery, id string) (domain.Conversation, error) {
	var c domain.Conversation
	var created, updated, profile, settings string
	var archived sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,title,mode,created_at,updated_at,archived_at,model_profile_json,research_settings_json FROM conversations WHERE id=? AND deleted_at IS NULL`, id).Scan(&c.ID, &c.Title, &c.Mode, &created, &updated, &archived, &profile, &settings)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt, c.UpdatedAt = parseTime(created), parseTime(updated)
	c.ArchivedAt = optionalTime(archived)
	if err = json.Unmarshal([]byte(profile), &c.ModelProfile); err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(settings), &c.ResearchSettings); err != nil {
		return c, err
	}
	err = q.QueryRowContext(ctx, `SELECT id FROM tasks WHERE conversation_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, id).Scan(&c.LatestRunID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	err = q.QueryRowContext(ctx, `SELECT id FROM tasks WHERE conversation_id=? AND status NOT IN ('completed','failed','cancelled') LIMIT 1`, id).Scan(&c.ActiveRunID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	return c, nil
}

func optionalTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	t := parseTime(value.String)
	return &t
}

func (s *Store) CreateConversation(ctx context.Context, c domain.Conversation) (domain.Conversation, error) {
	return s.CreateConversationIdempotent(ctx, c, "")
}

func (s *Store) CreateConversationIdempotent(ctx context.Context, c domain.Conversation, key string) (domain.Conversation, error) {
	if len(key) > 200 {
		return c, domain.ErrInvalidConversation
	}
	encoded, err := json.Marshal(struct {
		Title    string
		Mode     string
		Profile  map[string]any
		Settings map[string]any
	}{c.Title, c.Mode, c.ModelProfile, c.ResearchSettings})
	if err != nil {
		return c, err
	}
	hash := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(hash[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if key != "" {
		var id, operation, prior string
		err = tx.QueryRowContext(ctx, `SELECT resource_id,operation,COALESCE(request_sha256,'') FROM idempotency_keys WHERE key=?`, key).Scan(&id, &operation, &prior)
		if err == nil {
			if operation != "conversations.create" || prior != fingerprint {
				return c, ErrIdempotency
			}
			return conversation(ctx, tx, id)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return c, err
		}
	}
	c.ID = newID()
	now := stamp(time.Now().UTC())
	p, _ := json.Marshal(c.ModelProfile)
	settings, _ := json.Marshal(c.ResearchSettings)
	_, err = tx.ExecContext(ctx, `INSERT INTO conversations(id,title,mode,created_at,updated_at,model_profile_json,research_settings_json) VALUES(?,?,?,?,?,?,?)`, c.ID, c.Title, c.Mode, now, now, string(p), string(settings))
	if err != nil {
		return c, err
	}
	if key != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO idempotency_keys(key,operation,resource_id,created_at,request_sha256) VALUES(?,?,?,?,?)`, key, "conversations.create", c.ID, now, fingerprint); err != nil {
			return c, err
		}
	}
	result, err := conversation(ctx, tx, c.ID)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) GetConversation(ctx context.Context, id string) (domain.Conversation, error) {
	return conversation(ctx, s.db, id)
}

func (s *Store) ListConversations(ctx context.Context, query string, archived bool) ([]domain.Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM conversations WHERE deleted_at IS NULL AND (archived_at IS NOT NULL)=? AND instr(lower(title),lower(?))>0 ORDER BY updated_at DESC,id DESC LIMIT 200`, archived, query)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []domain.Conversation{}
	for _, id := range ids {
		c, e := s.GetConversation(ctx, id)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		result = append(result, c)
	}
	return result, nil
}

func (s *Store) UpdateConversation(ctx context.Context, id string, patch map[string]any) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	c, err := conversation(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if c.ActiveRunID != "" {
		for _, field := range []string{"mode", "model_profile", "research_settings"} {
			if _, present := patch[field]; present {
				return c, fmt.Errorf("%w: execution settings cannot change while a run is active", ErrConflict)
			}
		}
	}
	if v, ok := patch["title"].(string); ok {
		c.Title = v
	}
	if v, ok := patch["mode"].(string); ok {
		c.Mode = v
	}
	if v, ok := patch["archived"].(bool); ok {
		c.ArchivedAt = nil
		if v {
			now := time.Now().UTC()
			c.ArchivedAt = &now
		}
	}
	if v, ok := patch["model_profile"].(map[string]any); ok {
		c.ModelProfile = v
	}
	if v, ok := patch["research_settings"].(map[string]any); ok {
		c.ResearchSettings = v
	}
	p, _ := json.Marshal(c.ModelProfile)
	settings, _ := json.Marshal(c.ResearchSettings)
	var archived any
	if c.ArchivedAt != nil {
		archived = stamp(*c.ArchivedAt)
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversations SET title=?,mode=?,archived_at=?,model_profile_json=?,research_settings_json=?,updated_at=? WHERE id=?`, c.Title, c.Mode, archived, string(p), string(settings), stamp(time.Now().UTC()), id)
	if err != nil {
		return c, err
	}
	if err = tx.Commit(); err != nil {
		return c, err
	}
	return s.GetConversation(ctx, id)
}

func run(ctx context.Context, q conversationQuery, id string) (domain.Run, error) {
	var r domain.Run
	var stage, started, completed, heartbeat, deadline sql.NullString
	var created, updated string
	err := q.QueryRowContext(ctx, `SELECT id,conversation_id,user_message_id,assistant_message_id,mode,operation,continue_from_message_id,status,stage,error_code,error_message,retry_count,cancel_requested,created_at,updated_at,started_at,completed_at,heartbeat_at,deadline_at FROM tasks WHERE id=? AND conversation_id IS NOT NULL`, id).Scan(&r.ID, &r.ConversationID, &r.UserMessageID, &r.AssistantMessageID, &r.Mode, &r.Operation, &r.ContinueFromMessageID, &r.Status, &stage, &r.ErrorCode, &r.ErrorMessage, &r.RetryCount, &r.CancelRequested, &created, &updated, &started, &completed, &heartbeat, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if stage.Valid {
		v := domain.Stage(stage.String)
		r.Stage = &v
	}
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	r.StartedAt = optionalTime(started)
	r.CompletedAt = optionalTime(completed)
	r.HeartbeatAt = optionalTime(heartbeat)
	r.DeadlineAt = optionalTime(deadline)
	return r, nil
}

func (s *Store) GetRun(ctx context.Context, id string) (domain.Run, error) { return run(ctx, s.db, id) }

func (s *Store) TouchRun(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.renewLeaseTx(ctx, tx, id); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET heartbeat_at=? WHERE id=? AND conversation_id IS NOT NULL AND status IN ('planning','running','verifying')`, stamp(now), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE research_sessions SET heartbeat_at=?,updated_at=? WHERE task_id=?`, stamp(now), stamp(now), id); err != nil {
		return err
	}
	return tx.Commit()
}

func conversationMessage(ctx context.Context, q conversationQuery, id string) (domain.ConversationMessage, error) {
	var m domain.ConversationMessage
	var created, updated, metadata string
	var parent sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,conversation_id,run_id,role,content,status,parent_message_id,created_at,updated_at,last_event_seq,metadata_json FROM conversation_messages WHERE id=?`, id).Scan(&m.ID, &m.ConversationID, &m.RunID, &m.Role, &m.Content, &m.Status, &parent, &created, &updated, &m.LastEventSeq, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	m.ParentMessageID = parent.String
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	err = json.Unmarshal([]byte(metadata), &m.Metadata)
	if err == nil {
		if verification, ok := m.Metadata["verification"].(map[string]any); ok {
			if refs, ok := verification["citations"].([]any); ok {
				for _, ref := range refs {
					encoded, e := json.Marshal(ref)
					if e != nil {
						return m, e
					}
					m.Citations = append(m.Citations, encoded)
				}
			}
		}
	}
	return m, err
}

func (s *Store) ConversationMessages(ctx context.Context, id string) ([]domain.ConversationMessage, error) {
	if _, err := s.GetConversation(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM conversation_messages WHERE conversation_id=? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []domain.ConversationMessage{}
	for _, id := range ids {
		m, e := conversationMessage(ctx, s.db, id)
		if e != nil {
			return nil, e
		}
		result = append(result, m)
	}
	return result, nil
}

func submitResult(ctx context.Context, q conversationQuery, id string) (domain.SubmitResponse, error) {
	r, err := run(ctx, q, id)
	if err != nil {
		return domain.SubmitResponse{}, err
	}
	u, err := conversationMessage(ctx, q, r.UserMessageID)
	if err != nil {
		return domain.SubmitResponse{}, err
	}
	a, err := conversationMessage(ctx, q, r.AssistantMessageID)
	return domain.SubmitResponse{ConversationID: r.ConversationID, UserMessage: u, AssistantMessage: a, Run: r}, err
}

func (s *Store) SubmitTurn(ctx context.Context, in domain.SubmitTurn, key string) (domain.SubmitResponse, error) {
	encoded, _ := json.Marshal(in)
	hash := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(hash[:])
	op := "conversations.submit"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SubmitResponse{}, err
	}
	defer tx.Rollback()
	c, err := conversation(ctx, tx, in.ConversationID)
	if err != nil {
		return domain.SubmitResponse{}, err
	}
	if key != "" {
		var id, operation, prior string
		err = tx.QueryRowContext(ctx, `SELECT resource_id,operation,COALESCE(request_sha256,'') FROM idempotency_keys WHERE key=?`, key).Scan(&id, &operation, &prior)
		if err == nil {
			if operation != op || prior != fingerprint {
				return domain.SubmitResponse{}, ErrIdempotency
			}
			return submitResult(ctx, tx, id)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.SubmitResponse{}, err
		}
	}
	if c.ActiveRunID != "" || c.ArchivedAt != nil {
		return domain.SubmitResponse{}, fmt.Errorf("%w: conversation is busy or archived", ErrConflict)
	}
	mode := in.Mode
	if mode == "" {
		mode = c.Mode
	}
	operation := in.Operation
	if operation == "" {
		operation = "send"
	}
	parent := in.ParentMessageID
	var sourceResearch *domain.ResearchSession
	var chatSnapshot *domain.ModelProfileSnapshot
	var head sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT head_message_id FROM conversations WHERE id=?`, c.ID).Scan(&head); err != nil {
		return domain.SubmitResponse{}, err
	}
	if parent == "" {
		parent = head.String
	}
	if parent != "" {
		m, e := conversationMessage(ctx, tx, parent)
		if e != nil || m.ConversationID != c.ID {
			return domain.SubmitResponse{}, ErrConflict
		}
	}
	userID := newID()
	assistantID := newID()
	id := newID()
	content := in.Content
	retries := 0
	continued := ""
	if operation != "send" {
		prior, e := run(ctx, tx, in.SourceRunID)
		if e != nil {
			return domain.SubmitResponse{}, e
		}
		if prior.ConversationID != c.ID || c.LatestRunID != prior.ID || !prior.Status.Terminal() {
			return domain.SubmitResponse{}, ErrConflict
		}
		userID = prior.UserMessageID
		user, e := conversationMessage(ctx, tx, userID)
		if e != nil {
			return domain.SubmitResponse{}, e
		}
		content = user.Content
		mode = prior.Mode
		if mode == "research" {
			source, e := researchSession(ctx, tx, prior.ID)
			if e == nil {
				sourceResearch = &source
			} else if !errors.Is(e, ErrNotFound) {
				return domain.SubmitResponse{}, e
			}
		} else if mode == "chat" {
			model, _, e := chatRunModelSnapshot(ctx, tx, prior.ID)
			if e == nil {
				chatSnapshot = &model
			} else if !errors.Is(e, ErrNotFound) {
				return domain.SubmitResponse{}, e
			}
		}
		parent = userID
		if operation == "retry" {
			retries = prior.RetryCount + 1
		} else if operation == "continue" {
			old, e := conversationMessage(ctx, tx, prior.AssistantMessageID)
			if e != nil {
				return domain.SubmitResponse{}, e
			}
			if strings.TrimSpace(old.Content) == "" {
				return domain.SubmitResponse{}, ErrConflict
			}
			parent = old.ID
			continued = old.ID
		} else {
			return domain.SubmitResponse{}, ErrConflict
		}
	}
	if enabled, specified := c.ResearchSettings["web_enabled"].(bool); mode == "research" && specified && !enabled {
		return domain.SubmitResponse{}, ErrWebDisabled
	}
	if mode == "chat" && chatSnapshot == nil && in.ModelSnapshot != nil {
		model := *in.ModelSnapshot
		if model.StopSequences != nil {
			model.StopSequences = append([]string{}, model.StopSequences...)
		}
		chatSnapshot = &model
	}
	if chatSnapshot != nil {
		if err := validateModelProfileSnapshot(*chatSnapshot); err != nil {
			return domain.SubmitResponse{}, err
		}
	}
	now := time.Now().UTC()
	deadlineSeconds := int64(in.DeadlineSeconds)
	var researchConfig *researchSessionConfig
	if mode == "research" {
		config := researchConfigFromTurn(in)
		if sourceResearch != nil {
			config = researchConfigFromSession(*sourceResearch)
		}
		if strings.TrimSpace(config.goal) == "" {
			config.goal = content
		}
		deadlineSeconds = researchDeadlineSeconds(in.DeadlineSeconds, config.budget.WallTimeSeconds)
		researchConfig = &config
	} else if deadlineSeconds <= 0 {
		deadlineSeconds = 7200
	}
	deadline := now.Add(time.Duration(deadlineSeconds) * time.Second)
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,title,description,status,priority,version,created_at,updated_at,conversation_id,mode,operation,continue_from_message_id,retry_count,deadline_at) VALUES(?,?,?,'queued',0,1,?,?,?,?,?,?,?,?)`, id, c.Title, content, stamp(now), stamp(now), c.ID, mode, operation, continued, retries, stamp(deadline))
	if err != nil {
		return domain.SubmitResponse{}, err
	}
	var position int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0) FROM conversation_messages WHERE conversation_id=?`, c.ID).Scan(&position); err != nil {
		return domain.SubmitResponse{}, err
	}
	if operation == "send" {
		position++
		_, err = tx.ExecContext(ctx, `INSERT INTO conversation_messages(id,conversation_id,run_id,role,content,status,parent_message_id,position,created_at,updated_at) VALUES(?,?,?,'user',?,'completed',?,?,?,?)`, userID, c.ID, id, content, nullableKey(parent), position, stamp(now), stamp(now))
		if err != nil {
			return domain.SubmitResponse{}, err
		}
		parent = userID
	}
	position++
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_messages(id,conversation_id,run_id,role,content,status,parent_message_id,position,created_at,updated_at) VALUES(?,?,?,'assistant','','pending',?,?,?,?)`, assistantID, c.ID, id, parent, position, stamp(now), stamp(now))
	if err != nil {
		return domain.SubmitResponse{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET user_message_id=?,assistant_message_id=? WHERE id=?`, userID, assistantID, id); err != nil {
		return domain.SubmitResponse{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE conversations SET head_message_id=?,updated_at=? WHERE id=?`, assistantID, stamp(now), c.ID); err != nil {
		return domain.SubmitResponse{}, err
	}
	createdPayload := map[string]any{"status": "queued", "mode": mode}
	if chatSnapshot != nil {
		createdPayload["model_snapshot_schema_version"] = 1
	}
	if _, err = appendEvent(ctx, tx, id, "task.created", nil, "user", createdPayload); err != nil {
		return domain.SubmitResponse{}, err
	}
	seq, err := appendEvent(ctx, tx, id, "message.created", nil, "user", map[string]any{"message_id": assistantID, "role": "assistant", "status": "pending"})
	if err != nil {
		return domain.SubmitResponse{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE conversation_messages SET last_event_seq=? WHERE id=?`, seq, assistantID); err != nil {
		return domain.SubmitResponse{}, err
	}
	if key != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO idempotency_keys(key,operation,resource_id,created_at,request_sha256) VALUES(?,?,?,?,?)`, key, op, id, stamp(now), fingerprint); err != nil {
			return domain.SubmitResponse{}, err
		}
	}
	if researchConfig != nil {
		session := domain.ResearchSession{
			ID:             id,
			TaskID:         id,
			ConversationID: c.ID,
			Goal:           researchConfig.goal,
			Status:         string(domain.StatusQueued),
			Profile:        researchConfig.profile,
			Policy:         researchConfig.policy,
			Budget:         researchConfig.budget,
			ModelSnapshot:  researchConfig.modelSnapshot,
			CurrentPhase:   string(domain.StatusQueued),
			DeadlineAt:     deadline,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err = insertResearchSession(ctx, tx, session); err != nil {
			return domain.SubmitResponse{}, err
		}
	}
	if chatSnapshot != nil {
		if err := insertInitialChatModelSnapshot(ctx, tx, id, seq, now, *chatSnapshot); err != nil {
			return domain.SubmitResponse{}, err
		}
		if err := sealInitialCheckpoint(ctx, tx, id); err != nil {
			return domain.SubmitResponse{}, err
		}
	}
	result, err := submitResult(ctx, tx, id)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func syncConversationTerminal(ctx context.Context, tx *sql.Tx, id string, status domain.TaskStatus, payload map[string]any) error {
	if status == domain.StatusFailed || status == domain.StatusCancelled {
		if _, err := tx.ExecContext(ctx, `UPDATE harness_bundles SET status='rejected',active=0,updated_at=? WHERE task_id=? AND status IN ('candidate','evaluating','ready')`, stamp(time.Now().UTC()), id); err != nil {
			return err
		}
	}
	r, err := run(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	code, _ := payload["error_code"].(string)
	message, _ := payload["message"].(string)
	now := stamp(time.Now().UTC())
	metadata, _ := json.Marshal(map[string]any{"error_code": code, "error_message": message})
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET completed_at=?,cancel_requested=?,error_code=?,error_message=? WHERE id=?`, now, status == domain.StatusCancelled, code, message, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversation_messages SET status=?,metadata_json=CASE WHEN ?='completed' THEN metadata_json ELSE ? END,updated_at=?,last_event_seq=(SELECT COALESCE(MAX(seq),0) FROM events WHERE task_id=?) WHERE id=?`, status, status, string(metadata), now, id, r.AssistantMessageID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversations SET updated_at=? WHERE id=?`, now, r.ConversationID)
	if err != nil {
		return err
	}
	return updateResearchSessionStatus(ctx, tx, id, status, string(status), true, researchTerminalStopReason(status, payload), time.Now().UTC())
}

func enforceClaimPermission(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	r, err := run(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if r.Mode != "research" {
		return true, nil
	}
	c, err := conversation(ctx, tx, r.ConversationID)
	if err != nil {
		return false, err
	}
	enabled, specified := c.ResearchSettings["web_enabled"].(bool)
	if !specified || enabled {
		return true, nil
	}
	now := time.Now().UTC()
	if err = transitionStageTx(ctx, tx, id, r.Stage, nil, "failed", now); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='failed',stage=NULL,version=version+1,updated_at=? WHERE id=?`, stamp(now), id); err != nil {
		return false, err
	}
	payload := map[string]any{"error_code": "web_disabled", "message": "Веб-поиск отключён для этого разговора. Выберите Chat или измените настройки."}
	if _, err = appendEvent(ctx, tx, id, "task.failed", nil, "user", payload); err != nil {
		return false, err
	}
	if err = syncConversationTerminal(ctx, tx, id, domain.StatusFailed, payload); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) DeleteConversation(ctx context.Context, id string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := conversation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	if c.ActiveRunID != "" {
		id := c.ActiveRunID
		if err := clearLease(ctx, tx, id); err != nil {
			return nil, err
		}
		t, e := getTask(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		now := time.Now().UTC()
		if e = transitionStageTx(ctx, tx, id, t.Stage, nil, "cancelled", now); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE tasks SET status='cancelled',stage=NULL,updated_at=?,version=version+1 WHERE id=?`, stamp(now), id); e != nil {
			return nil, e
		}
		if _, e = appendEvent(ctx, tx, id, "task.cancelled", nil, "user", map[string]any{"to": "cancelled"}); e != nil {
			return nil, e
		}
		if e = syncConversationTerminal(ctx, tx, id, domain.StatusCancelled, nil); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversations SET deleted_at=?,updated_at=? WHERE id=?`, stamp(time.Now().UTC()), stamp(time.Now().UTC()), id)
	if err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}
