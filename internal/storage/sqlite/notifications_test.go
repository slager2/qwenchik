package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func notificationFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	ctx := context.Background()
	s, turn, path := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	if _, ok, err := s.ClaimNotification(ctx, "desktop"); err != nil || ok {
		t.Fatalf("uncommitted notification: %t %v", ok, err)
	}
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusFailed, nil, "task.failed", "user", map[string]any{"error_code": "research_budget_exceeded", "message": "PRIVATE_PROMPT_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	return s, turn.Run.ID, path
}

func TestNotificationOutboxClaimsOnceAndPersistsWithoutChangingTaskOutcome(t *testing.T) {
	ctx := context.Background()
	s, id, path := notificationFixture(t)
	var wg sync.WaitGroup
	deliveries := make(chan domain.NotificationDelivery, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, ok, err := s.ClaimNotification(ctx, "desktop")
			if err != nil {
				t.Error(err)
			}
			if ok {
				deliveries <- d
			}
		}()
	}
	wg.Wait()
	close(deliveries)
	claimed := []domain.NotificationDelivery{}
	for d := range deliveries {
		claimed = append(claimed, d)
	}
	if len(claimed) != 1 || claimed[0].Outcome != "budget_exhausted" {
		t.Fatalf("claims=%+v", claimed)
	}
	d := claimed[0]
	encoded, _ := json.Marshal(d)
	if strings.Contains(string(encoded), "PRIVATE_PROMPT_TOKEN") {
		t.Fatal("prompt leaked to delivery")
	}
	if err := s.FinishNotification(ctx, d, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimNotification(ctx, "desktop"); err != nil || ok {
		t.Fatalf("busy retry: %t %v", ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for attempt := 2; attempt <= 3; attempt++ {
		d, ok, err := reopened.claimNotificationAt(ctx, "desktop", time.Now().Add(time.Hour))
		if err != nil || !ok || d.Attempt != attempt {
			t.Fatalf("retry %d: %+v %t %v", attempt, d, ok, err)
		}
		if err := reopened.FinishNotification(ctx, d, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := reopened.claimNotificationAt(ctx, "desktop", time.Now().Add(2*time.Hour)); err != nil || ok {
		t.Fatalf("unbounded retry: %t %v", ok, err)
	}
	task, err := reopened.GetTask(ctx, id)
	if err != nil || task.Status != domain.StatusFailed || task.TerminalOutcome != "budget_exhausted" {
		t.Fatalf("outcome changed: %+v %v", task, err)
	}
}

func TestNotificationExpiredClaimFencesLateAcknowledgement(t *testing.T) {
	ctx := context.Background()
	s, _, _ := notificationFixture(t)
	first, ok, err := s.ClaimNotification(ctx, "desktop")
	if err != nil || !ok {
		t.Fatal(err)
	}
	second, ok, err := s.claimNotificationAt(ctx, "desktop", time.Now().Add(time.Minute))
	if err != nil || !ok || second.Attempt != 2 {
		t.Fatalf("claim recovery: %+v %v", second, err)
	}
	if err := s.FinishNotification(ctx, first, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("late acknowledgement accepted: %v", err)
	}
	if err := s.FinishNotification(ctx, second, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimNotification(ctx, "ntfy"); err != nil || ok {
		t.Fatalf("duplicate after channel switch: %t %v", ok, err)
	}
}

func TestTerminalRollbackCannotEnqueueNotification(t *testing.T) {
	ctx := context.Background()
	s, turn, _ := researchConversationFixture(t, nil)
	claimResearchFixture(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_terminal BEFORE INSERT ON events WHEN NEW.type='task.failed' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, turn.Run.ID, domain.StatusFailed, nil, "task.failed", "user", nil); err == nil {
		t.Fatal("terminal escaped rollback")
	}
	if _, ok, err := s.ClaimNotification(ctx, "desktop"); err != nil || ok {
		t.Fatalf("rollback enqueued: %t %v", ok, err)
	}
}
