package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
)

func loadResearchPolicy(c *Config) error {
	c.ResearchPrivacyMode = env("RESEARCH_PRIVACY_MODE", domain.PrivacyExternalSearch)
	c.OfflineCorpusPath = env("RESEARCH_OFFLINE_CORPUS", "")
	if c.ResearchPrivacyMode == domain.PrivacyOffline {
		corpus, err := research.LoadOfflineCorpus(c.OfflineCorpusPath)
		if err != nil {
			return fmt.Errorf("RESEARCH_OFFLINE_CORPUS: %w", err)
		}
		c.OfflineCorpusSHA256 = corpus.SHA256()
	}
	c.SearchProviderPolicy = env("SEARCH_PROVIDER_POLICY", "local_primary")
	if c.SearchProviderPolicy != "local_primary" && c.SearchProviderPolicy != "parallel" {
		return errors.New("SEARCH_PROVIDER_POLICY must be local_primary or parallel")
	}
	c.ResearchDefaultProfile = env("RESEARCH_DEFAULT_PROFILE", domain.ResearchProfileStandard)
	if !knownResearchProfile(c.ResearchDefaultProfile) {
		return errors.New("RESEARCH_DEFAULT_PROFILE must be quick, standard, deep_laptop, long_laptop, or overnight_laptop")
	}
	c.PDFPolicy = pdfextract.DefaultPolicy()
	var err error
	c.ResearchCaps = defaultResearchCaps(c.MaxSteps, c.MaxSearchQueries)
	for _, item := range []struct {
		name   string
		target *int
	}{
		{"MAX_INITIAL_PLAN_STEPS", &c.ResearchCaps.MaxInitialPlanSteps}, {"MAX_ROUND_STEPS", &c.ResearchCaps.MaxRoundSteps}, {"MAX_TOTAL_ROUNDS", &c.ResearchCaps.MaxRounds}, {"MAX_TOTAL_STEPS", &c.ResearchCaps.MaxTotalSteps},
		{"MAX_FETCHES", &c.ResearchCaps.MaxFetches}, {"MAX_LLM_CALLS", &c.ResearchCaps.MaxLLMCalls}, {"MAX_EVIDENCE", &c.ResearchCaps.MaxEvidence}, {"MAX_QUESTIONS", &c.ResearchCaps.MaxQuestionsCreated}, {"MAX_QUESTIONS_ACCEPTED", &c.ResearchCaps.MaxQuestionsAccepted},
		{"MAX_CITATION_SOURCES", &c.ResearchCaps.MaxCitationSources}, {"MAX_CITATION_CLAIMS", &c.ResearchCaps.MaxCitationClaims}, {"MAX_REPORT_SECTIONS", &c.ResearchCaps.MaxReportSections}, {"MAX_REPORT_PARAGRAPHS", &c.ResearchCaps.MaxReportParagraphs}, {"MAX_REPORT_BYTES", &c.ResearchCaps.MaxReportBytes},
	} {
		if *item.target, err = intEnv(item.name, *item.target); err != nil {
			return err
		}
	}
	if err := validateResearchCaps(c.ResearchCaps); err != nil {
		return err
	}
	if c.ResearchAdaptiveEnabled, err = boolEnv("RESEARCH_ADAPTIVE_ENABLED", false); err != nil {
		return err
	}
	if c.ResearchSectionReportEnabled, err = boolEnv("RESEARCH_SECTION_REPORT_ENABLED", false); err != nil {
		return err
	}
	if c.MaxSourceBytes, err = int64Env("MAX_SOURCE_BYTES", min(int64(16<<20), c.MaxFetchBytes)); err != nil {
		return err
	}
	if c.PDFPolicy.MaxInputBytes, err = int64Env("MAX_PDF_BYTES", min(c.PDFPolicy.MaxInputBytes, c.MaxFetchBytes)); err != nil {
		return err
	}
	if c.PDFPolicy.MaxPages, err = intEnv("PDF_MAX_PAGES", c.PDFPolicy.MaxPages); err != nil {
		return err
	}
	if c.PDFPolicy.MaxTextRunes, err = intEnv("PDF_MAX_TEXT_RUNES", c.PDFPolicy.MaxTextRunes); err != nil {
		return err
	}
	seconds, err := intEnv("PDF_EXTRACTION_TIMEOUT_SECONDS", 30)
	if err != nil {
		return err
	}
	if seconds < 1 || seconds > 600 {
		return errors.New("PDF_EXTRACTION_TIMEOUT_SECONDS must be 1..600")
	}
	c.PDFPolicy.ExtractionTimeout = time.Duration(seconds) * time.Second
	if c.PDFDownloadTimeoutSeconds, err = intEnv("PDF_DOWNLOAD_TIMEOUT_SECONDS", 120); err != nil {
		return err
	}
	if c.PDFDownloadTimeoutSeconds < 1 || c.PDFDownloadTimeoutSeconds > 3600 {
		return errors.New("PDF_DOWNLOAD_TIMEOUT_SECONDS must be 1..3600")
	}
	memoryMB, err := int64Env("PDF_EXTRACTOR_MEMORY_MB", 512)
	if err != nil {
		return err
	}
	tempMB, err := int64Env("PDF_EXTRACTOR_TMP_MB", 64)
	if err != nil {
		return err
	}
	if memoryMB < 32 || memoryMB > 8192 || tempMB < 1 || tempMB > 4096 {
		return errors.New("PDF extractor memory/temp limits are invalid")
	}
	c.PDFPolicy.MemoryBytes, c.PDFPolicy.TempBytes = memoryMB<<20, tempMB<<20
	if c.PDFPolicy.AllowUnsandboxedFallback, err = boolEnv("PDF_ALLOW_UNSANDBOXED_FALLBACK", false); err != nil {
		return err
	}
	if c.ArtifactMaxBytes, err = int64Env("ARTIFACT_MAX_BYTES", max(c.MaxSourceBytes, c.PDFPolicy.MaxInputBytes)); err != nil {
		return err
	}
	if c.SandboxMaxFileBytes, err = int64Env("SANDBOX_MAX_FILE_BYTES", 5_000_000); err != nil {
		return err
	}
	c.FetchTempDir = env("FETCH_TEMP_DIR", "./data/fetch-temp")
	if c.FetchTempTaskBytes, err = int64Env("FETCH_TEMP_TASK_BYTES", 128<<20); err != nil {
		return err
	}
	if c.FetchTempGlobalBytes, err = int64Env("FETCH_TEMP_GLOBAL_BYTES", 256<<20); err != nil {
		return err
	}
	if c.PDFPolicy.MaxInputBytes > c.MaxFetchBytes && os.Getenv("MAX_PDF_BYTES") != "" {
		return errors.New("MAX_PDF_BYTES exceeds MAX_FETCH_BYTES task budget")
	}
	if err := c.PDFPolicy.Validate(); err != nil {
		return err
	}
	if err := c.FetchPolicy().Validate(); err != nil {
		return err
	}
	if c.ArtifactMaxBytes < max(c.MaxSourceBytes, c.PDFPolicy.MaxInputBytes) || c.ArtifactMaxBytes >= math.MaxInt64 {
		return errors.New("ARTIFACT_MAX_BYTES must cover source limits without integer overflow")
	}
	if c.SandboxMaxFileBytes < 1 || c.SandboxMaxFileBytes > 100<<20 {
		return errors.New("SANDBOX_MAX_FILE_BYTES must be 1..104857600")
	}
	return nil
}

