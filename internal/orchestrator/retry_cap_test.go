package orchestrator

import (
	"context"
	"errors"
	"testing"
)

func TestCitationCriticRetriesOnlyOnceAfterFinalizerCorrection(t *testing.T) {
	for _, test := range []struct {
		corrected bool
		want      int
	}{{false, 2}, {true, 1}} {
		repo, state := citationFixture()
		model := &scriptedLLM{responses: []string{"not json", "not json"}}
		worker := New(repo, model, nil, Options{MaxInputBytes: 64 << 10, MaxOutputTokens: 256})
		catalogue, err := worker.citationCatalogue(context.Background(), "research", state.ToolResults, []string{fixtureCitationID})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(context.Background(), finalizerCorrectedKey{}, test.corrected)
		_, err = worker.reviewCitationClaims(ctx, "research", "The archive contains 40 volumes. ["+fixtureCitationID+"]", catalogue)
		if !errors.Is(err, errCitationMapping) || model.requests != test.want {
			t.Fatalf("corrected=%t critic calls=%d err=%v, want %d calls", test.corrected, model.requests, err, test.want)
		}
	}
}
