package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

var ErrContextSnapshotTooLarge = domain.ErrContextSnapshotTooLarge

var workingSetKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validateWorkingSet(values map[string]string) error {
	if len(values) > 20 {
		return ErrContextSnapshotTooLarge
	}
	size := 0
	for key, value := range values {
		if !workingSetKey.MatchString(key) {
			return fmt.Errorf("invalid working set key %q", key)
		}
		switch key {
		case "version", "id", "number", "hash", "goal", "constraints", "done_criteria", "completed_steps", "accepted_evidence_ids", "evidence", "budget", "budget_remaining", "counters", "policy", "session_id", "task_id", "open_questions", "contradictions", "failed_attempts", "next_action", "summary", "file_changes", "working_set", "context_snapshot", "context_snapshot_hash", "plan_attempt", "pending_action":
			return fmt.Errorf("reserved working set key %q", key)
		case "budgets", "donecriteria", "acceptedevidenceids", "accepted_evidence", "budgetremaining", "llm_calls", "http_attempts", "search_queries", "fetches", "rounds", "steps", "questions_created", "questions_accepted", "stagnant_rounds", "fetched_bytes", "wall_time_seconds", "plan_version", "current_round", "current_phase", "model_snapshot", "research_schema_version", "deadline_at", "questions":
			return fmt.Errorf("reserved working set key %q", key)
		}
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 1024 {
			return ErrContextSnapshotTooLarge
		}
		size += len(key) + len(value)
	}
	if size > 16<<10 {
		return ErrContextSnapshotTooLarge
	}
	return nil
}

func snapshotHash(snapshot domain.ContextSnapshot) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	values := checkpointObject(encoded)
	delete(values, "id")
	delete(values, "hash")
	delete(values, "number")
	encoded, err = json.Marshal(values)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func snapshotID(taskID, hash string) string {
	encoded, _ := json.Marshal([]string{taskID, hash})
	digest := sha256.Sum256(encoded)
	return "context-" + hex.EncodeToString(digest[:])
}

func validateSnapshot(snapshot domain.ContextSnapshot) error {
	if snapshot.Version != 1 {
		return errors.New("unsupported context snapshot version")
	}
	if err := validateWorkingSet(snapshot.WorkingSet); err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(encoded) > 32<<10 {
		return ErrContextSnapshotTooLarge
	}
	return nil
}

func checkpointSnapshot(state any) (domain.ContextSnapshot, bool, error) {
	var raw json.RawMessage
	var exists bool
	if values, ok := state.(map[string]any); ok {
		value, found := values["context_snapshot"]
		exists = found
		if found {
			var err error
			raw, err = json.Marshal(value)
			if err != nil {
				return domain.ContextSnapshot{}, false, err
			}
		}
	} else {
		encoded, err := json.Marshal(state)
		if err != nil {
			return domain.ContextSnapshot{}, false, err
		}
		raw, exists = checkpointObject(encoded)["context_snapshot"]
	}
	if !exists {
		return domain.ContextSnapshot{}, false, nil
	}
	var snapshot domain.ContextSnapshot
	if len(raw) > 32<<10 {
		return snapshot, true, ErrContextSnapshotTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, true, err
	}
	return snapshot, true, validateSnapshot(snapshot)
}

func contextSnapshotTx(ctx context.Context, tx *sql.Tx, taskID string) (domain.ContextSnapshot, []domain.Checkpoint, error) {
	items, err := verifiedCheckpoints(ctx, tx, taskID)
	if err != nil {
		return domain.ContextSnapshot{}, nil, err
	}
	if len(items) == 0 {
		return domain.ContextSnapshot{}, items, ErrNotFound
	}
	snapshot, exists, err := checkpointSnapshot(items[len(items)-1].State)
	if err != nil {
		return snapshot, items, fmt.Errorf("%w: %v", ErrCheckpointInvalid, err)
	}
	if !exists {
		return snapshot, items, ErrNotFound
	}
	return snapshot, items, nil
}

func (s *Store) ContextSnapshot(ctx context.Context, taskID string) (domain.ContextSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ContextSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, _, err := contextSnapshotTx(ctx, tx, taskID)
	return snapshot, err
}