func researchProfileNames() [5]string {
	return [5]string{
		domain.ResearchProfileQuick,
		domain.ResearchProfileStandard,
		domain.ResearchProfileDeepLaptop,
		domain.ResearchProfileLongLaptop,
		domain.ResearchProfileOvernight,
	}
}

func knownResearchProfile(name string) bool {
	_, ok := domain.ResearchBudgetForProfile(name)
	return ok
}

func (c Config) ResearchProfile(name string) (domain.ResearchPolicy, error) {
	budget, ok := domain.RoundResearchBudgetForProfile(name)
	if !ok {
		return domain.ResearchPolicy{}, fmt.Errorf("unknown research profile %q", name)
	}
	if err := c.validateResearchLimits(); err != nil {
		return domain.ResearchPolicy{}, err
	}

	caps := c.ResearchCaps
	if caps == (domain.ResearchBudget{}) {
		caps = defaultResearchCaps(c.MaxSteps, c.MaxSearchQueries)
	}
	if err := validateResearchCaps(caps); err != nil {
		return domain.ResearchPolicy{}, err
	}
	budget.MaxInitialPlanSteps = min(c.MaxSteps, min(budget.MaxInitialPlanSteps, caps.MaxInitialPlanSteps))
	budget.MaxSteps = budget.MaxInitialPlanSteps // compatibility alias; never a total-run ceiling in v2.
	budget.MaxRoundSteps = min(budget.MaxRoundSteps, caps.MaxRoundSteps)
	budget.MaxTotalSteps = min(budget.MaxTotalSteps, caps.MaxTotalSteps)
	budget.MaxRounds = min(budget.MaxRounds, caps.MaxRounds)
	budget.MaxSearchQueries = min(budget.MaxSearchQueries, c.MaxSearchQueries)
	budget.MaxFetches = min(budget.MaxFetches, caps.MaxFetches)
	budget.MaxLLMCalls = min(budget.MaxLLMCalls, caps.MaxLLMCalls)
	budget.MaxEvidence = min(budget.MaxEvidence, caps.MaxEvidence)
	budget.MaxQuestionsCreated = min(budget.MaxQuestionsCreated, caps.MaxQuestionsCreated)
	budget.MaxQuestionsAccepted = min(budget.MaxQuestionsAccepted, min(caps.MaxQuestionsAccepted, budget.MaxQuestionsCreated))
	budget.MaxCitationSources = min(budget.MaxCitationSources, min(caps.MaxCitationSources, budget.MaxEvidence))
	budget.MaxCitationClaims = min(budget.MaxCitationClaims, caps.MaxCitationClaims)
	budget.MaxReportSections = min(budget.MaxReportSections, caps.MaxReportSections)
	budget.MaxReportParagraphs = min(budget.MaxReportParagraphs, caps.MaxReportParagraphs)
	budget.MaxReportBytes = min(budget.MaxReportBytes, caps.MaxReportBytes)
	budget.MaxStagnantRounds = min(budget.MaxStagnantRounds, budget.MaxRounds)
	budget.MinRounds = min(budget.MinRounds, budget.MaxRounds)

	policy := domain.ResearchPolicy{
		PrivacyMode: domain.EffectivePrivacyMode(c.ResearchPrivacyMode), OfflineCorpusSHA256: c.OfflineCorpusSHA256,
		SectionedReportEnabled: c.ResearchSectionReportEnabled,
		MaxContinuations:       min(1, budget.MaxRounds-1),
		KeepAwake:              c.KeepAwake,
		Version:                2, Profile: name, AdaptiveEnabled: c.ResearchAdaptiveEnabled, Budget: budget,
		TaskFetchBudgetBytes: c.MaxFetchBytes, SourceFetchBytes: c.MaxSourceBytes, PDFBytes: c.PDFPolicy.MaxInputBytes,
		PDFPages: c.PDFPolicy.MaxPages, PDFTextRunes: c.PDFPolicy.MaxTextRunes,
		PDFDownloadTimeoutSec: c.PDFDownloadTimeoutSeconds,
		PDFExtractTimeoutSec:  int(c.PDFPolicy.ExtractionTimeout / time.Second),
		PDFMemoryBytes:        c.PDFPolicy.MemoryBytes, PDFTempBytes: c.PDFPolicy.TempBytes,
		PDFCPULimit: c.PDFPolicy.CPUs, PDFPIDLimit: c.PDFPolicy.PIDs,
		AllowUnsandboxedPDF: c.PDFPolicy.AllowUnsandboxedFallback,
	}
	if policy.AdaptiveEnabled {
		policy.MaxContinuations = max(0, budget.MaxRounds-1)
	}
	policy.FetchTarget = max(0, min(domain.ResearchFetchTargetForProfile(name), budget.MaxFetches, budget.MaxEvidence))
	// Half of the search queries stay available for continuation rounds.
	policy.SearchTarget = max(0, min(domain.ResearchSearchTargetForProfile(name), budget.MaxSearchQueries/2))
	return policy, ValidateResearchPolicy(policy)
}

