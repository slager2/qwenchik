package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func refinementStoreFixture(t *testing.T) (*Store, domain.Task, domain.AgentBundle) {
	t.Helper()
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusFailed, nil, "task.failed", "user", nil); err != nil {
		t.Fatal(err)
	}
	bundle := domain.AgentBundle{BundleID: "research-default", Version: 1, ModelProfile: "local-qwen", PromptHash: strings.Repeat("a", 64), RoleSchemaVersion: 1, EnabledTools: []string{"text.inspect"}, MemoryPolicyVersion: 1, BudgetProfile: "snapshotted-research-v1", EvaluatorVersion: "baseline-v1"}
	bundle.BundleHash = bundle.Hash()
	if _, err := s.EnsureBaselineBundle(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	model := domain.ModelProfileSnapshot{Bundle: &bundle, Version: 1, Model: "local-qwen", BaseURL: "http://127.0.0.1:8000/v1", ContextLength: 8192, MaxOutputTokens: 512, TopP: 0.8, PromptVersion: "local-research-v1", SchemaVersion: 1}
	task, err := s.StartRefinement(ctx, model, []string{turn.Run.ID}, "store-refinement")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimNext(ctx)
	if err != nil || !ok || claimed.ID != task.ID {
		t.Fatal(err)
	}
	return s, claimed, bundle
}

func TestRefinementBudgetsAndImmutableJobSurviveCheckpointWrites(t *testing.T) {
	ctx := context.Background()
	s, task, _ := refinementStoreFixture(t)
	before, err := s.RefinementJob(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpoint(ctx, task.ID, 1, map[string]any{"partial": "resource pause fixture"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.RefinementJob(ctx, task.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("job lost: %+v %v", after, err)
	}
	forged := after
	forged.SourceTaskIDs = []string{"foreign"}
	if _, err := s.SaveCheckpoint(ctx, task.ID, 1, map[string]any{"refinement_job": forged}); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("job provenance changed: %v", err)
	}
	for index := 0; index < 20; index++ {
		if err := s.ReserveRefinementHTTP(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReserveRefinementHTTP(ctx, task.ID); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("HTTP ceiling escaped: %v", err)
	}
	for index := 0; index < 10; index++ {
		invocation := invocationFixture(task.ID, "llm.complete", 1)
		invocation.Role = "refinement_eval"
		invocation.OperationID = string(rune('a' + index))
		invocation.IdempotencyKey = invocation.OperationID
		if _, err := s.BeginInvocation(ctx, invocation); err != nil {
			t.Fatal(err)
		}
	}
	invocation := invocationFixture(task.ID, "llm.complete", 1)
	invocation.Role = "refinement_eval"
	invocation.OperationID = "eleventh"
	invocation.IdempotencyKey = "eleventh"
	if _, err := s.BeginInvocation(ctx, invocation); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("model ceiling escaped: %v", err)
	}
	var sequence int
	var name, path string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ReserveRefinementHTTP(ctx, task.ID); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("HTTP ceiling reset after restart: %v", err)
	}
	if _, err := reopened.BeginInvocation(ctx, invocation); !errors.Is(err, ErrResearchBudgetExceeded) {
		t.Fatalf("model ceiling reset after restart: %v", err)
	}
}

func TestRefinementPromotionRollsBackAtomicallyAndRequiresApproval(t *testing.T) {
	ctx := context.Background()
	s, task, base := refinementStoreFixture(t)
	if _, err := s.TransitionTask(ctx, task.ID, domain.StatusRunning, nil, "task.status_changed", "user", nil); err != nil {
		t.Fatal(err)
	}
	job, err := s.RefinementJob(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := s.SaveCandidate(ctx, task.ID, job, domain.CandidatePatch{Hints: domain.RoleHints{Actor: "Use distinct focused queries."}, Rationale: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	evaluation := domain.RefinementEvaluation{Version: "heldout-v1", CaseCount: 4, BaselinePassed: 4, CandidatePassed: 4, BaselineMS: 100, CandidateMS: 100, Passed: true}
	if err := s.SaveRefinementEvaluation(ctx, task.ID, candidate.Bundle.BundleHash, evaluation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteRefinement(ctx, task.ID, candidate.Bundle.BundleHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing approval bypassed: %v", err)
	}
	args, _ := json.Marshal(map[string]string{"bundle_hash": candidate.Bundle.BundleHash})
	cp, _ := s.LatestCheckpoint(ctx, task.ID)
	approval, err := s.CreateApproval(ctx, domain.Approval{TaskID: task.ID, ToolName: "harness.promote_bundle", Arguments: args, Target: candidate.Bundle.BundleHash, RiskExplanation: "Fixture explicit approval of bounded operational hints"}, cp.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideApproval(ctx, approval.ID, true, "user", "approve-candidate"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimNext(ctx); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_promotion BEFORE INSERT ON events WHEN NEW.type='refinement.promoted' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteRefinement(ctx, task.ID, candidate.Bundle.BundleHash); err == nil {
		t.Fatal("promotion escaped rollback")
	}
	active, err := s.ActiveBundle(ctx, base.BundleHash)
	if err != nil || active.BundleHash != base.BundleHash {
		t.Fatal("active bundle changed despite rollback")
	}
	var consumed bool
	if err := s.db.QueryRow(`SELECT consumed_at IS NOT NULL FROM approvals WHERE id=?`, approval.ID).Scan(&consumed); err != nil || consumed {
		t.Fatal("approval consumption escaped rollback")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_promotion`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteRefinement(ctx, task.ID, candidate.Bundle.BundleHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RollbackBundle(ctx, base.BundleHash, base.BundleHash); err != nil {
		t.Fatal(err)
	}
	active, err = s.ActiveBundle(ctx, base.BundleHash)
	if err != nil || active.BundleHash != base.BundleHash {
		t.Fatal("rollback did not restore baseline")
	}
}

func TestMigration011AddsOnlyBundleStateWithoutBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pre-refinement.db")
	db := researchLegacyDatabase(t, path, 10)
	seedResearchMigrationLegacy(t, db)
	before := researchMigrationDataSnapshot(t, db)
	fts := ftsSchemaSnapshot(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !reflect.DeepEqual(before, researchMigrationDataSnapshot(t, s.db)) || fts != ftsSchemaSnapshot(t, s.db) {
		t.Fatal("migration changed legacy data or FTS")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM harness_bundles`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected bundle backfill: %d %v", count, err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
