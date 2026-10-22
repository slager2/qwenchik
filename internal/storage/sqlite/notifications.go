package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func notificationTerminal(kind string) bool {
	switch kind {
	case "task.completed", "task.failed", "task.cancelled", "run.blocked", "run.failed":
		return true
	}
	return false
}

func (s *Store) ClaimNotification(ctx context.Context, channel string) (domain.NotificationDelivery, bool, error) {
	return s.claimNotificationAt(ctx, channel, time.Now().UTC())
}

func (s *Store) claimNotificationAt(ctx context.Context, channel string, now time.Time) (domain.NotificationDelivery, bool, error) {
	if channel != "desktop" && channel != "ntfy" {
		return domain.NotificationDelivery{}, false, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NotificationDelivery{}, false, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT e.task_id,e.seq,e.type,COALESCE(json_extract(e.payload_json,'$.terminal_outcome'),'') FROM events e WHERE e.type IN ('task.completed','task.failed','task.cancelled','run.blocked','run.failed') AND json_valid(e.payload_json) AND json_extract(e.payload_json,'$.notification_schema_version')=1 AND NOT EXISTS(SELECT 1 FROM events n WHERE n.task_id=e.task_id AND n.type IN ('notification.delivered','notification.abandoned') AND json_valid(n.payload_json) AND json_extract(n.payload_json,'$.terminal_seq')=e.seq) ORDER BY e.created_at,e.seq LIMIT 32`)
	if err != nil {
		return domain.NotificationDelivery{}, false, err
	}
	candidates := []domain.NotificationDelivery{}
	for rows.Next() {
		var delivery domain.NotificationDelivery
		var kind string
		if err := rows.Scan(&delivery.TaskID, &delivery.TerminalSeq, &kind, &delivery.Outcome); err != nil {
			rows.Close()
			return delivery, false, err
		}
		if delivery.Outcome == "" {
			switch kind {
			case "task.completed":
				delivery.Outcome = "completed"
			case "task.cancelled":
				delivery.Outcome = "cancelled"
			default:
				delivery.Outcome = "failed"
			}
		}
		candidates = append(candidates, delivery)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.NotificationDelivery{}, false, err
	}
	rows.Close()
	for _, delivery := range candidates {
		var raw, kind string
		err := tx.QueryRowContext(ctx, `SELECT type,payload_json FROM events WHERE task_id=? AND type IN ('notification.attempted','notification.failed') AND json_valid(payload_json) AND json_extract(payload_json,'$.terminal_seq')=? ORDER BY seq DESC LIMIT 1`, delivery.TaskID, delivery.TerminalSeq).Scan(&kind, &raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return delivery, false, err
		}
		var prior struct {
			Attempt     int       `json:"attempt"`
			LeaseUntil  time.Time `json:"lease_until"`
			NextAttempt time.Time `json:"next_attempt_at"`
		}
		if raw != "" && json.Unmarshal([]byte(raw), &prior) != nil {
			return delivery, false, ErrCheckpointInvalid
		}
		if prior.Attempt < 0 || prior.Attempt > 3 {
			return delivery, false, ErrCheckpointInvalid
		}
		if kind == "notification.attempted" && now.Before(prior.LeaseUntil) || kind == "notification.failed" && now.Before(prior.NextAttempt) {
			continue
		}
		if prior.Attempt >= 3 {
			if _, err := appendEvent(ctx, tx, delivery.TaskID, "notification.abandoned", nil, "internal", map[string]any{"terminal_seq": delivery.TerminalSeq, "attempt": prior.Attempt, "reason": "unknown_delivery_after_crash"}); err != nil {
				return delivery, false, err
			}
			continue
		}
		delivery.Channel, delivery.ClaimID, delivery.Attempt = channel, newID(), prior.Attempt+1
		if _, err := appendEvent(ctx, tx, delivery.TaskID, "notification.attempted", nil, "internal", map[string]any{"terminal_seq": delivery.TerminalSeq, "claim_id": delivery.ClaimID, "attempt": delivery.Attempt, "channel": channel, "lease_until": now.Add(30 * time.Second)}); err != nil {
			return delivery, false, err
		}
		if err := tx.Commit(); err != nil {
			return delivery, false, err
		}
		return delivery, true, nil
	}
	return domain.NotificationDelivery{}, false, tx.Commit()
}

func (s *Store) FinishNotification(ctx context.Context, delivery domain.NotificationDelivery, success bool) error {
	if delivery.Attempt < 1 || delivery.Attempt > 3 || delivery.ClaimID == "" {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, kind string
	err = tx.QueryRowContext(ctx, `SELECT type,payload_json FROM events WHERE task_id=? AND type LIKE 'notification.%' AND json_valid(payload_json) AND json_extract(payload_json,'$.terminal_seq')=? ORDER BY seq DESC LIMIT 1`, delivery.TaskID, delivery.TerminalSeq).Scan(&kind, &raw)
	if err != nil {
		return err
	}
	var claim domain.NotificationDelivery
	if json.Unmarshal([]byte(raw), &claim) != nil || claim.ClaimID != delivery.ClaimID || claim.Attempt != delivery.Attempt || kind != "notification.attempted" {
		return ErrConflict
	}
	status := "notification.delivered"
	payload := map[string]any{"terminal_seq": delivery.TerminalSeq, "claim_id": delivery.ClaimID, "attempt": delivery.Attempt, "channel": delivery.Channel}
	if !success {
		status = "notification.failed"
		payload["error_code"] = "delivery_unavailable"
		payload["next_attempt_at"] = time.Now().UTC().Add(time.Duration(5<<(delivery.Attempt-1)) * time.Second)
		if delivery.Attempt >= 3 {
			status = "notification.abandoned"
		}
	}
	if _, err := appendEvent(ctx, tx, delivery.TaskID, status, nil, "internal", payload); err != nil {
		return err
	}
	return tx.Commit()
}