func (c Config) ResearchPolicies() map[string]domain.ResearchPolicy {
	policies := make(map[string]domain.ResearchPolicy, 5)
	for _, profile := range researchProfileNames() {
		policy, err := c.ResearchProfile(profile)
		if err == nil {
			policies[profile] = policy
		}
	}
	return policies
}

func (c Config) validateResearchLimits() error {
	if c.MaxSteps < 1 || c.MaxSteps > 12 || c.MaxSearchQueries < 1 {
		return errors.New("research step and search caps are invalid")
	}
	if c.PDFDownloadTimeoutSeconds < 1 || c.PDFDownloadTimeoutSeconds > 3600 || c.PDFPolicy.ExtractionTimeout < time.Second || c.PDFPolicy.ExtractionTimeout > 600*time.Second || c.PDFPolicy.ExtractionTimeout%time.Second != 0 {
		return errors.New("research PDF timeouts are invalid")
	}
	if err := c.PDFPolicy.Validate(); err != nil {
		return err
	}
	return c.FetchPolicy().Validate()
}

func ValidateResearchPolicy(policy domain.ResearchPolicy) error {
	mode := domain.EffectivePrivacyMode(policy.PrivacyMode)
	if mode != domain.PrivacyExternalSearch && mode != domain.PrivacyLocalSearXNG && mode != domain.PrivacyOffline {
		return errors.New("unknown research privacy mode")
	}
	if mode == domain.PrivacyOffline {
		if len(policy.OfflineCorpusSHA256) != 64 {
			return errors.New("offline policy requires a pinned corpus hash")
		}
		for _, ch := range policy.OfflineCorpusSHA256 {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				return errors.New("invalid offline corpus hash")
			}
		}
	} else if policy.OfflineCorpusSHA256 != "" {
		return errors.New("offline corpus hash requires offline mode")
	}
	maxContinuations := 1
	if policy.AdaptiveEnabled {
		maxContinuations = min(9, policy.Budget.MaxRounds-1)
	}
	if policy.MaxContinuations < 0 || policy.MaxContinuations > maxContinuations {
		return errors.New("max_continuations exceeds the bounded research round allowance")
	}
	if (policy.Version != 1 && policy.Version != 2) || !knownResearchProfile(policy.Profile) {
		return errors.New("research policy version, profile, or adaptive setting is invalid")
	}
	b := policy.Budget
	if policy.FetchTarget < 0 || policy.FetchTarget > min(domain.MaxResearchFetchTarget, b.MaxFetches, b.MaxEvidence) {
		return errors.New("research fetch target exceeds the fetch, evidence, or plan capacity")
	}
	if policy.SearchTarget < 0 || policy.SearchTarget > min(domain.MaxResearchSearchTarget, b.MaxSearchQueries) {
		return errors.New("research search target exceeds the search query or plan capacity")
	}
	if b.WallTimeSeconds < 60 || b.WallTimeSeconds > 21600 || b.MaxRounds < 1 || b.MaxSteps < 1 || b.MaxSteps > 12 || b.MaxSearchQueries < 1 || b.MaxFetches < 1 || b.MaxLLMCalls < 1 || b.MaxEvidence < 1 || b.MaxQuestionsCreated < 1 || b.MaxQuestionsAccepted < 1 || b.MaxStagnantRounds < 1 || b.MaxCitationSources < 1 || b.MaxCitationClaims < 1 || b.MinRounds < 0 || b.MinRounds > 10 {
		return errors.New("research budget limits are invalid")
	}
	claimLimit, sourceLimit := 8, 8
	if policy.Version == 2 {
		claimLimit, sourceLimit = 32, 16
		if err := validateResearchCaps(b); err != nil {
			return err
		}
		if b.MaxSteps != b.MaxInitialPlanSteps {
			return errors.New("v2 max_steps must alias initial plan capacity")
		}
	}
	if b.MaxQuestionsAccepted > b.MaxQuestionsCreated || b.MaxStagnantRounds > b.MaxRounds || b.MaxCitationSources > b.MaxEvidence || b.MaxCitationSources > sourceLimit || b.MaxCitationClaims > claimLimit {
		return errors.New("research budget relationships are invalid")
	}
	if policy.TaskFetchBudgetBytes < 1 || policy.TaskFetchBudgetBytes == math.MaxInt64 || policy.SourceFetchBytes < 1 || policy.PDFBytes < 1 || policy.SourceFetchBytes > policy.TaskFetchBudgetBytes || policy.PDFBytes > policy.TaskFetchBudgetBytes {
		return errors.New("research source limits exceed the task fetch budget")
	}
	if policy.PDFDownloadTimeoutSec < 1 || policy.PDFDownloadTimeoutSec > 3600 || policy.PDFExtractTimeoutSec < 1 || policy.PDFExtractTimeoutSec > 600 {
		return errors.New("research PDF timeouts are invalid")
	}
	return (pdfextract.Policy{
		MaxInputBytes: policy.PDFBytes, MaxPages: policy.PDFPages, MaxTextRunes: policy.PDFTextRunes,
		ExtractionTimeout: time.Duration(policy.PDFExtractTimeoutSec) * time.Second,
		MemoryBytes:       policy.PDFMemoryBytes, TempBytes: policy.PDFTempBytes,
		CPUs: policy.PDFCPULimit, PIDs: policy.PDFPIDLimit,
		AllowUnsandboxedFallback: policy.AllowUnsandboxedPDF,
	}).Validate()
}

