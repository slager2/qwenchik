package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestCheckpointCachePublishesOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	first, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"value": "first"})
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := s.checkpointCache[turn.Run.ID]
	if !ok || cached.generation == 0 || len(cached.checkpoints) != 1 {
		t.Fatalf("checkpoint was not cached after commit: %#v", cached)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := appendEvent(ctx, tx, turn.Run.ID, "checkpoint.saved", nil, "internal", map[string]any{"rollback": true})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := s.saveCheckpointTx(ctx, tx, turn.Run.ID, 1, seq, map[string]any{"value": "rolled back"}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	afterRollback := s.checkpointCache[turn.Run.ID]
	if afterRollback.generation != cached.generation || len(afterRollback.checkpoints) != 1 || afterRollback.checkpoints[0].ID != first.ID {
		t.Fatalf("rolled-back checkpoint was published: %#v", afterRollback)
	}

	second, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"value": "second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ParentID != first.ID {
		t.Fatalf("checkpoint chain after rollback: parent=%q want=%q", second.ParentID, first.ID)
	}
}

func TestLatestCheckpointReturnsAnImmutableClone(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{
		"nested": map[string]any{"value": "original"},
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest.State.(map[string]any)["nested"].(map[string]any)["value"] = "mutated by caller"
	again, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.State.(map[string]any)["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("LatestCheckpoint exposed cached state: %v", got)
	}
}

func TestCheckpointCachePublishesTransactionalWriterAfterCommit(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	prior, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"value": "prior"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.BeginInvocation(ctx, invocationFixture(turn.Run.ID, "tool.search", 1))
	if err != nil {
		t.Fatal(err)
	}
	generation, err := checkpointChainGeneration(ctx, s.db, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := s.checkpointCache[turn.Run.ID]
	if !ok || cached.generation != generation || len(cached.checkpoints) != 1 {
		t.Fatalf("transactional checkpoint was not published after commit: %#v generation=%d", cached, generation)
	}
	if cached.checkpoints[0].ID == prior.ID || cached.checkpoints[0].State == nil {
		t.Fatalf("cache still contains prior or incomplete tail: %#v", cached.checkpoints[0])
	}
	latest, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := latest.State.(map[string]any)
	if !ok || state["pending_invocation_id"] != started.ID {
		t.Fatalf("published tail lost invocation state: %#v", latest.State)
	}
}

func benchmarkResearchStore(b *testing.B) (*Store, string) {
	b.Helper()
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "checkpoint-benchmark.db")
	s, err := Open(ctx, path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	c, err := s.CreateConversation(ctx, domain.Conversation{Title: "Checkpoint benchmark", Mode: "research"})
	if err != nil {
		b.Fatal(err)
	}
	policy, budget, model := standardResearchSessionDefaults()
	defaults := domain.ResearchSession{Profile: policy.Profile, Policy: policy, Budget: budget, ModelSnapshot: model}
	defaults.Policy.Profile, defaults.Policy.Budget = defaults.Profile, defaults.Budget
	turn, err := s.SubmitTurnWithResearchDefaults(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Benchmark checkpoint cache"}, defaults, "checkpoint-benchmark")
	if err != nil {
		b.Fatal(err)
	}
	if _, ok, err := s.ClaimNext(ctx); err != nil || !ok {
		b.Fatalf("claim benchmark task: %v %v", ok, err)
	}
	return s, turn.Run.ID
}

func BenchmarkLatestCheckpointCached(b *testing.B) {
	for _, count := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("history_%d", count), func(b *testing.B) {
			ctx := context.Background()
			s, taskID := benchmarkResearchStore(b)
			for i := 0; i < count; i++ {
				if _, err := s.SaveCheckpoint(ctx, taskID, 1, map[string]any{"index": i, "payload": "cache benchmark"}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.LatestCheckpoint(ctx, taskID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
