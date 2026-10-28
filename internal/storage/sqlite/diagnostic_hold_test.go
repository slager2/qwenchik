package sqlite

import (
	"context"
	"github.com/slager2/qwenchik/internal/domain"
	"testing"
	"time"
)

func TestDiagnosticResourceHoldExcludesAutomaticProbesAndRetainsCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	if _, err := s.SaveCheckpoint(ctx, turn.Run.ID, 1, map[string]any{"retained": "evidence and frontier"}); err != nil {
		t.Fatal(err)
	}
	paused, err := s.PauseTaskForResource(WithDiagnosticResourceHold(ctx), turn.Run.ID, "high_model_rss", time.Now().Add(-time.Second), nil)
	if err != nil || paused.Status != domain.StatusPaused || paused.PauseReason != "manual" {
		t.Fatalf("hold=%+v %v", paused, err)
	}
	before, err := s.LatestCheckpoint(ctx, turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{time.Now(), time.Now().Add(time.Hour), time.Now().Add(24 * time.Hour)} {
		due, err := s.ResourcePausedTasks(ctx, at, 10)
		if err != nil || len(due) != 0 {
			t.Fatal("held experiment entered recovery queue", err)
		}
	}
	after, _ := s.LatestCheckpoint(ctx, turn.Run.ID)
	if after.StateHash != before.StateHash {
		t.Fatal("held checkpoint mutated")
	}
}
