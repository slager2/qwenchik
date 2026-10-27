package domain

import "errors"

// Stored in the existing checkpoint, not concatenated into tasks.plan_json.
// task_steps/events retain execution history. Only storage transactions advance it.
type RoundPlan struct {
	Number int  `json:"number"`
	Plan   Plan `json:"plan"`
}
type ResearchPlans struct {
	Version      int         `json:"version"`
	CurrentRound int         `json:"current_round"`
	InitialPlan  Plan        `json:"initial_plan"`
	Rounds       []RoundPlan `json:"round_plans"`
}

func (p ResearchPlans) Current() (Plan, error) {
	if p.Version != 1 || p.CurrentRound < 1 || p.CurrentRound > 10 || len(p.Rounds) != p.CurrentRound {
		return Plan{}, errors.New("invalid research round plans")
	}
	for i, r := range p.Rounds {
		if r.Number != i+1 || ValidatePlan(r.Plan, 12) != nil {
			return Plan{}, errors.New("invalid bounded round plan")
		}
	}
	return p.Rounds[len(p.Rounds)-1].Plan, nil
}

// Version-one defaults stay unchanged for already persisted sessions and callers.
// New submissions use version-two budgets with an explicit total-step allowance.
func RoundResearchBudgetForProfile(name string) (ResearchBudget, bool) {
	b, ok := ResearchBudgetForProfile(name)
	if !ok {
		return b, false
	}
	b.MaxInitialPlanSteps, b.MaxRoundSteps = 12, 12
	b.MaxReportBytes = 512 << 10
	switch name {
	case ResearchProfileQuick:
		b.MaxTotalSteps = 12
		b.MaxLLMCalls = 24
		b.MaxReportSections = 4
		b.MaxReportParagraphs = 8
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 8, 8
	case ResearchProfileStandard:
		b.MaxTotalSteps = 20
		b.MaxLLMCalls = 48
		b.MaxCitationClaims = 16
		b.MaxReportSections = 6
		b.MaxReportParagraphs = 16
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 24, 16
	case ResearchProfileDeepLaptop:
		b.MaxTotalSteps = 32
		b.MaxLLMCalls = 64
		b.MaxCitationClaims = 24
		b.MaxReportSections = 8
		b.MaxReportParagraphs = 24
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 40, 24
	case ResearchProfileLongLaptop:
		b.MaxTotalSteps = 64
		b.MaxLLMCalls = 96
		b.MaxCitationClaims = 32
		b.MaxReportSections = 8
		b.MaxReportParagraphs = 32
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 64, 32
	case ResearchProfileOvernight:
		b.MaxTotalSteps = 96
		b.MaxLLMCalls = 128
		b.MaxCitationClaims = 32
		b.MaxReportSections = 8
		b.MaxReportParagraphs = 32
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 80, 40
	}
	return b, true
}

func DefaultRoundResearchPolicy() ResearchPolicy {
	p := DefaultResearchPolicy()
	p.Version = 2
	p.Budget, _ = RoundResearchBudgetForProfile(ResearchProfileStandard)
	return p
}
