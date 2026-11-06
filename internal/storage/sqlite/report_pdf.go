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
