package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
)

func validateResearchContextEvidenceTx(ctx context.Context, tx *sql.Tx, runID string, bridge domain.ResearchContext) error {
	var conversationID, assistantID string
	if err := tx.QueryRowContext(ctx, `SELECT conversation_id,assistant_message_id FROM tasks WHERE id=?`, runID).Scan(&conversationID, &assistantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if conversationID == "" {
		return ErrConflict
	}
	var answer, messageStatus, metadataRaw string
	if err := tx.QueryRowContext(ctx, `SELECT content,status,metadata_json FROM conversation_messages WHERE id=? AND run_id=? AND conversation_id=? AND role='assistant'`, assistantID, runID, conversationID).Scan(&answer, &messageStatus, &metadataRaw); err != nil {
		return ErrCheckpointInvalid
	}
	if messageStatus != "completed" {
		return ErrConflict
	}
	var metadata map[string]any
	if json.Unmarshal([]byte(metadataRaw), &metadata) != nil {
		return ErrCheckpointInvalid
	}
	verification, ok := metadata["verification"].(map[string]any)
	if !ok || verification["status"] != bridge.VerificationStatus {
		return ErrCheckpointInvalid
	}
	citations, ok := verification["citations"].([]any)
	if !ok {
		return ErrCheckpointInvalid
	}
	verifiedRefs := map[string]string{}
	for _, value := range citations {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var ref struct {
			EvidenceID string `json:"evidence_id"`
			URL        string `json:"url"`
		}
		if json.Unmarshal(raw, &ref) == nil && ref.EvidenceID != "" {
			verifiedRefs[ref.EvidenceID] = ref.URL
		}
	}
	if bridge.VerificationStatus == "fallback_excerpts" && len(bridge.Claims) != 0 {
		return ErrCheckpointInvalid
	}
	for _, source := range bridge.Sources {
		var status int
		var rawFlags, excerpt, canonical, title string
		err := tx.QueryRowContext(ctx, `SELECT http_status,quality_flags_json,excerpt,canonical_url,title FROM evidence WHERE task_id=? AND id=?`, runID, source.EvidenceID).Scan(&status, &rawFlags, &excerpt, &canonical, &title)
		var flags []string
		titleRunes := []rune(strings.TrimSpace(title))
		if len(titleRunes) > 160 {
			titleRunes = titleRunes[:160]
		}
		if err != nil || json.Unmarshal([]byte(rawFlags), &flags) != nil || status < 200 || status >= 300 || len(flags) > 0 || strings.TrimSpace(excerpt) == "" || canonical != source.CanonicalURL || source.Title != string(titleRunes) || domain.SensitiveResearchContextURL(canonical) || verifiedRefs[source.EvidenceID] != canonical {
			return ErrCheckpointInvalid
		}
	}
	for _, claim := range bridge.Claims {
		if !strings.Contains(answer, claim.Text+" ["+claim.EvidenceID+"]") || verifiedRefs[claim.EvidenceID] == "" {
			return ErrCheckpointInvalid
		}
	}
	return nil
}
