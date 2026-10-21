package domain

const DefaultTaskFetchBudgetBytes int64 = 128 << 20

func ResearchBudgetForProfile(name string) (ResearchBudget, bool) {
	b := ResearchBudget{MaxSteps: 12, MaxStagnantRounds: 2, MaxCitationSources: 8, MaxCitationClaims: 8}
	switch name {
	case ResearchProfileQuick:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 300, 1, 4, 4, 10, 4
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted, b.MaxStagnantRounds, b.MaxCitationSources = 6, 2, 1, 4
	case ResearchProfileStandard:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 1800, 3, 8, 8, 24, 8
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 16, 6
	case ResearchProfileDeepLaptop:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 7200, 5, 16, 12, 40, 12
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 30, 10
	case ResearchProfileLongLaptop:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 14400, 8, 32, 24, 72, 24
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 60, 20
	case ResearchProfileOvernight:
		b.WallTimeSeconds, b.MaxRounds, b.MaxSearchQueries, b.MaxFetches, b.MaxLLMCalls, b.MaxEvidence = 21600, 10, 48, 36, 96, 32
		b.MaxQuestionsCreated, b.MaxQuestionsAccepted = 80, 24
	default:
		return ResearchBudget{}, false
	}
	return b, true
}

func DefaultResearchPolicy() ResearchPolicy {
	b, _ := ResearchBudgetForProfile(ResearchProfileStandard)
	return ResearchPolicy{
		Version: 1, Profile: ResearchProfileStandard, Budget: b,
		TaskFetchBudgetBytes: DefaultTaskFetchBudgetBytes, SourceFetchBytes: 16 << 20, PDFBytes: 64 << 20,
		PDFPages: 20, PDFTextRunes: 50000, PDFDownloadTimeoutSec: 120, PDFExtractTimeoutSec: 30,
		PDFMemoryBytes: 512 << 20, PDFTempBytes: 64 << 20, PDFCPULimit: 1, PDFPIDLimit: 32,
	}
}
