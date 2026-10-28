package sqlite

import (
	"context"
	"github.com/slager2/qwenchik/internal/domain"
)

type InvocationSummaryRecord struct {
	Seq     int64                   `json:"seq"`
	Summary domain.InferenceSummary `json:"summary"`
}

func (s *Store) InferenceSummaries(ctx context.Context, id string, after int64) ([]InvocationSummaryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,payload_json FROM events WHERE task_id=? AND seq>? AND type='inference.summary' AND visibility='internal' ORDER BY seq LIMIT 128`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InvocationSummaryRecord{}
	for rows.Next() {
		var n int64
		var raw string
		if err := rows.Scan(&n, &raw); err != nil {
			return nil, err
		}
		summary, err := decodeInferenceSummary(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, InvocationSummaryRecord{Seq: n, Summary: summary})
	}
	return out, rows.Err()
}
