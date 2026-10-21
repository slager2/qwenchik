package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
)

func verificationURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || research.URLHasSensitiveData(u) {
		return ""
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

func verificationTitle(title string) string {
	lower := strings.ToLower(title)
	for _, marker := range []string{"bearer ", "token=", "secret=", "password=", "api_key=", "apikey=", "authorization:", "://"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if !utf8.ValidString(title) {
		return ""
	}
	runes := []rune(title)
	if len(runes) > 500 {
		runes = runes[:500]
	}
	return string(runes)
}

func SafeVerificationMetadata(raw json.RawMessage, owned []domain.Evidence) map[string]any {
	var payload struct {
		Phase     string `json:"phase"`
		Status    string `json:"status"`
		Reason    string `json:"reason_code"`
		Method    string `json:"method"`
		Claims    int    `json:"claim_count"`
		Citations []struct {
			EvidenceID string `json:"evidence_id"`
			URL        string `json:"url"`
		} `json:"citations"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return map[string]any{}
	}
	result := map[string]any{}
	if payload.Phase == "citation_verification" {
		result["phase"] = payload.Phase
	}
	switch payload.Status {
	case "started", "supported", "fallback_excerpts":
		result["status"] = payload.Status
	default:
		return result
	}
	switch payload.Reason {
	case "", "citation_evidence_unavailable", "claim_not_supported", "context_budget_exceeded", "finalization_model_unavailable", "citation_mapping_invalid":
		result["reason_code"] = payload.Reason
	}
	switch payload.Method {
	case "local_model_entailment_and_exact_excerpt_matching", "verbatim_excerpt_fallback":
		result["method"] = payload.Method
	}
	if payload.Claims >= 0 && payload.Claims <= 8 {
		result["claim_count"] = payload.Claims
	}
	refs := []map[string]string{}
	catalogue := map[string]domain.Evidence{}
	for _, e := range owned {
		if e.HTTPStatus >= 200 && e.HTTPStatus < 300 && len(e.QualityFlags) == 0 && strings.TrimSpace(e.Excerpt) != "" {
			catalogue[e.ID] = e
		}
	}
	seen := map[string]bool{}
	if payload.Status != "started" {
		for _, citation := range payload.Citations {
			e, ok := catalogue[citation.EvidenceID]
			if !ok || seen[e.ID] {
				continue
			}
			canonical := verificationURL(e.CanonicalURL)
			if canonical == "" || verificationURL(citation.URL) != canonical {
				continue
			}
			seen[e.ID] = true
			refs = append(refs, map[string]string{"evidence_id": e.ID, "url": canonical, "title": verificationTitle(e.Title)})
			if len(refs) == 8 {
				break
			}
		}
	}
	result["citations"] = refs
	return result
}

func persistVerification(ctx context.Context, tx *sql.Tx, taskID, messageID string) error {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE task_id=? AND type='verification.updated' AND visibility='user' AND CASE WHEN json_valid(payload_json) THEN json_extract(payload_json,'$.status') END IN ('supported','fallback_excerpts') ORDER BY seq DESC LIMIT 1`, taskID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var status struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(raw), &status) != nil || (status.Status != "supported" && status.Status != "fallback_excerpts") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,canonical_url,title,http_status,quality_flags_json,excerpt FROM evidence WHERE task_id=?`, taskID)
	if err != nil {
		return err
	}
	owned := []domain.Evidence{}
	for rows.Next() {
		var e domain.Evidence
		var flags string
		if err = rows.Scan(&e.ID, &e.CanonicalURL, &e.Title, &e.HTTPStatus, &flags, &e.Excerpt); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(flags), &e.QualityFlags); err != nil {
			rows.Close()
			return err
		}
		owned = append(owned, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	metadata := map[string]any{"verification": SafeVerificationMetadata(json.RawMessage(raw), owned)}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversation_messages SET metadata_json=? WHERE id=?`, string(encoded), messageID)
	return err
}
