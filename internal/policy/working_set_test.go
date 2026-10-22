package policy

import "testing"

func TestWorkingSetGrantsAreExplicitAndTaskScoped(t *testing.T) {
	for _, capability := range []string{"task.evidence.read", "task.artifact.read", "task.context.read", "task.working_set.write"} {
		tool := Tool{Name: "fixture", RequiredCapability: capability, RiskClass: "read", AllowedRoles: []string{"analyst", "researcher", "planner", "coder"}}
		if NewDefaultEvaluator().Evaluate("researcher", tool).Action != Deny {
			t.Fatal("library default expanded its authority")
		}
		configured := NewWorkingSetEvaluator()
		for _, role := range []string{"analyst", "researcher"} {
			if configured.Evaluate(role, tool).Action != Allow {
				t.Fatalf("missing explicit %s grant", role)
			}
		}
		for _, role := range []string{"planner", "coder"} {
			if configured.Evaluate(role, tool).Action != Deny {
				t.Fatalf("ungranted %s gained working set access", role)
			}
		}
		tool.RequiredCapability = "host.shell"
		if configured.Evaluate("researcher", tool).Action != Deny {
			t.Fatal("working set configuration granted host shell")
		}
	}
}
