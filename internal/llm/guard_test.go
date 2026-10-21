package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type guardedFixture struct {
	started     chan struct{}
	closed      chan struct{}
	active      atomic.Int32
	maximum     atomic.Int32
	completeErr error
	streamErr   error
}

func (f *guardedFixture) enter() {
	n := f.active.Add(1)
	for {
		old := f.maximum.Load()
		if n <= old || f.maximum.CompareAndSwap(old, n) {
			break
		}
	}
}
func (f *guardedFixture) Complete(ctx context.Context, _ ChatRequest) (ChatResponse, error) {
	f.enter()
	defer f.active.Add(-1)
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.closed != nil {
		select {
		case <-f.closed:
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		}
	}
	return ChatResponse{Content: `{"ok":true}`}, f.completeErr
}
func (f *guardedFixture) Stream(ctx context.Context, _ ChatRequest) (<-chan ChatEvent, error) {
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	f.enter()
	events := make(chan ChatEvent)
	go func() {
		defer close(events)
		defer f.active.Add(-1)
		if f.started != nil {
			f.started <- struct{}{}
		}
		select {
		case events <- ChatEvent{Text: "partial"}:
		case <-ctx.Done():
			return
		}
		select {
		case <-f.closed:
		case <-ctx.Done():
		}
	}()
	return events, nil
}

func TestGuardSerializesCompleteAndRejectsBusyProbe(t *testing.T) {
	f := &guardedFixture{started: make(chan struct{}, 1), closed: make(chan struct{})}
	g := NewGuardedClient(f)
	done := make(chan error, 1)
	go func() { _, e := g.Complete(context.Background(), ChatRequest{}); done <- e }()
	<-f.started
	if e := ProbeStructuredOutput(context.Background(), g); !errors.Is(e, ErrInferenceBusy) {
		t.Fatalf("busy probe %v", e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, e := g.Complete(ctx, ChatRequest{}); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("waiting cancellation %v", e)
	}
	close(f.closed)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := ProbeStructuredOutput(context.Background(), g); e != nil {
		t.Fatal(e)
	}
	if f.maximum.Load() != 1 {
		t.Fatal("concurrent inference")
	}
}

func TestGuardStreamHoldsSlotUntilUpstreamClosesAndReleasesOnCancel(t *testing.T) {
	for _, cancelStream := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "cancel"}[cancelStream], func(t *testing.T) {
			f := &guardedFixture{started: make(chan struct{}, 1), closed: make(chan struct{})}
			g := NewGuardedClient(f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, e := g.Stream(ctx, ChatRequest{})
			if e != nil {
				t.Fatal(e)
			}
			<-f.started
			if event := <-stream; event.Text != "partial" {
				t.Fatal(event)
			}
			if _, e = g.TryComplete(context.Background(), ChatRequest{}); !errors.Is(e, ErrInferenceBusy) {
				t.Fatalf("slot released at Stream return: %v", e)
			}
			if cancelStream {
				cancel()
			} else {
				close(f.closed)
			}
			for range stream {
			}
			if f.active.Load() != 0 {
				t.Fatal("upstream still active after downstream closed")
			}
			if cancelStream {
				close(f.closed)
			}
			if _, e = g.TryComplete(context.Background(), ChatRequest{}); e != nil {
				t.Fatalf("slot leaked: %v", e)
			}
		})
	}
}

func TestGuardReleasesFailedRequestsAndConcurrentCallers(t *testing.T) {
	f := &guardedFixture{completeErr: errors.New("fixture"), streamErr: errors.New("fixture")}
	g := NewGuardedClient(f)
	if _, e := g.Stream(context.Background(), ChatRequest{}); e == nil {
		t.Fatal("expected stream error")
	}
	if _, e := g.TryComplete(context.Background(), ChatRequest{}); e != f.completeErr {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = g.Complete(context.Background(), ChatRequest{}) }()
	}
	wg.Wait()
	if f.maximum.Load() != 1 {
		t.Fatal("concurrent inference")
	}
}