func (c Config) ResearchModelSnapshot() domain.ModelProfileSnapshot {
	return domain.ModelProfileSnapshot{
		ConfiguredContext: c.LLMContextLength, ServerReportedContext: c.ServerReportedContext, RuntimeFingerprint: c.RuntimeModelFingerprint, ThinkingPinned: true, ThinkingEnabled: c.LLMEnableThinking,
		Version: 1, Model: c.LLMModel, BaseURL: c.LLMBaseURL,
		ContextLength: c.LLMContextLength, MaxOutputTokens: c.LLMMaxOutputTokens,
		Temperature: c.LLMTemperature, TopP: c.LLMTopP, StopSequences: []string{},
		PromptVersion: "local-research-v1", SchemaVersion: 1,
	}
}

func researchProfileBudget(name string) (domain.ResearchBudget, bool) {
	return domain.ResearchBudgetForProfile(name)
}

func (c Config) FetchPolicy() research.FetchPolicy {
	return research.FetchPolicy{TaskFetchBudgetBytes: c.MaxFetchBytes, SourceFetchBytes: c.MaxSourceBytes, PDFBytes: c.PDFPolicy.MaxInputBytes,
		PDFDownloadTimeout: time.Duration(c.PDFDownloadTimeoutSeconds) * time.Second, SourceDownloadTimeout: 20 * time.Second,
		TempRoot: c.FetchTempDir, TempTaskBytes: c.FetchTempTaskBytes, TempGlobalBytes: c.FetchTempGlobalBytes}
}

