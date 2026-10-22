package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func (s *Store) EnsureBaselineBundle(ctx context.Context, bundle domain.AgentBundle) (domain.AgentBundle, error) {
	if err := bundle.Validate(); err != nil {
		return bundle, err
	}
	if bundle.Version != 1 {
		return bundle, ErrConflict
	}
	raw, _ := json.Marshal(bundle)
	now := stamp(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO harness_bundles(hash,base_hash,version,spec_json,status,active,created_at,updated_at) VALUES(?,?,1,?,'approved',1,?,?)`, bundle.BundleHash, bundle.BundleHash, string(raw), now, now)
	if err != nil {
		return bundle, err
	}
	return s.ActiveBundle(ctx, bundle.BundleHash)
}

func (s *Store) ActiveBundle(ctx context.Context, base string) (domain.AgentBundle, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT spec_json FROM harness_bundles WHERE base_hash=? AND active=1 AND status='approved'`, base).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentBundle{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentBundle{}, err
	}
	var bundle domain.AgentBundle
	if json.Unmarshal([]byte(raw), &bundle) != nil || bundle.Validate() != nil || bundle.BaseHash() != base {
		return bundle, ErrCheckpointInvalid
	}
	return bundle, nil
}

func (s *Store) StartRefinement(ctx context.Context, model domain.ModelProfileSnapshot, sources []string, key string) (domain.Task, error) {
	if err := validateModelProfileSnapshot(model); err != nil {
		return domain.Task{}, err
	}
	if model.Bundle == nil || model.Bundle.Validate() != nil || len(sources) == 0 || len(sources) > 8 || key == "" || len(key) > 128 {
		return domain.Task{}, ErrConflict
	}
	sources = append([]string{}, sources...)
	sort.Strings(sources)
	for index := 1; index < len(sources); index++ {
		if sources[index] == sources[index-1] {
			return domain.Task{}, ErrConflict
		}
	}
	fingerprintRaw, _ := json.Marshal(sources)
	fingerprint := roleInputHash(fingerprintRaw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	var id, operation, prior string
	err = tx.QueryRowContext(ctx, `SELECT resource_id,operation,COALESCE(request_sha256,'') FROM idempotency_keys WHERE key=?`, key).Scan(&id, &operation, &prior)
	if err == nil {
		if operation != "refinement.create" || prior != fingerprint {
			return domain.Task{}, ErrIdempotency
		}
		return getTask(ctx, tx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, err
	}
	for _, source := range sources {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=? AND status IN ('completed','failed','cancelled'))`, source).Scan(&exists); err != nil {
			return domain.Task{}, err
		}
		if !exists {
			return domain.Task{}, ErrConflict
		}
	}
	now := time.Now().UTC()
	id = newID()
	deadline := now.Add(5 * time.Minute)
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,title,description,status,mode,priority,version,created_at,updated_at,deadline_at) VALUES(?,'Harness refinement','Propose and evaluate bounded actor/finalizer hints without changing policy, tools or hard budgets.','queued','refinement',0,1,?,?,?)`, id, stamp(now), stamp(now), stamp(now.Add(20*time.Minute))); err != nil {
		return domain.Task{}, err
	}
	seq, err := appendEvent(ctx, tx, id, "task.created", nil, "user", map[string]any{"status": "queued", "kind": "harness_refinement"})
	if err != nil {
		return domain.Task{}, err
	}
	job := domain.RefinementJob{Base: *model.Bundle, Model: model, SourceTaskIDs: sources, DeadlineAt: deadline}
	if _, err := saveCheckpointTx(ctx, tx, id, 1, seq, map[string]any{"refinement_job": job, "completed_steps": []string{}}); err != nil {
		return domain.Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(key,operation,resource_id,created_at,request_sha256) VALUES(?,'refinement.create',?,?,?)`, key, id, stamp(now), fingerprint); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) ReserveRefinementHTTP(ctx context.Context, taskID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=? AND type='refinement.http_reserved'`, taskID).Scan(&count); err != nil {
		return err
	}
	if count >= 20 {
		return ErrResearchBudgetExceeded
	}
	state, version, err := invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return err
	}
	seq, err := appendEvent(ctx, tx, taskID, "refinement.http_reserved", nil, "internal", map[string]any{"attempt": count + 1, "maximum": 20})
	if err != nil {
		return err
	}
	if _, err := saveCheckpointTx(ctx, tx, taskID, version, seq, state); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RefinementRoleLatency(ctx context.Context, taskID, role string) (int64, error) {
	var kind, raw string
	err := s.db.QueryRowContext(ctx, `SELECT type,payload_json FROM events WHERE task_id=? AND type IN ('invocation.completed','invocation.failed') AND json_valid(payload_json) AND json_extract(payload_json,'$.invocation.role')=? ORDER BY seq DESC LIMIT 1`, taskID, role).Scan(&kind, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	invocation, err := decodeInvocationEvent(kind, raw)
	if err != nil {
		return 0, err
	}
	if invocation.StartedAt == nil || invocation.FinishedAt == nil {
		return 0, ErrCheckpointInvalid
	}
	return max(int64(0), invocation.FinishedAt.Sub(*invocation.StartedAt).Milliseconds()), nil
}

func (s *Store) RefinementJob(ctx context.Context, id string) (domain.RefinementJob, error) {
	var mode string
	if err := s.db.QueryRowContext(ctx, `SELECT mode FROM tasks WHERE id=?`, id).Scan(&mode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RefinementJob{}, ErrNotFound
		}
		return domain.RefinementJob{}, err
	}
	if mode != "refinement" {
		return domain.RefinementJob{}, ErrNotFound
	}
	cp, err := s.LatestCheckpoint(ctx, id)
	if err != nil {
		return domain.RefinementJob{}, err
	}
	raw, _ := json.Marshal(cp.State)
	var state struct {
		Job *domain.RefinementJob `json:"refinement_job"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return domain.RefinementJob{}, ErrCheckpointInvalid
	}
	if state.Job == nil {
		return domain.RefinementJob{}, ErrCheckpointInvalid
	}
	return *state.Job, nil
}

func (s *Store) FailureClusters(ctx context.Context, sources []string) (map[string]int, error) {
	result := map[string]int{}
	for _, id := range sources {
		rows, err := s.db.QueryContext(ctx, `SELECT type,payload_json FROM events WHERE task_id=? AND type IN ('task.failed','verification.updated') ORDER BY seq DESC LIMIT 32`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var kind, raw string
			if err := rows.Scan(&kind, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			var value struct {
				Code   string `json:"error_code"`
				Reason string `json:"reason_code"`
				Status string `json:"status"`
			}
			if json.Unmarshal([]byte(raw), &value) != nil {
				rows.Close()
				return nil, ErrCheckpointInvalid
			}
			code := value.Code
			if kind == "verification.updated" && value.Status == "fallback_excerpts" {
				code = value.Reason
			}
			if invocationName.MatchString(code) && len(code) <= 64 {
				result[code]++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

func readBundleCandidate(ctx context.Context, q conversationQuery, hash string) (domain.BundleCandidate, error) {
	var result domain.BundleCandidate
	var raw string
	var patch, evaluation sql.NullString
	err := q.QueryRowContext(ctx, `SELECT spec_json,parent_hash,COALESCE(task_id,''),status,active,patch_json,evaluation_json FROM harness_bundles WHERE hash=?`, hash).Scan(&raw, &result.ParentHash, &result.TaskID, &result.Status, &result.Active, &patch, &evaluation)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if json.Unmarshal([]byte(raw), &result.Bundle) != nil || result.Bundle.Validate() != nil || result.Bundle.BundleHash != hash {
		return result, ErrCheckpointInvalid
	}
	if patch.Valid {
		result.Patch = &domain.CandidatePatch{}
		if json.Unmarshal([]byte(patch.String), result.Patch) != nil {
			return result, ErrCheckpointInvalid
		}
	}
	if evaluation.Valid {
		result.Evaluation = &domain.RefinementEvaluation{}
		if json.Unmarshal([]byte(evaluation.String), result.Evaluation) != nil {
			return result, ErrCheckpointInvalid
		}
	}
	return result, nil
}

func (s *Store) BundleCandidate(ctx context.Context, hash string) (domain.BundleCandidate, error) {
	return readBundleCandidate(ctx, s.db, hash)
}

func (s *Store) SaveCandidate(ctx context.Context, taskID string, job domain.RefinementJob, patch domain.CandidatePatch) (domain.BundleCandidate, error) {
	if err := patch.Validate(); err != nil {
		return domain.BundleCandidate{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.BundleCandidate{}, err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return domain.BundleCandidate{}, err
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT hash FROM harness_bundles WHERE task_id=?`, taskID).Scan(&prior)
	if err == nil {
		return readBundleCandidate(ctx, tx, prior)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.BundleCandidate{}, err
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM harness_bundles WHERE base_hash=?`, job.Base.BaseHash()).Scan(&version); err != nil {
		return domain.BundleCandidate{}, err
	}
	bundle := job.Base
	bundle.Version = version
	bundle.BaseBundleHash = job.Base.BaseHash()
	bundle.BasePromptHash = job.Base.PromptHash
	if job.Base.BasePromptHash != "" {
		bundle.BasePromptHash = job.Base.BasePromptHash
	}
	bundle.Hints = &patch.Hints
	bundle.PromptHash = domain.EffectivePromptHash(bundle.BasePromptHash, patch.Hints)
	bundle.BundleHash = bundle.Hash()
	if err := bundle.Validate(); err != nil {
		return domain.BundleCandidate{}, err
	}
	spec, _ := json.Marshal(bundle)
	proposal, _ := json.Marshal(patch)
	now := stamp(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `INSERT INTO harness_bundles(hash,base_hash,version,parent_hash,spec_json,patch_json,task_id,status,active,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'evaluating',0,?,?)`, bundle.BundleHash, bundle.BaseBundleHash, version, job.Base.BundleHash, string(spec), string(proposal), taskID, now, now); err != nil {
		return domain.BundleCandidate{}, err
	}
	state, planVersion, err := invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return domain.BundleCandidate{}, err
	}
	job.CandidateHash = bundle.BundleHash
	state["refinement_job"] = job
	seq, err := appendEvent(ctx, tx, taskID, "refinement.candidate_created", nil, "internal", map[string]any{"bundle_hash": bundle.BundleHash, "parent_hash": job.Base.BundleHash})
	if err != nil {
		return domain.BundleCandidate{}, err
	}
	if _, err := saveCheckpointTx(ctx, tx, taskID, planVersion, seq, state); err != nil {
		return domain.BundleCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.BundleCandidate{}, err
	}
	return s.BundleCandidate(ctx, bundle.BundleHash)
}

func (s *Store) SaveRefinementEvaluation(ctx context.Context, taskID, hash string, evaluation domain.RefinementEvaluation) error {
	if evaluation.Version != "heldout-v1" || evaluation.CaseCount != 4 || evaluation.BaselinePassed < 0 || evaluation.BaselinePassed > 4 || evaluation.CandidatePassed < 0 || evaluation.CandidatePassed > 4 || evaluation.BaselineMS < 0 || evaluation.CandidateMS < 0 {
		return ErrConflict
	}
	evaluation.Passed = evaluation.CandidatePassed == 4 && evaluation.CandidatePassed >= evaluation.BaselinePassed && evaluation.CandidateMS <= evaluation.BaselineMS*2+2000
	status := "rejected"
	if evaluation.Passed {
		status = "ready"
	}
	raw, _ := json.Marshal(evaluation)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE harness_bundles SET evaluation_json=?,status=?,updated_at=? WHERE hash=? AND task_id=? AND status='evaluating'`, string(raw), status, stamp(time.Now().UTC()), hash, taskID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	if _, err := appendEvent(ctx, tx, taskID, "refinement.evaluated", nil, "user", map[string]any{"bundle_hash": hash, "status": status, "evaluation": evaluation}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PromoteRefinement(ctx context.Context, taskID, hash string) (domain.AgentBundle, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentBundle{}, err
	}
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return domain.AgentBundle{}, err
	}
	candidate, err := readBundleCandidate(ctx, tx, hash)
	if err != nil {
		return domain.AgentBundle{}, err
	}
	if candidate.TaskID != taskID {
		return domain.AgentBundle{}, ErrConflict
	}
	if candidate.Status == "approved" && candidate.Active {
		return candidate.Bundle, nil
	}
	if candidate.Status != "ready" || candidate.Evaluation == nil || !candidate.Evaluation.Passed || candidate.Evaluation.Version != "heldout-v1" || candidate.Evaluation.CaseCount != 4 || candidate.Evaluation.CandidatePassed != 4 || candidate.Evaluation.CandidatePassed < candidate.Evaluation.BaselinePassed || candidate.Evaluation.CandidateMS > candidate.Evaluation.BaselineMS*2+2000 {
		return domain.AgentBundle{}, ErrConflict
	}
	arguments, _ := json.Marshal(map[string]string{"bundle_hash": hash})
	var approval string
	err = tx.QueryRowContext(ctx, `SELECT id FROM approvals WHERE task_id=? AND tool_name='harness.promote_bundle' AND arguments_json=? AND status='approved' AND consumed_at IS NULL AND expires_at>? ORDER BY created_at DESC LIMIT 1`, taskID, string(arguments), stamp(time.Now().UTC())).Scan(&approval)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentBundle{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentBundle{}, err
	}
	var active string
	if err := tx.QueryRowContext(ctx, `SELECT hash FROM harness_bundles WHERE base_hash=? AND active=1`, candidate.Bundle.BaseHash()).Scan(&active); err != nil {
		return domain.AgentBundle{}, err
	}
	if active != candidate.ParentHash {
		return domain.AgentBundle{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_bundles SET active=0 WHERE base_hash=?`, candidate.Bundle.BaseHash()); err != nil {
		return domain.AgentBundle{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_bundles SET status='approved',active=1,updated_at=? WHERE hash=?`, stamp(time.Now().UTC()), hash); err != nil {
		return domain.AgentBundle{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE approvals SET consumed_at=? WHERE id=?`, stamp(time.Now().UTC()), approval); err != nil {
		return domain.AgentBundle{}, err
	}
	if _, err := appendEvent(ctx, tx, taskID, "refinement.promoted", nil, "user", map[string]any{"bundle_hash": hash, "version": candidate.Bundle.Version}); err != nil {
		return domain.AgentBundle{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentBundle{}, err
	}
	return candidate.Bundle, nil
}

func (s *Store) RollbackBundle(ctx context.Context, base, hash string) (domain.AgentBundle, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentBundle{}, err
	}
	defer tx.Rollback()
	candidate, err := readBundleCandidate(ctx, tx, hash)
	if err != nil {
		return domain.AgentBundle{}, err
	}
	if candidate.Status != "approved" || candidate.Bundle.BaseHash() != base {
		return domain.AgentBundle{}, ErrConflict
	}
	var auditTask sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM harness_bundles WHERE base_hash=? AND active=1`, base).Scan(&auditTask); err != nil {
		return domain.AgentBundle{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_bundles SET active=0 WHERE base_hash=?`, base); err != nil {
		return domain.AgentBundle{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_bundles SET active=1,updated_at=? WHERE hash=?`, stamp(time.Now().UTC()), hash); err != nil {
		return domain.AgentBundle{}, err
	}
	if auditTask.Valid {
		if _, err := appendEvent(ctx, tx, auditTask.String, "refinement.rollback", nil, "user", map[string]any{"bundle_hash": hash, "version": candidate.Bundle.Version}); err != nil {
			return domain.AgentBundle{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentBundle{}, err
	}
	return candidate.Bundle, nil
}
