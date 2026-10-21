package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"path/filepath"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/urlcanon"
)

func (s *Store) SaveResearchSource(ctx context.Context, raw domain.Artifact, rawRef string, text domain.Artifact, textRef string, evidence domain.Evidence) (domain.Evidence, error) {
	canonical, err := urlcanon.Canonical(evidence.CanonicalURL)
	if err != nil || canonical != evidence.CanonicalURL || evidence.TaskID == "" || evidence.Provider == "" || evidence.OriginalURL == "" || evidence.ContentSHA256 == "" {
		return domain.Evidence{}, errors.New("evidence is missing valid provenance fields")
	}
	textMIME, _, mimeErr := mime.ParseMediaType(text.MIMEType)
	if raw.TaskID != evidence.TaskID || text.TaskID != evidence.TaskID || raw.ID != evidence.RawArtifactID || text.ID != evidence.TextArtifactID || raw.SHA256 != evidence.ContentSHA256 || mimeErr != nil || textMIME != "text/plain" {
		return domain.Evidence{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Evidence{}, err
	}
	defer tx.Rollback()
	if err := fenceExecution(ctx, tx, evidence.TaskID); err != nil {
		return domain.Evidence{}, err
	}
	existing, err := researchSourceEvidence(ctx, tx, evidence)
	duplicate := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.Evidence{}, err
	}
	if duplicate && (existing.RawArtifactID != raw.ID || existing.TextArtifactID != text.ID) {
		return domain.Evidence{}, ErrConflict
	}
	session, sessionErr := researchSession(ctx, tx, evidence.TaskID)
	if sessionErr != nil && !errors.Is(sessionErr, ErrNotFound) {
		return domain.Evidence{}, sessionErr
	}
	if sessionErr == nil {
		if err := researchReservationAllowed(ctx, tx, session); err != nil {
			return domain.Evidence{}, err
		}
		if !duplicate {
			count, err := researchEvidenceCapacity(ctx, tx, session)
			if err != nil {
				return domain.Evidence{}, err
			}
			session.Counters.Evidence = count + 1
		}
	}
	if err := saveResearchArtifact(ctx, tx, raw, rawRef); err != nil {
		return domain.Evidence{}, err
	}
	if err := saveResearchArtifact(ctx, tx, text, textRef); err != nil {
		return domain.Evidence{}, err
	}
	if duplicate {
		if err := tx.Commit(); err != nil {
			return domain.Evidence{}, err
		}
		return existing, nil
	}
	if evidence.ID == "" {
		evidence.ID = newID()
	}
	if evidence.RetrievedAt.IsZero() {
		evidence.RetrievedAt = time.Now().UTC()
	}
	if evidence.QuoteSpans == nil {
		evidence.QuoteSpans = []domain.QuoteSpan{}
	}
	if evidence.QualityFlags == nil {
		evidence.QualityFlags = []string{}
	}
	quotes, err := json.Marshal(evidence.QuoteSpans)
	if err != nil {
		return domain.Evidence{}, err
	}
	flags, err := json.Marshal(evidence.QualityFlags)
	if err != nil {
		return domain.Evidence{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evidence(id,task_id,provider,original_query,original_url,canonical_url,title,language,retrieved_at,http_status,content_sha256,extraction_method,raw_artifact_id,text_artifact_id,quote_spans_json,quality_flags_json,excerpt) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		evidence.ID, evidence.TaskID, evidence.Provider, evidence.OriginalQuery, evidence.OriginalURL, evidence.CanonicalURL,
		evidence.Title, evidence.Language, stamp(evidence.RetrievedAt), evidence.HTTPStatus, evidence.ContentSHA256,
		evidence.ExtractionMethod, evidence.RawArtifactID, evidence.TextArtifactID, string(quotes), string(flags), evidence.Excerpt); err != nil {
		return domain.Evidence{}, err
	}
	if _, err := appendEvent(ctx, tx, evidence.TaskID, "evidence.created", nil, "user", map[string]any{
		"evidence_id": evidence.ID, "title": evidence.Title, "canonical_url": evidence.CanonicalURL,
	}); err != nil {
		return domain.Evidence{}, err
	}
	if sessionErr == nil {
		if err := writeResearchCounters(ctx, tx, evidence.TaskID, session.Counters); err != nil {
			return domain.Evidence{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Evidence{}, err
	}
	return evidence, nil
}

func saveResearchArtifact(ctx context.Context, tx *sql.Tx, artifact domain.Artifact, ref string) error {
	if artifact.ID == "" || artifact.TaskID == "" || artifact.SHA256 == "" || artifact.MIMEType == "" || artifact.SizeBytes < 0 || !safeArtifactReference(ref) || ref != filepath.Join(artifact.TaskID, artifact.ID) {
		return errors.New("invalid research artifact metadata or owned storage reference")
	}
	var taskID, path, hash, mime string
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT task_id,storage_path,sha256,mime_type,size_bytes FROM artifacts WHERE id=?`, artifact.ID).Scan(&taskID, &path, &hash, &mime, &size)
	if err == nil {
		if taskID != artifact.TaskID || path != ref || hash != artifact.SHA256 || mime != artifact.MIMEType || size != artifact.SizeBytes {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id,task_id,name,mime_type,sha256,size_bytes,storage_path,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		artifact.ID, artifact.TaskID, artifact.Name, artifact.MIMEType, artifact.SHA256, artifact.SizeBytes, ref, stamp(artifact.CreatedAt)); err != nil {
		return err
	}
	_, err = appendEvent(ctx, tx, artifact.TaskID, "artifact.created", nil, "user", map[string]any{
		"artifact_id": artifact.ID, "name": artifact.Name, "mime_type": artifact.MIMEType, "size_bytes": artifact.SizeBytes, "sha256": artifact.SHA256,
	})
	return err
}

func researchSourceEvidence(ctx context.Context, tx *sql.Tx, key domain.Evidence) (domain.Evidence, error) {
	var item domain.Evidence
	var retrieved, quotes, flags string
	err := tx.QueryRowContext(ctx, `SELECT id,task_id,provider,original_query,original_url,canonical_url,title,language,retrieved_at,http_status,content_sha256,extraction_method,COALESCE(raw_artifact_id,''),COALESCE(text_artifact_id,''),quote_spans_json,quality_flags_json,excerpt FROM evidence WHERE task_id=? AND canonical_url=? AND content_sha256=?`, key.TaskID, key.CanonicalURL, key.ContentSHA256).
		Scan(&item.ID, &item.TaskID, &item.Provider, &item.OriginalQuery, &item.OriginalURL, &item.CanonicalURL, &item.Title, &item.Language, &retrieved, &item.HTTPStatus, &item.ContentSHA256, &item.ExtractionMethod, &item.RawArtifactID, &item.TextArtifactID, &quotes, &flags, &item.Excerpt)
	if err != nil {
		return domain.Evidence{}, err
	}
	if err := json.Unmarshal([]byte(quotes), &item.QuoteSpans); err != nil {
		return domain.Evidence{}, err
	}
	if err := json.Unmarshal([]byte(flags), &item.QualityFlags); err != nil {
		return domain.Evidence{}, err
	}
	item.RetrievedAt, item.Taint = parseTime(retrieved), "untrusted_external_content"
	return item, nil
}