func (c Config) ResearchLimits() map[string]any {
	return map[string]any{
		"adaptive_enabled": c.ResearchAdaptiveEnabled, "sectioned_report_enabled": c.ResearchSectionReportEnabled,
		"privacy_mode": domain.EffectivePrivacyMode(c.ResearchPrivacyMode), "offline_corpus_sha256": c.OfflineCorpusSHA256,
		"task_fetch_budget_bytes": c.MaxFetchBytes, "source_fetch_bytes": c.MaxSourceBytes, "pdf_bytes": c.PDFPolicy.MaxInputBytes,
		"pdf_pages": c.PDFPolicy.MaxPages, "pdf_text_runes": c.PDFPolicy.MaxTextRunes,
		"pdf_extraction_timeout_seconds": int(c.PDFPolicy.ExtractionTimeout / time.Second), "pdf_download_timeout_seconds": c.PDFDownloadTimeoutSeconds,
		"pdf_memory_bytes": c.PDFPolicy.MemoryBytes, "pdf_temp_bytes": c.PDFPolicy.TempBytes, "pdf_cpu_limit": c.PDFPolicy.CPUs, "pdf_pid_limit": c.PDFPolicy.PIDs,
		"pdf_allow_unsandboxed_fallback": c.PDFPolicy.AllowUnsandboxedFallback, "artifact_max_bytes": c.ArtifactMaxBytes, "sandbox_max_file_bytes": c.SandboxMaxFileBytes,
		"fetch_temp_task_bytes": c.FetchTempTaskBytes, "fetch_temp_global_bytes": c.FetchTempGlobalBytes,
	}
}
