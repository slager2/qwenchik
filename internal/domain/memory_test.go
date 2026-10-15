package domain

import (
	"errors"
	"math"
	"testing"
)

func TestMemoryValidationBoundsConfidenceAndScope(t *testing.T) {
	confidence := math.NaN()
	memory := Memory{Kind: "preference", Status: MemoryCandidate, Text: "Keep answers brief.", Scope: "global", Confidence: &confidence, RetentionPolicy: "until_deleted"}
	if err := memory.Validate(); !errors.Is(err, ErrInvalidMemory) {
		t.Fatalf("NaN confidence error = %v", err)
	}
	confidence = 0.8
	memory.Confidence = &confidence
	if err := memory.Validate(); err != nil {
		t.Fatalf("valid global memory: %v", err)
	}
	memory.Scope = "task"
	if err := memory.Validate(); !errors.Is(err, ErrInvalidMemory) {
		t.Fatalf("task scope without source error = %v", err)
	}
}
