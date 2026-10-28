package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/slager2/qwenchik/internal/domain"
)

// Metadata enrichment is unnecessary for an already committed idempotent turn.
// The actual submission still repeats this lookup atomically before insertion.
func (s *Store) ReplaySubmitTurn(ctx context.Context, in domain.SubmitTurn, key string) (domain.SubmitResponse, bool, error) {
	if key == "" {
		return domain.SubmitResponse{}, false, nil
	}
	var id, op, prior string
	err := s.db.QueryRowContext(ctx, `SELECT resource_id,operation,COALESCE(request_sha256,'') FROM idempotency_keys WHERE key=?`, key).Scan(&id, &op, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SubmitResponse{}, false, nil
	}
	if err != nil {
		return domain.SubmitResponse{}, false, err
	}
	raw, _ := json.Marshal(in)
	h := sha256.Sum256(raw)
	if op != "conversations.submit" || prior != hex.EncodeToString(h[:]) {
		return domain.SubmitResponse{}, false, ErrIdempotency
	}
	result, err := submitResult(ctx, s.db, id)
	return result, true, err
}
