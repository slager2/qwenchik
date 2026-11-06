package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var reportPDFName = regexp.MustCompile(`^research_report-[0-9a-f]{16}\.pdf$`)

const maxReportPDFBytes = 64 << 20

// SaveReportPDFArtifact records a rendered PDF of a finished research run's
// report. The PDF is a derived cache of the report, not research output:
// it is recorded without an event so terminal runs keep their event log.
func (s *Store) SaveReportPDFArtifact(ctx context.Context, artifact domain.Artifact, ref string) (domain.Artifact, error) {
	if !reportPDFName.MatchString(artifact.Name) || artifact.MIMEType != "application/pdf" || artifact.SizeBytes <= 0 || artifact.SizeBytes > maxReportPDFBytes ||
		artifact.ID == "" || artifact.TaskID == "" || artifact.SHA256 == "" || !safeArtifactReference(ref) || ref != filepath.Join(artifact.TaskID, artifact.ID) {
		return domain.Artifact{}, ErrCheckpointInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Artifact{}, err
	}
	defer tx.Rollback()
	var mode, status string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(mode,''),status FROM tasks WHERE id=?`, artifact.TaskID).Scan(&mode, &status); errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, ErrNotFound
	} else if err != nil {
		return domain.Artifact{}, err
	}
	if mode != "research" || !domain.TaskStatus(status).Terminal() {
		return domain.Artifact{}, ErrConflict
	}
	var taskID, path, hash, mime string
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT task_id,storage_path,sha256,mime_type,size_bytes FROM artifacts WHERE id=?`, artifact.ID).Scan(&taskID, &path, &hash, &mime, &size)
	if err == nil {
		if taskID != artifact.TaskID || path != ref || hash != artifact.SHA256 || mime != artifact.MIMEType || size != artifact.SizeBytes {
			return domain.Artifact{}, ErrConflict
		}
		return artifact, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, err
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id,task_id,name,mime_type,sha256,size_bytes,storage_path,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		artifact.ID, artifact.TaskID, artifact.Name, artifact.MIMEType, artifact.SHA256, artifact.SizeBytes, ref, stamp(artifact.CreatedAt)); err != nil {
		return domain.Artifact{}, err
	}
	return artifact, tx.Commit()
}

// Report PDF auto-save outcome events (internal visibility).
const (
	EventReportPDFAutosaved = "report_pdf.autosaved"
	EventReportPDFSkipped   = "report_pdf.autosave_skipped"
	EventReportPDFFailed    = "report_pdf.autosave_failed"
)

// PendingReportPDFAutosaves lists research runs completed at or after since
// that have no recorded auto-save outcome, oldest first.
func (s *Store) PendingReportPDFAutosaves(ctx context.Context, since time.Time, limit int) ([]domain.ReportPDFCandidate, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.title,COALESCE(rs.profile,''),COALESCE(t.started_at,''),COALESCE(t.completed_at,''),e.created_at,
		COALESCE((SELECT a.size_bytes FROM artifacts a WHERE a.task_id=t.id AND a.name='research_report.md' ORDER BY a.created_at DESC LIMIT 1),0)
		FROM events e JOIN tasks t ON t.id=e.task_id LEFT JOIN research_sessions rs ON rs.task_id=t.id
		WHERE e.type='task.completed' AND e.created_at>=? AND t.mode='research' AND t.status='completed'
		AND NOT EXISTS(SELECT 1 FROM events m WHERE m.task_id=t.id AND m.type IN (?,?,?))
		ORDER BY e.created_at,e.seq LIMIT ?`, stamp(since), EventReportPDFAutosaved, EventReportPDFSkipped, EventReportPDFFailed, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReportPDFCandidate
	seen := map[string]bool{}
	for rows.Next() {
		var item domain.ReportPDFCandidate
		var started, completed, event string
		if err := rows.Scan(&item.TaskID, &item.Title, &item.Profile, &started, &completed, &event, &item.ReportMarkdownBytes); err != nil {
			return nil, err
		}
		if seen[item.TaskID] {
			continue
		}
		seen[item.TaskID] = true
		item.StartedAt, item.CompletedAt = parseTime(started), parseTime(completed)
		if item.CompletedAt.IsZero() {
			item.CompletedAt = parseTime(event)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// RecordReportPDFAutosave records the auto-save outcome for a run so it is
// not considered again.
func (s *Store) RecordReportPDFAutosave(ctx context.Context, taskID, eventType string, payload map[string]any) error {
	switch eventType {
	case EventReportPDFAutosaved, EventReportPDFSkipped, EventReportPDFFailed:
	default:
		return ErrConflict
	}
	if payload == nil {
		payload = map[string]any{}
	}
	_, err := s.RecordEvent(ctx, taskID, eventType, nil, "internal", payload)
	return err
}
