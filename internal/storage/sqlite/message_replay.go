package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/slager2/qwenchik/internal/domain"
)

func committedConversationDelta(ctx context.Context, tx *sql.Tx, taskID, streamID, messageID string, index int, delta string) (domain.Event, bool, error) {
	var event domain.Event
	var created, payload string
	var stage sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,task_id,seq,type,schema_version,created_at,stage,visibility,payload_json FROM events WHERE task_id=? AND type='message.delta' AND CASE WHEN json_valid(payload_json) THEN json_extract(payload_json,'$.stream_id') END=? AND CASE WHEN json_valid(payload_json) THEN json_extract(payload_json,'$.chunk_index') END=? ORDER BY seq LIMIT 1`, taskID, streamID, index).Scan(&event.ID, &event.TaskID, &event.Seq, &event.Type, &event.SchemaVersion, &created, &stage, &event.Visibility, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return event, false, nil
	}
	if err != nil {
		return event, false, err
	}
	var prior struct {
		MessageID string `json:"message_id"`
		Delta     string `json:"delta"`
	}
	if err = json.Unmarshal([]byte(payload), &prior); err != nil {
		return event, true, err
	}
	if prior.MessageID != messageID || prior.Delta != delta {
		return event, true, ErrConflict
	}
	event.CreatedAt = parseTime(created)
	if stage.Valid {
		s := domain.Stage(stage.String)
		event.Stage = &s
	}
	event.Payload = json.RawMessage(payload)
	return event, true, nil
}
