package policy

import "testing"

func TestEvaluatorUsesRoleCapabilityAndRisk(t *testing.T) {
	evaluator := New(map[string][]string{
		"analyst": {"research.fetch", "external.submit"},
	})
	tests := []struct {
		name string
		role string
		tool Tool
		want Action
	}{
		{"read allowed", "analyst", Tool{Name: "fetch", RequiredCapability: "research.fetch", RiskClass: "read", AllowedRoles: []string{"analyst"}}, Allow},
		{"role denied", "planner", Tool{Name: "fetch", RequiredCapability: "research.fetch", RiskClass: "read", AllowedRoles: []string{"analyst"}}, Deny},
		{"capability denied", "analyst", Tool{Name: "write", RequiredCapability: "workspace.write", RiskClass: "workspace_write", AllowedRoles: []string{"analyst"}}, Deny},
		{"external requires approval", "analyst", Tool{Name: "submit", RequiredCapability: "external.submit", RiskClass: "external_side_effect", AllowedRoles: []string{"analyst"}}, ApprovalRequired},
		{"destructive denied", "analyst", Tool{Name: "delete", RequiredCapability: "external.submit", RiskClass: "destructive", AllowedRoles: []string{"analyst"}}, Deny},
		{"unknown risk denied", "analyst", Tool{Name: "unknown", RequiredCapability: "research.fetch", RiskClass: "surprise", AllowedRoles: []string{"analyst"}}, Deny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := evaluator.Evaluate(test.role, test.tool).Action; got != test.want {
				t.Fatalf("action = %q, want %q", got, test.want)
			}
		})
	}
}
