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
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

const (
	MaxRoleContextPins       = 128
	MaxRoleContextInputBytes = 128 << 10
	MaxRoleContextTotalBytes = 1 << 20
)

var roleContextSlot = regexp.MustCompile(`^[0-9a-f]{64}$`)

type pinnedRoleInput struct {
	Version   int             `json:"version"`
	InputHash string          `json:"input_hash"`
	Input     json.RawMessage `json:"input"`
}

func canonicalRoleJSON(raw []byte) (json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("role input must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var readValue func(int) (any, error)
	readValue = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("role input nesting exceeds 64")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch token {
		case json.Delim('{'):
			values := make(map[string]any)
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid role input key")
				}
				if _, exists := values[name]; exists {
					return nil, errors.New("duplicate role input key")
				}
				value, err := readValue(depth + 1)
				if err != nil {
					return nil, err
				}
				values[name] = value
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return values, nil
		case json.Delim('['):
			values := []any{}
			for decoder.More() {
				value, err := readValue(depth + 1)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return values, nil
		default:
			return token, nil
		}
	}
	value, err := readValue(0)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("role input must be one JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("role input must be one JSON object")
	}
	return json.Marshal(value)
}

func roleInputHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func decodeRoleInputs(raw json.RawMessage) (map[string]pinnedRoleInput, error) {
	values := make(map[string]pinnedRoleInput)
	if raw == nil {
		return values, nil
	}
	if len(raw) > MaxRoleContextTotalBytes {
		return nil, ErrContextSnapshotTooLarge
	}
	canonical, err := canonicalRoleJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: role inputs: %v", ErrCheckpointInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("%w: role inputs: %v", ErrCheckpointInvalid, err)
	}
	if len(values) > MaxRoleContextPins {
		return nil, ErrContextSnapshotTooLarge
	}
	for slot, pin := range values {
		if !roleContextSlot.MatchString(slot) || pin.Version != 1 || !roleContextSlot.MatchString(pin.InputHash) {
			return nil, ErrCheckpointInvalid
		}
		if len(pin.Input) > MaxRoleContextInputBytes {
			return nil, ErrContextSnapshotTooLarge
		}
		input, err := canonicalRoleJSON(pin.Input)
		if err != nil || pin.InputHash != roleInputHash(input) {
			return nil, ErrCheckpointInvalid
		}
		pin.Input = input
		values[slot] = pin
	}
	return values, nil
}

func roleInputsFromState(state any) (map[string]pinnedRoleInput, error) {
	if values, ok := state.(map[string]any); ok {
		if raw, ok := values["role_inputs"].(json.RawMessage); ok {
			return decodeRoleInputs(raw)
		}
		if _, exists := values["role_inputs"]; !exists {
			return make(map[string]pinnedRoleInput), nil
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return decodeRoleInputs(checkpointObject(encoded)["role_inputs"])
}

func mergeRoleInputs(values, previous map[string]pinnedRoleInput) error {
	for slot, old := range previous {
		if current, exists := values[slot]; exists && (current.Version != old.Version || current.InputHash != old.InputHash || !bytes.Equal(current.Input, old.Input)) {
			return ErrCheckpointInvalid
		}
		values[slot] = old
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = decodeRoleInputs(encoded)
	return err
}

func validateRoleInputsState(state any, prior []domain.Checkpoint) error {
	values, err := roleInputsFromState(state)
	if err != nil {
		return err
	}
	if len(prior) == 0 {
		return nil
	}
	previous, err := roleInputsFromState(prior[len(prior)-1].State)
	if err != nil {
		return err
	}
	return mergeRoleInputs(values, previous)
}

func verifyRoleInputsCheckpoint(ctx context.Context, tx *sql.Tx, item domain.Checkpoint, prior []domain.Checkpoint) error {
	if len(prior) > 0 {
		current, currentOK := item.State.(map[string]any)
		previous, previousOK := prior[len(prior)-1].State.(map[string]any)
		if currentOK && previousOK {
			raw, rawOK := current["role_inputs"].(json.RawMessage)
			old, oldOK := previous["role_inputs"].(json.RawMessage)
			if rawOK && oldOK && bytes.Equal(raw, old) {
				return nil
			}
		}
	}
	values, err := roleInputsFromState(item.State)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCheckpointInvalid, err)
	}
	previous := make(map[string]pinnedRoleInput)
	if len(prior) > 0 {
		previous, err = roleInputsFromState(prior[len(prior)-1].State)
		if err != nil {
			return ErrCheckpointInvalid
		}
	}
	for slot, old := range previous {
		current, exists := values[slot]
		if !exists || current.Version != old.Version || current.InputHash != old.InputHash || !bytes.Equal(current.Input, old.Input) {
			return ErrCheckpointInvalid
		}
	}
	newPins := 0
	for slot, pin := range values {
		if _, exists := previous[slot]; exists {
			continue
		}
		newPins++
		var payload string
		if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE task_id=? AND seq=? AND type='context.role_pinned' AND visibility='internal'`, item.TaskID, item.LastEventSeq).Scan(&payload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCheckpointInvalid
			}
			return err
		}
		var event struct {
			Slot      string `json:"slot"`
			InputHash string `json:"input_hash"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil || event.Slot != slot || event.InputHash != pin.InputHash {
			return ErrCheckpointInvalid
		}
	}
	if newPins > 1 {
		return ErrCheckpointInvalid
	}
	return nil
}

func (s *Store) RoleContext(ctx context.Context, taskID, slot string) (json.RawMessage, error) {
	if !roleContextSlot.MatchString(slot) {
		return nil, ErrCheckpointInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	items, err := verifiedCheckpoints(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	values, err := roleInputsFromState(items[len(items)-1].State)
	if err != nil {
		return nil, err
	}
	pin, exists := values[slot]
	if !exists {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), pin.Input...), nil
}

func (s *Store) PinRoleContext(ctx context.Context, taskID, slot string, input json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !roleContextSlot.MatchString(slot) {
		return nil, ErrCheckpointInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return nil, err
	}
	state, planVersion, err := invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	values, err := roleInputsFromState(state)
	if err != nil {
		return nil, err
	}
	if pin, exists := values[slot]; exists {
		if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), pin.Input...), nil
	}
	if len(input) > MaxRoleContextInputBytes {
		return nil, ErrContextSnapshotTooLarge
	}
	canonical, err := canonicalRoleJSON(input)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCheckpointInvalid, err)
	}
	if len(canonical) > MaxRoleContextInputBytes {
		return nil, ErrContextSnapshotTooLarge
	}
	pin := pinnedRoleInput{Version: 1, InputHash: roleInputHash(canonical), Input: canonical}
	values[slot] = pin
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	if _, err := decodeRoleInputs(encoded); err != nil {
		return nil, err
	}
	state["role_inputs"] = json.RawMessage(encoded)
	seq, err := appendEvent(ctx, tx, taskID, "context.role_pinned", nil, "internal", map[string]any{"slot": slot, "input_hash": pin.InputHash, "input_bytes": len(canonical), "pin_count": len(values)})
	if err != nil {
		return nil, err
	}
	if _, err := saveCheckpointTx(ctx, tx, taskID, planVersion, seq, state); err != nil {
		return nil, err
	}
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), canonical...), nil
}
