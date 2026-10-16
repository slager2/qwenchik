package domain

import (
	"errors"
	"testing"
)

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
			{ID: "a", Kind: "analysis", Description: "first", Dependencies: []string{"b"}, SuccessCriteria: []string{"first result exists"}},
			{ID: "b", Kind: "analysis", Description: "second", Dependencies: []string{"a"}, SuccessCriteria: []string{"second result exists"}},
		},
	}
	if err := ValidatePlan(valid, 12); err == nil {
		t.Fatal("expected a cyclic plan to be rejected")
	}
	valid.Subtasks[0].Dependencies = nil
	valid.Subtasks[1].Dependencies = []string{"a"}
	valid.Subtasks[0].Kind = "host_shell"
	if err := ValidatePlan(valid, 12); !errors.Is(err, ErrUnsupportedPlanKind) {
		t.Fatalf("unsupported plan kind error = %v", err)
	}
	for _, kind := range []string{"browser", "extract", "host_shell"} {
		valid.Subtasks[0].Kind = kind
		if err := ValidatePlan(valid, 12); !errors.Is(err, ErrUnsupportedPlanKind) {
			t.Errorf("plan kind %q error = %v, want ErrUnsupportedPlanKind", kind, err)
		}
	}
}

func TestValidatePlanRequiresNonblankSuccessCriteria(t *testing.T) {
	base := Plan{
		SchemaVersion: 1, Goal: "inspect", MaxSteps: 1, FinalDeliverable: "summary",
		Subtasks: []PlanStep{{ID: "step_1", Kind: "analysis", Description: "Inspect the input", SuccessCriteria: []string{"result is recorded"}}},
	}
	for _, criteria := range [][]string{nil, {}, {""}, {" \t\n"}} {
		plan := base
		plan.Subtasks = append([]PlanStep(nil), base.Subtasks...)
		plan.Subtasks[0].SuccessCriteria = criteria
		if err := ValidatePlan(plan, 12); err == nil {
			t.Errorf("ValidatePlan accepted success criteria %#v", criteria)
		}
	}
	if err := ValidatePlan(base, 12); err != nil {
		t.Fatalf("valid criterion was rejected: %v", err)
	}
}
