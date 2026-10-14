package policy

type Action string

const (
	Allow            Action = "allow"
	ApprovalRequired Action = "approval_required"
	Deny             Action = "deny"
)

type Tool struct {
	Name               string
	RequiredCapability string
	RiskClass          string
	Idempotent         bool
	AllowedRoles       []string
}

type Decision struct {
	Action     Action `json:"action"`
	Capability string `json:"capability,omitempty"`
	RiskClass  string `json:"risk_class,omitempty"`
	Reason     string `json:"reason"`
}

type Evaluator struct {
	grants map[string]map[string]struct{}
}

func New(grants map[string][]string) *Evaluator {
	evaluator := &Evaluator{grants: make(map[string]map[string]struct{}, len(grants))}
	for role, capabilities := range grants {
		roleGrants := make(map[string]struct{}, len(capabilities))
		for _, capability := range capabilities {
			roleGrants[capability] = struct{}{}
		}
		evaluator.grants[role] = roleGrants
	}
	return evaluator
}

func NewDefaultEvaluator() *Evaluator {
	return New(map[string][]string{
		"analyst":    {"task.text.inspect"},
		"verifier":   {"task.text.inspect"},
		"researcher": {"research.search", "research.fetch"},
	})
}

func (e *Evaluator) Evaluate(role string, tool Tool) Decision {
	decision := Decision{Action: Deny, Capability: tool.RequiredCapability, RiskClass: tool.RiskClass}
	if tool.Name == "" || tool.RequiredCapability == "" {
		decision.Reason = "tool descriptor is incomplete"
		return decision
	}
	allowedRole := false
	for _, allowed := range tool.AllowedRoles {
		if allowed == role {
			allowedRole = true
			break
		}
	}
	if !allowedRole {
		decision.Reason = "role is not allowlisted for this tool"
		return decision
	}
	roleGrants := e.grants[role]
	if _, allowed := roleGrants[tool.RequiredCapability]; !allowed {
		decision.Reason = "role does not hold the required capability"
		return decision
	}
	switch tool.RiskClass {
	case "read":
		decision.Action = Allow
		decision.Reason = "read-only capability explicitly granted"
	case "workspace_write":
		decision.Action = Allow
		decision.Reason = "configured workspace write capability explicitly granted"
	case "sandbox_execute":
		decision.Action = Allow
		decision.Reason = "sandbox execution capability explicitly granted"
	case "external_side_effect":
		decision.Action = ApprovalRequired
		decision.Reason = "external side effects require an approval record"
	case "destructive":
		decision.Action = Deny
		decision.Reason = "destructive capability is denied by default"
	default:
		decision.Action = Deny
		decision.Reason = "unknown risk class is denied"
	}
	return decision
}
