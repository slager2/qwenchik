package sqlite

import (
	"encoding/json"
	"github.com/slager2/qwenchik/internal/domain"
	"sync"
)

type checkpointValidationRecord struct {
	Snapshot bool
	Number   int
	Hash     string
	Pins     map[string]string // canonical input hashes, never model-input bodies
}

// A bounded cache of PURE validation results, never states or SQL permissions.
// Each key covers actual row bytes, metadata, associated event bytes and the
// complete predecessor key. Every read still reads/hashes the whole SQL chain;
// changing/deleting any checkpoint or its pin event invalidates descendants.
// Lease, deadline, budget and ownership admission are never cached.
var checkpointValidation = struct {
	sync.Mutex
	valid map[string]checkpointValidationRecord
}{valid: map[string]checkpointValidationRecord{}}

func checkpointValidationKnown(key string) (checkpointValidationRecord, bool) {
	checkpointValidation.Lock()
	defer checkpointValidation.Unlock()
	r, ok := checkpointValidation.valid[key]
	return r, ok
}
func rememberCheckpointValidation(key string, record checkpointValidationRecord) {
	checkpointValidation.Lock()
	defer checkpointValidation.Unlock()
	if len(checkpointValidation.valid) >= 2048 {
		checkpointValidation.valid = map[string]checkpointValidationRecord{}
	}
	checkpointValidation.valid[key] = record
}

func validateCheckpointRecord(item domain.Checkpoint, previous checkpointValidationRecord, eventType, eventVisibility, eventPayload string) (checkpointValidationRecord, error) {
	prior := []domain.Checkpoint{}
	if previous.Snapshot {
		prior = append(prior, domain.Checkpoint{State: map[string]any{"context_snapshot": domain.ContextSnapshot{Version: 1, Number: previous.Number, Hash: previous.Hash}}})
	}
	if err := verifyContextSnapshotCheckpoint(item, prior); err != nil {
		return checkpointValidationRecord{}, err
	}
	snapshot, exists, err := checkpointSnapshot(item.State)
	if err != nil {
		return checkpointValidationRecord{}, err
	}
	pins, err := roleInputsFromState(item.State)
	if err != nil {
		return checkpointValidationRecord{}, err
	}
	r := checkpointValidationRecord{Snapshot: exists, Number: snapshot.Number, Hash: snapshot.Hash, Pins: map[string]string{}}
	for slot, oldHash := range previous.Pins {
		pin, ok := pins[slot]
		if !ok || pin.InputHash != oldHash {
			return r, ErrCheckpointInvalid
		}
	}
	newPins := 0
	for slot, pin := range pins {
		r.Pins[slot] = pin.InputHash
		if _, old := previous.Pins[slot]; old {
			continue
		}
		newPins++
		var event struct {
			Slot      string `json:"slot"`
			InputHash string `json:"input_hash"`
		}
		if eventType != "context.role_pinned" || eventVisibility != "internal" || json.Unmarshal([]byte(eventPayload), &event) != nil || event.Slot != slot || event.InputHash != pin.InputHash {
			return r, ErrCheckpointInvalid
		}
	}
	if newPins > 1 {
		return r, ErrCheckpointInvalid
	}
	return r, nil
}
