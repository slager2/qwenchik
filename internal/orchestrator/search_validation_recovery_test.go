package orchestrator

import (
	"errors"
	"fmt"
	"testing"

	"github.com/slager2/qwenchik/internal/providers/research"
)

func TestSearchValidationMissIsRecoverableOnlyWithUsableResults(t *testing.T) {
	timeout := fmt.Errorf("tool failed: %w", &research.SearchFailure{Code: "search_validation_timeout"})
	noUsable := &research.SearchFailure{Code: "search_no_usable_hits"}
	unavailable := &research.SearchFailure{Code: "search_unavailable"}
	empty := checkpointState{ToolResults: map[string]any{}}
	withHits := checkpointState{ToolResults: map[string]any{"search_1": []research.SearchHit{{Provider: "tavily", URL: "https://example.org/a"}}}}
	for _, tc := range []struct {
		name  string
		err   error
		state checkpointState
		want  bool
	}{
		{"timeout without anything usable fails", timeout, empty, false},
		{"timeout with earlier hits recovers", timeout, withHits, true},
		{"no usable hits with earlier hits recovers", noUsable, withHits, true},
		{"provider outage still fails", unavailable, withHits, false},
		{"plain error still fails", errors.New("boom"), withHits, false},
	} {
		if got := recoverableSearchValidationMiss(tc.err, tc.state); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
