package api

import "testing"

func TestSSEPollBackoffIsBounded(t *testing.T) {
	backoff := newSSEPollBackoff()
	want := []int64{500, 1000, 2000, 2000}
	for i, wantMS := range want {
		if got := backoff.Delay().Milliseconds(); got != wantMS {
			t.Fatalf("step %d: delay=%dms, want %dms", i, got, wantMS)
		}
		backoff.Advance()
	}
}

func TestSSEPollBackoffResetReturnsToResponsiveInterval(t *testing.T) {
	backoff := newSSEPollBackoff()
	backoff.Advance()
	backoff.Advance()
	if got := backoff.Delay().Milliseconds(); got != 2000 {
		t.Fatalf("before reset: delay=%dms, want 2000ms", got)
	}

	backoff.Reset()
	if got := backoff.Delay().Milliseconds(); got != 500 {
		t.Fatalf("after reset: delay=%dms, want 500ms", got)
	}
}
