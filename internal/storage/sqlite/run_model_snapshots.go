package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func validateModelProfileSnapshot(model domain.ModelProfileSnapshot) error {
	if model.Bundle != nil {
		if model.Bundle.ModelProfile != model.Model {
			return errors.New("bundle model profile does not match the model snapshot")
		}
		if err := model.Bundle.Validate(); err != nil {
			return err
		}
	}
	if model.Version != 1 || model.SchemaVersion != 1 {
		return errors.New("unsupported model snapshot version")
	}
	if strings.TrimSpace(model.Model) == "" || len(model.Model) > 1024 || model.ContextLength <= 0 || model.MaxOutputTokens <= 0 || model.MaxOutputTokens >= model.ContextLength ||
		math.IsNaN(model.Temperature) || math.IsInf(model.Temperature, 0) || model.Temperature < 0 || model.Temperature > 2 ||
		math.IsNaN(model.TopP) || math.IsInf(model.TopP, 0) || model.TopP <= 0 || model.TopP > 1 || len(model.StopSequences) > 32 || len(model.PromptVersion) > 256 {
		return errors.New("invalid model snapshot")
	}
	for _, stop := range model.StopSequences {
		if len(stop) > 4096 {
			return errors.New("invalid model stop sequence")
		}
	}
	if model.BaseURL != "" {
		endpoint, err := url.Parse(model.BaseURL)
		if err != nil || len(model.BaseURL) > 4096 || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" ||
			endpoint.Hostname() != "localhost" && !net.ParseIP(endpoint.Hostname()).IsLoopback() {
			return errors.New("invalid model endpoint")
		}
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return err
	}
	if len(encoded) > 64<<10 {
		return errors.New("model snapshot exceeds 65536 bytes")
	}
	return nil
}

func (s *Store) GetRunModelSnapshot(ctx context.Context, taskID string) (domain.ModelProfileSnapshot, error) {
	return runModelSnapshot(ctx, s.db, taskID)
}

func runModelSnapshot(ctx context.Context, q conversationQuery, taskID string) (domain.ModelProfileSnapshot, error) {
	session, err := researchSession(ctx, q, taskID)
	if err == nil {
		return session.ModelSnapshot, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.ModelProfileSnapshot{}, err
	}
	model, _, err := chatRunModelSnapshot(ctx, q, taskID)
	return model, err
}

func chatRunModelSnapshot(ctx context.Context, q conversationQuery, taskID string) (domain.ModelProfileSnapshot, bool, error) {
	var initial, latest, created sql.NullString
	err := q.QueryRowContext(ctx, `SELECT
		(SELECT state_json FROM checkpoints WHERE task_id=tasks.id ORDER BY rowid LIMIT 1),
		(SELECT state_json FROM checkpoints WHERE task_id=tasks.id ORDER BY rowid DESC LIMIT 1),
		(SELECT payload_json FROM events WHERE task_id=tasks.id AND type='task.created' ORDER BY seq LIMIT 1)
		FROM tasks WHERE id=? AND mode='chat' AND conversation_id IS NOT NULL`, taskID).Scan(&initial, &latest, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ModelProfileSnapshot{}, false, ErrNotFound
	}
	if err != nil {
		return domain.ModelProfileSnapshot{}, false, err
	}
	expected := false
	if created.Valid {
		values, err := chatCheckpointValues([]byte(created.String))
		if err != nil {
			return domain.ModelProfileSnapshot{}, true, err
		}
		if _, present := values["model_snapshot_schema_version"]; present {
			version, valid := checkpointInteger(values, "model_snapshot_schema_version")
			if !valid || version != 1 {
				return domain.ModelProfileSnapshot{}, true, errors.New("unsupported run model snapshot schema version")
			}
			expected = true
		}
	}
	if !initial.Valid {
		if expected {
			return domain.ModelProfileSnapshot{}, true, errors.New("run model snapshot checkpoint is missing")
		}
		return domain.ModelProfileSnapshot{}, true, ErrNotFound
	}
	model, present, err := decodeChatModelSnapshot([]byte(initial.String))
	if err != nil {
		return domain.ModelProfileSnapshot{}, true, err
	}
	if !present {
		if expected {
			return domain.ModelProfileSnapshot{}, true, errors.New("run model snapshot metadata is missing")
		}
		return domain.ModelProfileSnapshot{}, true, ErrNotFound
	}
	if latest.Valid && latest.String != initial.String {
		mirror, present, err := decodeChatModelSnapshot([]byte(latest.String))
		if err != nil {
			return domain.ModelProfileSnapshot{}, true, err
		}
		if !present {
			return domain.ModelProfileSnapshot{}, true, errors.New("run checkpoint lost its model snapshot")
		}
		originalJSON, _ := json.Marshal(model)
		mirrorJSON, _ := json.Marshal(mirror)
		if !bytes.Equal(originalJSON, mirrorJSON) {
			return domain.ModelProfileSnapshot{}, true, fmt.Errorf("%w: run model snapshot changed", ErrConflict)
		}
	}
	return model, true, nil
}

