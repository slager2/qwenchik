package sqlite

import (
	"context"

	"github.com/slager2/qwenchik/internal/domain"
)

// A report is not another evidence record. Reaching MaxEvidence must not
// prevent committing the bounded deliverable for those already accepted sources.
func (s *Store) SaveResearchReportArtifact(ctx context.Context, artifact domain.Artifact, ref string) (domain.Artifact, error) {
	if artifact.SizeBytes < 0 || artifact.SizeBytes > 1<<20 {
		return domain.Artifact{}, ErrCheckpointInvalid
	}
	if !((artifact.Name == "research_report.json" && artifact.MIMEType == "application/vnd.qwenchik.research-report+json") || (artifact.Name == "research_report.md" && artifact.MIMEType == "text/markdown; charset=utf-8")) {
		return domain.Artifact{}, ErrCheckpointInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Artifact{}, err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, artifact.TaskID, true); err != nil {
		return domain.Artifact{}, err
	}
	task, err := getTask(ctx, tx, artifact.TaskID)
	if err != nil {
		return domain.Artifact{}, err
	}
	if task.Status != domain.StatusVerifying {
		return domain.Artifact{}, ErrConflict
	}
	if session, err := researchSession(ctx, tx, artifact.TaskID); err == nil && session.Policy.Version == 2 && artifact.SizeBytes > int64(session.Budget.MaxReportBytes) {
		return domain.Artifact{}, ErrResearchBudgetExceeded
	} else if err != nil && err != ErrNotFound {
		return domain.Artifact{}, err
	}
	if err := saveResearchArtifact(ctx, tx, artifact, ref); err != nil {
		return domain.Artifact{}, err
	}
	if err := fenceExecution(ctx, tx, artifact.TaskID); err != nil {
		return domain.Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Artifact{}, err
	}
	return artifact, nil
}
