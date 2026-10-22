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
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var ErrInvocationInvalid = errors.New("invocation record invalid")

var invocationName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

const invocationOutputLimit = 128 << 10

func invocationDigest(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func invocationIdentity(taskID, key string) string {
	raw, _ := json.Marshal([]string{taskID, key})
	return invocationDigest(raw)
}

func validateInvocation(in domain.Invocation) error {
	for _, field := range []struct {
		value    string
		limit    int
		required bool
	}{
		{in.ID, 128, true}, {in.TaskID, 128, true}, {in.StepID, 128, false}, {in.Role, 64, true}, {in.Kind, 128, true}, {in.OperationID, 512, true}, {in.IdempotencyKey, 512, true}, {in.ErrorCode, 128, false},
	} {
		if len(field.value) > field.limit || strings.TrimSpace(field.value) != field.value || field.required && field.value == "" || strings.ContainsAny(field.value, "\x00\r\n") {
			return ErrInvocationInvalid
		}
	}
	if !invocationName.MatchString(in.Role) || in.StepID != "" && !invocationName.MatchString(in.StepID) || in.ErrorCode != "" && !invocationName.MatchString(in.ErrorCode) || in.Attempt < 1 || in.Attempt > 4 {
		return ErrInvocationInvalid
	}
	if in.Kind != "llm.complete" && in.Kind != "llm.stream" {
		if !strings.HasPrefix(in.Kind, "tool.") || !invocationName.MatchString(strings.TrimPrefix(in.Kind, "tool.")) {
			return ErrInvocationInvalid
		}
	}
	if raw, err := hex.DecodeString(in.InputHash); err != nil || len(raw) != sha256.Size {
		return ErrInvocationInvalid
	}
	if in.OutputHash != "" {
		if raw, err := hex.DecodeString(in.OutputHash); err != nil || len(raw) != sha256.Size {
			return ErrInvocationInvalid
		}
	}
	if len(in.Output) > invocationOutputLimit {
		return ErrInvocationInvalid
	}
	return nil
}

func sameInvocationIdentity(a, b domain.Invocation) bool {
	return a.ID == b.ID && a.TaskID == b.TaskID && a.StepID == b.StepID && a.Role == b.Role && a.Kind == b.Kind && a.OperationID == b.OperationID && a.InputHash == b.InputHash && a.IdempotencyKey == b.IdempotencyKey && a.Attempt == b.Attempt
}

func invocationAllowed(ctx context.Context, tx *sql.Tx, taskID string, checkDeadline bool) error {
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	var status domain.TaskStatus
	var cancelled bool
	var deadline sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status,cancel_requested,deadline_at FROM tasks WHERE id=?`, taskID).Scan(&status, &cancelled, &deadline); err != nil {
		return err
	}
	if cancelled || status != domain.StatusPlanning && status != domain.StatusRunning && status != domain.StatusVerifying {
		return ErrConflict
	}
	if !checkDeadline {
		return ctx.Err()
	}
	if deadline.Valid && !time.Now().Before(parseTime(deadline.String)) {
		return context.DeadlineExceeded
	}
	session, err := researchSession(ctx, tx, taskID)
	if errors.Is(err, ErrNotFound) {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	return researchReservationAllowed(ctx, tx, session)
}

func invocationCheckpointState(ctx context.Context, tx *sql.Tx, taskID string) (map[string]any, int, error) {
	items, err := verifiedCheckpoints(ctx, tx, taskID)
	if err != nil {
		return nil, 0, err
	}
	state := make(map[string]any)
	version := 1
	if len(items) > 0 {
		latest := items[len(items)-1]
		version = latest.PlanVersion
		if latest.State != nil {
			var raw string
			if err := tx.QueryRowContext(ctx, `SELECT state_json FROM checkpoints WHERE task_id=? AND id=?`, taskID, latest.ID).Scan(&raw); err != nil {
				return nil, 0, err
			}
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&state); err != nil || state == nil {
				return nil, 0, fmt.Errorf("%w: invocation checkpoint must be an object", ErrCheckpointInvalid)
			}
		}
	}
	return state, version, nil
}

func (s *Store) BeginInvocation(ctx context.Context, in domain.Invocation) (domain.Invocation, error) {
	identity := invocationIdentity(in.TaskID, in.IdempotencyKey)
	if in.ID == "" {
		in.ID = "invocation-" + identity
	}
	if err := validateInvocation(in); err != nil {
		return domain.Invocation{}, err
	}
	if in.Status != "" && in.Status != "started" || in.FinishedAt != nil || in.ErrorCode != "" || in.OutputHash != "" || len(in.Output) != 0 {
		return domain.Invocation{}, ErrInvocationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Invocation{}, err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, in.TaskID, true); err != nil {
		return domain.Invocation{}, err
	}
	state, version, err := invocationCheckpointState(ctx, tx, in.TaskID)
	if err != nil {
		return domain.Invocation{}, err
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE id=? AND task_id=? AND type='invocation.started'`, "invocation-started-"+identity, in.TaskID).Scan(&raw)
	if err == nil {
		prior, err := decodeInvocationEvent("invocation.started", raw)
		if err != nil {
			return domain.Invocation{}, err
		}
		if !sameInvocationIdentity(in, prior) {
			return domain.Invocation{}, ErrConflict
		}
		items, err := invocationsForOperationTx(ctx, tx, in.TaskID, in.OperationID)
		if err != nil {
			return domain.Invocation{}, err
		}
		for _, item := range items {
			if item.ID == prior.ID {
				return item, nil
			}
		}
		return domain.Invocation{}, ErrInvocationInvalid
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Invocation{}, err
	}
	items, err := invocationsForOperationTx(ctx, tx, in.TaskID, in.OperationID)
	if err != nil {
		return domain.Invocation{}, err
	}
	if len(items) >= 4 {
		return domain.Invocation{}, ErrConflict
	}
	for _, item := range items {
		if item.Attempt == in.Attempt || item.InputHash != in.InputHash || item.Kind != in.Kind || item.Role != in.Role || item.StepID != in.StepID {
			return domain.Invocation{}, ErrConflict
		}
	}
	var reused bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE task_id=? AND type='invocation.started' AND CASE WHEN json_valid(payload_json) THEN json_extract(payload_json,'$.invocation.id') END=?)`, in.TaskID, in.ID).Scan(&reused); err != nil {
		return domain.Invocation{}, err
	}
	if reused {
		return domain.Invocation{}, ErrConflict
	}
	if in.Role == "verifier" || in.Role == "critic_0" || in.Role == "critic_1" {
		var reviews int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=? AND type='invocation.started' AND json_valid(payload_json) AND json_extract(payload_json,'$.invocation.role') IN ('verifier','critic_0','critic_1')`, in.TaskID).Scan(&reviews); err != nil {
			return domain.Invocation{}, err
		}
		if reviews >= 3 {
			return domain.Invocation{}, ErrResearchBudgetExceeded
		}
	}
	reservationKind := ""
	if strings.HasPrefix(in.Role, "refinement_") {
		var calls int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=? AND type='invocation.started' AND json_valid(payload_json) AND json_extract(payload_json,'$.invocation.role') LIKE 'refinement_%'`, in.TaskID).Scan(&calls); err != nil {
			return domain.Invocation{}, err
		}
		if calls >= 10 {
			return domain.Invocation{}, ErrResearchBudgetExceeded
		}
	}
	switch in.Kind {
	case "llm.complete", "llm.stream":
		reservationKind = "llm"
	case "tool.search":
		reservationKind = "search"
	case "tool.fetch_url":
		reservationKind = "fetch"
	}
	if reservationKind != "" {
		if _, err := researchSession(ctx, tx, in.TaskID); err == nil {
			if _, err := reserveResearchOperationTx(ctx, tx, in.TaskID, reservationKind, "invocation:"+in.ID); err != nil {
				return domain.Invocation{}, err
			}
		} else if !errors.Is(err, ErrNotFound) {
			return domain.Invocation{}, err
		}
	}
	now := time.Now().UTC()
	in.StartedAt = &now
	in.Status = "started"
	seq, err := appendEvent(ctx, tx, in.TaskID, "invocation.started", nil, "internal", map[string]any{"invocation": in})
	if err != nil {
		return domain.Invocation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE events SET id=? WHERE task_id=? AND seq=?`, "invocation-started-"+identity, in.TaskID, seq); err != nil {
		return domain.Invocation{}, err
	}
	state["pending_invocation"] = in
	state["pending_invocation_id"] = in.ID
	if err := insertCheckpoint(ctx, tx, in.TaskID, version, seq, state); err != nil {
		return domain.Invocation{}, err
	}
	if err := invocationAllowed(ctx, tx, in.TaskID, true); err != nil {
		return domain.Invocation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invocation{}, err
	}
	return in, nil
}

