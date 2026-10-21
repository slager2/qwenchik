package api

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safeStepID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == ':') {
			return false
		}
	}
	return true
}

func (s *Server) projectPublicEvent(ctx context.Context, event domain.Event) (domain.Event, error) {
	if event.Type != "verification.updated" {
		return safePublicEvent(event), nil
	}
	owned, err := s.service.Evidence(ctx, event.TaskID)
	if err != nil {
		return event, err
	}
	event.Payload, _ = json.Marshal(sqlite.SafeVerificationMetadata(event.Payload, owned))
	return event, nil
}