func (s *Store) SaveContextSnapshot(ctx context.Context, taskID string, snapshot domain.ContextSnapshot, reason string) (domain.ContextSnapshot, error) {
	return s.updateContextSnapshot(ctx, taskID, snapshot, reason, nil, false)
}

func (s *Store) PatchWorkingSet(ctx context.Context, taskID string, updates map[string]string) (domain.ContextSnapshot, error) {
	if err := validateWorkingSet(updates); err != nil {
		return domain.ContextSnapshot{}, err
	}
	return s.updateContextSnapshot(ctx, taskID, domain.ContextSnapshot{}, "working_set_updated", updates, true)
}

func (s *Store) updateContextSnapshot(ctx context.Context, taskID string, snapshot domain.ContextSnapshot, reason string, updates map[string]string, patch bool) (domain.ContextSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.ContextSnapshot{}, err
	}
	if len(reason) > 256 || strings.TrimSpace(reason) == "" {
		return domain.ContextSnapshot{}, errors.New("context snapshot reason must contain 1 to 256 bytes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ContextSnapshot{}, err
	}
	defer tx.Rollback()
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return domain.ContextSnapshot{}, err
	}
	task, err := getTask(ctx, tx, taskID)
	if err != nil {
		return domain.ContextSnapshot{}, err
	}
	var cancelled bool
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested FROM tasks WHERE id=?`, taskID).Scan(&cancelled); err != nil {
		return domain.ContextSnapshot{}, err
	}
	if cancelled || (task.Status != domain.StatusPlanning && task.Status != domain.StatusRunning && task.Status != domain.StatusVerifying) {
		return domain.ContextSnapshot{}, ErrConflict
	}
	prior, checkpoints, err := contextSnapshotTx(ctx, tx, taskID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return domain.ContextSnapshot{}, err
	}
	if patch {
		if errors.Is(err, ErrNotFound) {
			return domain.ContextSnapshot{}, ErrNotFound
		}
		snapshot = prior
		if snapshot.WorkingSet == nil {
			snapshot.WorkingSet = make(map[string]string)
		}
		for key, value := range updates {
			snapshot.WorkingSet[key] = value
		}
	}
	if snapshot.Version != 0 && snapshot.Version != 1 {
		return domain.ContextSnapshot{}, errors.New("unsupported context snapshot version")
	}
	snapshot.Version = 1
	snapshot.ID, snapshot.Hash, snapshot.Number = "", "", 0
	if err := validateSnapshot(snapshot); err != nil {
		return domain.ContextSnapshot{}, err
	}
	state := make(map[string]json.RawMessage)
	planVersion := 1
	if len(checkpoints) > 0 {
		last := checkpoints[len(checkpoints)-1]
		var encoded string
		if err := tx.QueryRowContext(ctx, `SELECT state_json FROM checkpoints WHERE id=? AND task_id=?`, last.ID, taskID).Scan(&encoded); err != nil {
			return domain.ContextSnapshot{}, err
		}
		state = checkpointObject([]byte(encoded))
		if state == nil {
			state = make(map[string]json.RawMessage)
		}
		planVersion = last.PlanVersion
	}
	session, err := researchSession(ctx, tx, taskID)
	if err == nil {
		if err := researchReservationAllowed(ctx, tx, session); err != nil {
			return domain.ContextSnapshot{}, err
		}
		if err := reconcileResearchCounters(ctx, tx, &session, state); err != nil {
			return domain.ContextSnapshot{}, err
		}
		snapshot.Goal = session.Goal
		snapshot.BudgetRemaining, err = snapshotBudget(ctx, tx, session)
		if err != nil {
			return domain.ContextSnapshot{}, err
		}
	} else if errors.Is(err, ErrNotFound) {
		snapshot.BudgetRemaining = map[string]int64{}
	} else {
		return domain.ContextSnapshot{}, err
	}
	if err := validateSnapshotEvidence(ctx, tx, taskID, snapshot.AcceptedEvidenceIDs); err != nil {
		return domain.ContextSnapshot{}, err
	}
	snapshot.Hash, err = snapshotHash(snapshot)
	if err != nil {
		return domain.ContextSnapshot{}, err
	}
	snapshot.ID = snapshotID(taskID, snapshot.Hash)
	if snapshot.Hash == prior.Hash {
		if err := fenceExecution(ctx, tx, taskID); err != nil {
			return domain.ContextSnapshot{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ContextSnapshot{}, err
		}
		return prior, nil
	}
	snapshot.Number = prior.Number + 1
	if err := validateSnapshot(snapshot); err != nil {
		return domain.ContextSnapshot{}, err
	}
	encoded, _ := json.Marshal(snapshot)
	previous, _ := json.Marshal(prior)
	kind, visibility := "context.snapshot_saved", "internal"
	contentSize := func(value domain.ContextSnapshot) int {
		value.ID, value.Hash, value.Number, value.BudgetRemaining = "", "", 0, nil
		raw, _ := json.Marshal(value)
		return len(raw)
	}
	if prior.Number > 0 && len(encoded) < len(previous) && contentSize(snapshot) < contentSize(prior) && (reason == "compaction" || reason == "context_compaction" || reason == "context.compacted") {
		kind, visibility = "context.compacted", "user"
	}
	seq, err := appendEvent(ctx, tx, taskID, kind, task.Stage, visibility, map[string]any{
		"snapshot_id": snapshot.ID, "number": snapshot.Number, "hash": snapshot.Hash, "reason": reason,
		"previous_bytes": len(previous), "snapshot_bytes": len(encoded), "completed_steps": len(snapshot.CompletedSteps), "accepted_evidence": len(snapshot.AcceptedEvidenceIDs), "open_questions": len(snapshot.OpenQuestions),
	})
	if err != nil {
		return domain.ContextSnapshot{}, err
	}
	state["context_snapshot"] = encoded
	if _, err := saveCheckpointTx(ctx, tx, taskID, planVersion, seq, state); err != nil {
		return domain.ContextSnapshot{}, err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return domain.ContextSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ContextSnapshot{}, err
	}
	return snapshot, nil
}

func validateSnapshotEvidence(ctx context.Context, tx *sql.Tx, taskID string, ids []string) error {
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			return errors.New("duplicate accepted evidence ID")
		}
		seen[id] = true
		var status int
		var flags, excerpt string
		err := tx.QueryRowContext(ctx, `SELECT http_status,quality_flags_json,excerpt FROM evidence WHERE task_id=? AND id=?`, taskID, id).Scan(&status, &flags, &excerpt)
		var quality []string
		if err != nil || json.Unmarshal([]byte(flags), &quality) != nil || status < 200 || status >= 300 || len(quality) != 0 || strings.TrimSpace(excerpt) == "" {
			return fmt.Errorf("%w: accepted evidence is not an owned successful source", ErrCheckpointInvalid)
		}
	}
	return nil
}

func snapshotBudget(ctx context.Context, tx *sql.Tx, session domain.ResearchSession) (map[string]int64, error) {
	b, c := session.Budget, session.Counters
	var steps int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_steps WHERE task_id=? AND status IN ('completed','skipped','failed')`, session.TaskID).Scan(&steps); err != nil {
		return nil, err
	}
	remaining := map[string]int64{
		"rounds": int64(b.MaxRounds - c.Rounds), "steps": int64(b.MaxSteps) - steps,
		"llm_calls": int64(b.MaxLLMCalls - c.LLMCalls), "http_attempts": int64(b.MaxLLMCalls*2 - c.HTTPAttempts),
		"search_queries": int64(b.MaxSearchQueries - c.SearchQueries), "fetches": int64(b.MaxFetches - c.Fetches),
		"evidence": int64(b.MaxEvidence - c.Evidence), "questions_created": int64(b.MaxQuestionsCreated - c.QuestionsCreated),
		"questions_accepted": int64(b.MaxQuestionsAccepted - c.QuestionsAccepted), "stagnant_rounds": int64(b.MaxStagnantRounds - c.StagnantRounds),
		"fetched_bytes": session.Policy.TaskFetchBudgetBytes - c.FetchedBytes,
	}
	for key, value := range remaining {
		remaining[key] = max(0, value)
	}
	return remaining, nil
}