func chatCheckpointValues(encoded []byte) (map[string]json.RawMessage, error) {
	if !json.Valid(encoded) {
		return nil, errors.New("invalid run checkpoint JSON")
	}
	if bytes.TrimSpace(encoded)[0] != '{' {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid run checkpoint JSON key")
		}
		if _, duplicate := values[key]; duplicate && (key == "model_snapshot" || key == "model_snapshot_schema_version") {
			return nil, errors.New("duplicate run model snapshot metadata")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return values, nil
}

func decodeChatModelSnapshot(encoded []byte) (domain.ModelProfileSnapshot, bool, error) {
	values, err := chatCheckpointValues(encoded)
	if err != nil {
		return domain.ModelProfileSnapshot{}, false, err
	}
	_, versionPresent := values["model_snapshot_schema_version"]
	raw, modelPresent := values["model_snapshot"]
	if !versionPresent && !modelPresent {
		return domain.ModelProfileSnapshot{}, false, nil
	}
	version, valid := checkpointInteger(values, "model_snapshot_schema_version")
	if !valid || version != 1 || !modelPresent {
		return domain.ModelProfileSnapshot{}, false, errors.New("unsupported or incomplete run model snapshot metadata")
	}
	var model domain.ModelProfileSnapshot
	if err := decodeResearchJSON(string(raw), &model); err != nil {
		return domain.ModelProfileSnapshot{}, false, fmt.Errorf("decode run model snapshot: %w", err)
	}
	if err := validateModelProfileSnapshot(model); err != nil {
		return domain.ModelProfileSnapshot{}, false, err
	}
	return model, true, nil
}

func insertInitialChatModelSnapshot(ctx context.Context, tx *sql.Tx, taskID string, seq int64, created time.Time, model domain.ModelProfileSnapshot) error {
	if err := validateModelProfileSnapshot(model); err != nil {
		return err
	}
	state, err := json.Marshal(map[string]any{"model_snapshot_schema_version": 1, "model_snapshot": model})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO checkpoints(id,task_id,plan_version,state_json,last_event_seq,created_at) VALUES(?,?,1,?,?,?)`, newID(), taskID, string(state), seq, stamp(created))
	return err
}

func prepareChatCheckpoint(ctx context.Context, tx *sql.Tx, taskID string, encoded []byte) ([]byte, error) {
	model, chat, err := chatRunModelSnapshot(ctx, tx, taskID)
	if !chat && errors.Is(err, ErrNotFound) {
		return encoded, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	values, decodeErr := chatCheckpointValues(encoded)
	if decodeErr != nil {
		return nil, decodeErr
	}
	if errors.Is(err, ErrNotFound) {
		if _, present := values["model_snapshot_schema_version"]; present {
			return nil, fmt.Errorf("%w: legacy run model snapshots cannot be backfilled by a checkpoint", ErrConflict)
		}
		if _, present := values["model_snapshot"]; present {
			return nil, fmt.Errorf("%w: legacy run model snapshots cannot be backfilled by a checkpoint", ErrConflict)
		}
		return encoded, nil
	}
	if values == nil {
		if !bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
			return nil, errors.New("run checkpoint with a model snapshot must be a JSON object")
		}
		values = make(map[string]json.RawMessage)
	}
	if _, _, err := decodeChatModelSnapshot(encoded); err != nil {
		return nil, err
	}
	modelJSON, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	values["model_snapshot_schema_version"] = json.RawMessage("1")
	values["model_snapshot"] = modelJSON
	return json.Marshal(values)
}
