package domain

const DefaultTaskFetchBudgetBytes int64 = 128 << 20

func ResearchBudgetForProfile(name string) (ResearchBudget, bool) {
	b := ResearchBudget{MaxSteps: 12, MaxStagnantRounds: 2, MaxCitationSources: 8, MaxCitationClaims: 8}
	switch name {
	case ResearchProfileQuick:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 300, 1, 4, 6, 10, 4
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted, b.MaxStagnantRounds, b.MaxCitationSources = 6, 2, 1, 4
	case ResearchProfileStandard:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 1800, 3, 8, 16, 24, 8
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 16, 6
	case ResearchProfileDeepLaptop:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 7200, 5, 16, 24, 40, 12
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 30, 10
	case ResearchProfileLongLaptop:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 14400, 8, 32, 48, 72, 24
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 60, 20
	case ResearchProfileOvernight:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 21600, 10, 48, 64, 96, 32
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 80, 24
	default:
		return ResearchBudget{}, false
	}
	return b, true
}

// MaxResearchFetchTarget is the largest initial-plan fetch breadth: the
// 12-step plan capacity minus three search facets.
const MaxResearchFetchTarget = 9

// ResearchFetchTargetForProfile is how many distinct sources the initial
// research plan of a profile tries to fetch. Fetch steps are deterministic
// (no LLM call), so breadth costs wall time, bytes and evidence slots, not
// LLM calls; each value leaves part of MaxEvidence for a continuation round
// (standard 6 of 8, deep 8 of 12, long/overnight 9 of 24/32, capped by the
// plan capacity). MaxFetches counts attempts, failed ones included, so it is
// about twice MaxEvidence: bot-blocking sites (403/unavailable) are common
// and must not exhaust the budget before a second round.
func ResearchFetchTargetForProfile(name string) int {
	switch name {
	case ResearchProfileQuick:
		return 2
	case ResearchProfileStandard:
		return 6
	case ResearchProfileDeepLaptop:
		return 8
	case ResearchProfileLongLaptop, ResearchProfileOvernight:
		return MaxResearchFetchTarget
	}
	return 0
}

func DefaultResearchPolicy() ResearchPolicy {
	b, _ := ResearchBudgetForProfile(ResearchProfileStandard)
	return ResearchPolicy{
		PrivacyMode:      PrivacyExternalSearch,
		MaxContinuations: 1,
		Version:          1, Profile: ResearchProfileStandard, Budget: b,
		TaskFetchBudgetBytes: DefaultTaskFetchBudgetBytes, SourceFetchBytes: 16 << 20, PDFBytes: 64 << 20,
		PDFPages: 20, PDFTextRunes: 50000, PDFDownloadTimeoutSec: 120, PDFExtractTimeoutSec: 30,
		PDFMemoryBytes: 512 << 20, PDFTempBytes: 64 << 20, PDFCPULimit: 1, PDFPIDLimit: 32,
	}
}
