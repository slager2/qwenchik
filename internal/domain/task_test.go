package domain

import "testing"

func TestTaskStatusTransitions(t *testing.T) {
	tests := []struct {
		from, to TaskStatus
		want     bool
	}{
		{StatusInbox, StatusQueued, true},
		{StatusRunning, StatusWaitingApproval, true},
		{StatusPaused, StatusRunning, true},
		{StatusCompleted, StatusRunning, false},
		{StatusInbox, StatusCompleted, false},
	}
	for _, tt := range tests {
		if got := CanTransition(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransition(%q, %q) = %t, want %t", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestValidatePlanRejectsCyclesAndUnknownKinds(t *testing.T) {
	valid := Plan{
		SchemaVersion:    1,
		Goal:             "test",
		MaxSteps:         2,
		FinalDeliverable: "answer",
		Subtasks: []PlanStep{
			{ID: "a", Kind: "analysis", Description: "first", Dependencies: []string{"b"}},
			{ID: "b", Kind: "analysis", Description: "second", Dependencies: []string{"a"}},
		},
	}
	if err := ValidatePlan(valid, 12); err == nil {
		t.Fatal("expected a cyclic plan to be rejected")
	}
	valid.Subtasks[0].Dependencies = nil
	valid.Subtasks[1].Dependencies = []string{"a"}
	valid.Subtasks[0].Kind = "host_shell"
	if err := ValidatePlan(valid, 12); err == nil {
		t.Fatal("expected an unsupported plan kind to be rejected")
	}
}