func (s *Store) FinishInvocation(ctx context.Context, in domain.Invocation, result any) error {
	if err := validateInvocation(in); err != nil {
		return err
	}
	if in.Status != "completed" && in.Status != "failed" && in.Status != "unknown" {
		return ErrInvocationInvalid
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(encoded) > invocationOutputLimit {
		return ErrInvocationInvalid
	}
	hash := invocationDigest(encoded)
	if in.OutputHash != "" && in.OutputHash != hash {
		return ErrConflict
	}
	if len(in.Output) != 0 && !bytes.Equal(in.Output, encoded) {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := finishInvocationTx(ctx, tx, in, encoded); err != nil {
		return err
	}
	if err := invocationAllowed(ctx, tx, in.TaskID, false); err != nil {
		return err
	}
	return tx.Commit()
}

func finishInvocationTx(ctx context.Context, tx *sql.Tx, in domain.Invocation, encoded json.RawMessage) error {
	if err := invocationAllowed(ctx, tx, in.TaskID, false); err != nil {
		return err
	}
	items, err := invocationsForOperationTx(ctx, tx, in.TaskID, in.OperationID)
	if err != nil {
		return err
	}
	var prior *domain.Invocation
	for i := range items {
		if items[i].ID == in.ID {
			prior = &items[i]
			break
		}
	}
	if prior == nil {
		return ErrNotFound
	}
	if !sameInvocationIdentity(in, *prior) {
		return ErrConflict
	}
	state, version, err := invocationCheckpointState(ctx, tx, in.TaskID)
	if err != nil {
		return err
	}
	hash := invocationDigest(encoded)
	if prior.Status != "started" {
		if prior.Status == in.Status && prior.OutputHash == hash && prior.ErrorCode == in.ErrorCode {
			return nil
		}
		return ErrConflict
	}
	in.StartedAt = prior.StartedAt
	now := time.Now().UTC()
	in.FinishedAt = &now
	in.OutputHash = hash
	in.Output = nil
	eventType := "invocation.failed"
	if in.Status == "completed" {
		eventType = "invocation.completed"
	}
	payload := map[string]any{"invocation": in, "step_id": in.StepID, "result": encoded}
	if strings.HasPrefix(in.Kind, "tool.") {
		payload["tool"] = strings.TrimPrefix(in.Kind, "tool.")
	}
	seq, err := appendEvent(ctx, tx, in.TaskID, eventType, nil, "internal", payload)
	if err != nil {
		return err
	}
	if state["pending_invocation_id"] == in.ID {
		delete(state, "pending_invocation_id")
		delete(state, "pending_invocation")
	}
	return insertCheckpoint(ctx, tx, in.TaskID, version, seq, state)
}

func decodeInvocationEvent(eventType, raw string) (domain.Invocation, error) {
	invalid := func() (domain.Invocation, error) { return domain.Invocation{}, ErrInvocationInvalid }
	if len(raw) > invocationOutputLimit+(16<<10) {
		return invalid()
	}
	var payload struct {
		Invocation domain.Invocation `json:"invocation"`
		Tool       string            `json:"tool"`
		StepID     string            `json:"step_id"`
		Result     json.RawMessage   `json:"result"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return invalid()
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return invalid()
	}
	in := payload.Invocation
	if err := validateInvocation(in); err != nil {
		return invalid()
	}
	if len(in.Output) != 0 || in.StartedAt == nil || in.StartedAt.IsZero() {
		return invalid()
	}
	if eventType == "invocation.started" {
		if in.Status != "started" || in.FinishedAt != nil || in.OutputHash != "" || in.ErrorCode != "" || len(payload.Result) != 0 || payload.Tool != "" || payload.StepID != "" {
			return invalid()
		}
	} else {
		if eventType != "invocation.completed" && eventType != "invocation.failed" {
			return invalid()
		}
		if in.Status != "completed" && in.Status != "failed" && in.Status != "unknown" || (eventType == "invocation.completed") != (in.Status == "completed") || in.FinishedAt == nil || in.FinishedAt.Before(*in.StartedAt) || len(payload.Result) == 0 || len(payload.Result) > invocationOutputLimit || in.OutputHash != invocationDigest(payload.Result) || payload.StepID != in.StepID {
			return invalid()
		}
		tool := ""
		if strings.HasPrefix(in.Kind, "tool.") {
			tool = strings.TrimPrefix(in.Kind, "tool.")
		}
		if payload.Tool != tool {
			return invalid()
		}
		in.Output = payload.Result
	}
	return in, nil
}

func invocationsForOperationTx(ctx context.Context, tx *sql.Tx, taskID, operationID string) ([]domain.Invocation, error) {
	var malformed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE task_id=? AND type IN ('invocation.started','invocation.completed','invocation.failed') AND CASE WHEN json_valid(payload_json) THEN COALESCE(json_type(payload_json,'$.invocation.operation_id'),'')<>'text' ELSE 1 END)`, taskID).Scan(&malformed); err != nil {
		return nil, err
	}
	if malformed {
		return nil, ErrInvocationInvalid
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,type,visibility,payload_json FROM events WHERE task_id=? AND type IN ('invocation.started','invocation.completed','invocation.failed') AND CASE WHEN json_valid(payload_json) THEN json_extract(payload_json,'$.invocation.operation_id') END=? ORDER BY seq LIMIT 9`, taskID, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]int)
	var items []domain.Invocation
	count := 0
	for rows.Next() {
		count++
		if count > 8 {
			return nil, ErrInvocationInvalid
		}
		var eventID, kind, visibility, raw string
		if err := rows.Scan(&eventID, &kind, &visibility, &raw); err != nil {
			return nil, err
		}
		in, err := decodeInvocationEvent(kind, raw)
		if err != nil {
			return nil, err
		}
		if in.TaskID != taskID || in.OperationID != operationID || visibility != "internal" {
			return nil, ErrInvocationInvalid
		}
		index, exists := byID[in.ID]
		if kind == "invocation.started" {
			if eventID != "invocation-started-"+invocationIdentity(taskID, in.IdempotencyKey) {
				return nil, ErrInvocationInvalid
			}
			if exists || len(items) >= 4 {
				return nil, ErrInvocationInvalid
			}
			for _, item := range items {
				if item.Attempt == in.Attempt || item.IdempotencyKey == in.IdempotencyKey || item.InputHash != in.InputHash || item.Kind != in.Kind || item.Role != in.Role || item.StepID != in.StepID {
					return nil, ErrInvocationInvalid
				}
			}
			byID[in.ID] = len(items)
			items = append(items, in)
		} else {
			if !exists || items[index].Status != "started" || !sameInvocationIdentity(items[index], in) || !items[index].StartedAt.Equal(*in.StartedAt) {
				return nil, ErrInvocationInvalid
			}
			items[index] = in
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Attempt < items[j].Attempt })
	return items, nil
}

func (s *Store) InvocationsForOperation(ctx context.Context, taskID, operationID string) ([]domain.Invocation, error) {
	if taskID == "" || len(taskID) > 128 || operationID == "" || len(operationID) > 512 {
		return nil, ErrInvocationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := verifiedCheckpoints(ctx, tx, taskID); err != nil {
		return nil, err
	}
	return invocationsForOperationTx(ctx, tx, taskID, operationID)
}

func completePendingStepInvocationTx(ctx context.Context, tx *sql.Tx, taskID, stepID, tool string, result any, state any) (any, error) {
	latest, _, err := invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	id, _ := latest["pending_invocation_id"].(string)
	if id == "" {
		raw, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		var supplied map[string]any
		if err := json.Unmarshal(raw, &supplied); err != nil {
			return nil, err
		}
		id, _ = supplied["pending_invocation_id"].(string)
		if id == "" {
			return state, nil
		}
		latest = supplied
	}
	raw, err := json.Marshal(latest["pending_invocation"])
	if err != nil {
		return nil, err
	}
	var in domain.Invocation
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, ErrInvocationInvalid
	}
	if err := validateInvocation(in); err != nil {
		return nil, err
	}
	if in.ID != id || in.TaskID != taskID || in.StepID != stepID || in.Kind != "tool."+tool {
		return nil, ErrConflict
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > invocationOutputLimit {
		return nil, ErrInvocationInvalid
	}
	in.Status = "completed"
	if err := finishInvocationTx(ctx, tx, in, encoded); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values != nil && values["pending_invocation_id"] == id {
		delete(values, "pending_invocation_id")
		delete(values, "pending_invocation")
	}
	return values, nil
}

func reconcileInvocationCheckpoint(ctx context.Context, tx *sql.Tx, taskID string, encoded []byte) ([]byte, error) {
	var state map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &state); err != nil || state == nil {
		return encoded, nil
	}
	raw, present := state["pending_invocation_id"]
	if !present {
		return encoded, nil
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, ErrInvocationInvalid
	}
	if id == "" {
		return encoded, nil
	}
	var pending domain.Invocation
	if err := json.Unmarshal(state["pending_invocation"], &pending); err != nil {
		return nil, ErrInvocationInvalid
	}
	if pending.ID != id || pending.TaskID != taskID {
		return nil, ErrInvocationInvalid
	}
	if err := validateInvocation(pending); err != nil {
		return nil, err
	}
	var kind, payload string
	err := tx.QueryRowContext(ctx, `SELECT type,payload_json FROM events WHERE task_id=? AND type IN ('invocation.completed','invocation.failed') AND CASE WHEN json_valid(payload_json) THEN json_extract(payload_json,'$.invocation.id') END=? ORDER BY seq DESC LIMIT 1`, taskID, id).Scan(&kind, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return encoded, nil
	}
	if err != nil {
		return nil, err
	}
	finished, err := decodeInvocationEvent(kind, payload)
	if err != nil {
		return nil, err
	}
	if !sameInvocationIdentity(pending, finished) {
		return nil, ErrInvocationInvalid
	}
	delete(state, "pending_invocation_id")
	delete(state, "pending_invocation")
	return json.Marshal(state)
}
