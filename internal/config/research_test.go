package config

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func isolatedResearchEnv(t *testing.T) {
	t.Helper()
	clearConfigEnv(t)
	chdirForTest(t, t.TempDir())
	for _, name := range []string{"MAX_SOURCE_BYTES", "MAX_PDF_BYTES", "PDF_MAX_PAGES", "PDF_MAX_TEXT_RUNES", "PDF_EXTRACTION_TIMEOUT_SECONDS", "PDF_DOWNLOAD_TIMEOUT_SECONDS", "PDF_EXTRACTOR_MEMORY_MB", "PDF_EXTRACTOR_TMP_MB", "PDF_ALLOW_UNSANDBOXED_FALLBACK", "ARTIFACT_MAX_BYTES", "SANDBOX_MAX_FILE_BYTES", "FETCH_TEMP_DIR", "FETCH_TEMP_TASK_BYTES", "FETCH_TEMP_GLOBAL_BYTES"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResearchLoadDefaultsAndSafeEffectiveSnapshot(t *testing.T) {
	isolatedResearchEnv(t)
	t.Setenv("LLM_API_KEY", "private-model-fixture")
	t.Setenv("TAVILY_API_KEY", "private-search-fixture")
	t.Setenv("FETCH_TEMP_DIR", "./private-temp-fixture")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"task_fetch_budget_bytes": int64(128 << 20), "source_fetch_bytes": int64(16 << 20), "pdf_bytes": int64(64 << 20),
		"pdf_pages": 20, "pdf_text_runes": 50000, "pdf_extraction_timeout_seconds": 30, "pdf_download_timeout_seconds": 120,
		"pdf_memory_bytes": int64(512 << 20), "pdf_temp_bytes": int64(64 << 20), "pdf_cpu_limit": float64(1), "pdf_pid_limit": 32,
		"pdf_allow_unsandboxed_fallback": false, "artifact_max_bytes": int64(64 << 20), "sandbox_max_file_bytes": int64(5_000_000),
		"fetch_temp_task_bytes": int64(128 << 20), "fetch_temp_global_bytes": int64(256 << 20),
	}
	limits := cfg.ResearchLimits()
	if !reflect.DeepEqual(limits, want) {
		t.Fatalf("effective limits=%v want=%v", limits, want)
	}
	encoded, err := json.Marshal(limits)
	if err != nil || strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "api_key") || strings.Contains(string(encoded), "temp_dir") {
		t.Fatal("effective limits must expose only public policy fields")
	}
	limits["pdf_bytes"] = int64(1)
	if cfg.ResearchLimits()["pdf_bytes"] != int64(64<<20) {
		t.Fatal("mutating a public snapshot changed the configured policy")
	}
	policy := cfg.FetchPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	if policy.TaskFetchBudgetBytes != cfg.MaxFetchBytes || policy.SourceFetchBytes != cfg.MaxSourceBytes || policy.PDFBytes != cfg.PDFPolicy.MaxInputBytes || policy.PDFDownloadTimeout != 120*time.Second || policy.SourceDownloadTimeout != 20*time.Second || policy.TempRoot != cfg.FetchTempDir || policy.TempTaskBytes != cfg.FetchTempTaskBytes || policy.TempGlobalBytes != cfg.FetchTempGlobalBytes || cfg.PDFPolicy.ExtractionTimeout != 30*time.Second || cfg.PDFPolicy.AllowUnsandboxedFallback {
		t.Fatal("fetch/extractor policy differs from the effective snapshot")
	}
}

func TestResearchProfilesAndModelSnapshotAreTypedEffectiveCopies(t *testing.T) {
	isolatedResearchEnv(t)
	t.Setenv("LLM_API_KEY", "private-model-fixture")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResearchDefaultProfile != "standard" || cfg.ResearchAdaptiveEnabled {
		t.Fatalf("default research profile=%q", cfg.ResearchDefaultProfile)
	}
	wantNames := []string{"quick", "standard", "deep_laptop", "long_laptop", "overnight_laptop"}
	policies := cfg.ResearchPolicies()
	if len(policies) != len(wantNames) {
		t.Fatalf("profiles=%v", policies)
	}
	for _, name := range wantNames {
		policy, ok := policies[name]
		if !ok || policy.Profile != name || policy.Version != 1 || policy.AdaptiveEnabled || policy.Budget.WallTimeSeconds < 60 || policy.Budget.MaxSteps > cfg.MaxSteps || policy.Budget.MaxSearchQueries > cfg.MaxSearchQueries || policy.SourceFetchBytes != cfg.MaxSourceBytes || policy.PDFBytes != cfg.PDFPolicy.MaxInputBytes {
			t.Fatalf("profile %q not effective/clamped: %#v", name, policy)
		}
		selected, err := cfg.ResearchProfile(name)
		if err != nil || !reflect.DeepEqual(selected, policy) {
			t.Fatalf("selected %q=%#v err=%v; map=%#v", name, selected, err, policy)
		}
	}
	if _, err := cfg.ResearchProfile("unknown"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	policies["standard"] = domain.ResearchPolicy{Profile: "mutated"}
	if cfg.ResearchPolicies()["standard"].Profile != "standard" {
		t.Fatal("mutating returned policies changed the config")
	}
	model := cfg.ResearchModelSnapshot()
	if model.Model != "local-qwen" || model.BaseURL != cfg.LLMBaseURL || model.ContextLength != cfg.LLMContextLength || model.MaxOutputTokens != cfg.LLMMaxOutputTokens || model.Temperature != cfg.LLMTemperature || model.TopP != cfg.LLMTopP || model.PromptVersion != "local-research-v1" || model.SchemaVersion != 1 || model.Version != 1 || model.StopSequences == nil || len(model.StopSequences) != 0 {
		t.Fatalf("model snapshot=%#v", model)
	}
	encoded, err := json.Marshal(model)
	if err != nil || strings.Contains(string(encoded), "private-model-fixture") || strings.Contains(string(encoded), "api_key") {
		t.Fatalf("model snapshot exposed credentials: %s err=%v", encoded, err)
	}
}

func TestResearchDefaultProfileAndTaskTimeoutValidation(t *testing.T) {
	for _, name := range []string{"quick", "standard", "deep_laptop", "long_laptop", "overnight_laptop"} {
		t.Run(name, func(t *testing.T) {
			isolatedResearchEnv(t)
			t.Setenv("RESEARCH_DEFAULT_PROFILE", name)
			cfg, err := Load()
			if err != nil || cfg.ResearchDefaultProfile != name {
				t.Fatalf("profile=%q err=%v", cfg.ResearchDefaultProfile, err)
			}
		})
	}
	isolatedResearchEnv(t)
	t.Setenv("RESEARCH_DEFAULT_PROFILE", "remote")
	if _, err := Load(); err == nil {
		t.Fatal("unknown default profile accepted")
	}
	t.Setenv("RESEARCH_DEFAULT_PROFILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("explicit empty default profile accepted")
	}

	isolatedResearchEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.TaskTimeoutSeconds = int(math.MaxInt64/1_000_000_000) + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("task timeout that overflows time.Duration accepted")
	}
	for _, seconds := range []int{1, 7200, 21600} {
		cfg.TaskTimeoutSeconds = seconds
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid task timeout=%d err=%v", seconds, err)
		}
	}
	cfg.TaskTimeoutSeconds = 21601
	if err := cfg.Validate(); err == nil {
		t.Fatal("task timeout above the six-hour server maximum accepted")
	}
}

func TestResearchAdaptiveIsExplicitlyOffUntilPhase3(t *testing.T) {
	isolatedResearchEnv(t)
	t.Setenv("RESEARCH_ADAPTIVE_ENABLED", "false")
	cfg, err := Load()
	if err != nil || cfg.ResearchAdaptiveEnabled {
		t.Fatalf("fixed research adaptive=%v err=%v", cfg.ResearchAdaptiveEnabled, err)
	}
	for _, policy := range cfg.ResearchPolicies() {
		encoded, err := json.Marshal(policy)
		if err != nil || !strings.Contains(string(encoded), `"adaptive_enabled":false`) {
			t.Fatalf("adaptive flag not fixed in policy snapshot=%s err=%v", encoded, err)
		}
	}
	t.Setenv("RESEARCH_ADAPTIVE_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "Phase 3") {
		t.Fatalf("unsupported adaptive config err=%v", err)
	}
}

func TestResearchModelSnapshotUsesValidatedEffectiveConfiguration(t *testing.T) {
	isolatedResearchEnv(t)
	for key, value := range map[string]string{
		"LLM_BASE_URL": "http://[::1]:8000/v1", "LLM_MODEL": "configured-local-model",
		"LLM_CONTEXT_LENGTH": "32768", "LLM_MAX_OUTPUT_TOKENS": "3072", "LLM_TEMPERATURE": "0", "LLM_TOP_P": "0.7",
	} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLMContextLength = 8192
	want := domain.ModelProfileSnapshot{
		Version: 1, Model: "configured-local-model", BaseURL: "http://[::1]:8000/v1",
		ContextLength: 8192, MaxOutputTokens: 3072, Temperature: 0, TopP: 0.7,
		StopSequences: []string{}, PromptVersion: "local-research-v1", SchemaVersion: 1,
	}
	if got := cfg.ResearchModelSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("effective model snapshot=%#v want=%#v", got, want)
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		cfg.LLMTemperature = invalid
		if err := cfg.Validate(); err == nil {
			t.Fatal("nonfinite model temperature accepted")
		}
		cfg.LLMTemperature, cfg.LLMTopP = 0, invalid
		if err := cfg.Validate(); err == nil {
			t.Fatal("nonfinite model top_p accepted")
		}
		cfg.LLMTopP = 0.7
	}
}

func TestResearchProfileBudgetsMatchCanonicalAndExactBrief(t *testing.T) {
	isolatedResearchEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	uncapped := cfg
	uncapped.MaxSearchQueries = 100
	for _, test := range []struct {
		profile string
		budget  domain.ResearchBudget
	}{
		{"quick", domain.ResearchBudget{
			WallTimeSeconds: 300, MaxRounds: 1, MaxSteps: 12, MaxSearchQueries: 4, MaxFetches: 4,
			MaxLLMCalls: 10, MaxEvidence: 4, MaxQuestionsCreated: 6, MaxQuestionsAccepted: 2,
			MaxStagnantRounds: 1, MaxCitationSources: 4, MaxCitationClaims: 8,
		}},
		{"standard", domain.ResearchBudget{
			WallTimeSeconds: 1800, MaxRounds: 3, MaxSteps: 12, MaxSearchQueries: 8, MaxFetches: 8,
			MaxLLMCalls: 24, MaxEvidence: 8, MaxQuestionsCreated: 16, MaxQuestionsAccepted: 6,
			MaxStagnantRounds: 2, MaxCitationSources: 8, MaxCitationClaims: 8,
		}},
		{"deep_laptop", domain.ResearchBudget{
			WallTimeSeconds: 7200, MaxRounds: 5, MaxSteps: 12, MaxSearchQueries: 16, MaxFetches: 12,
			MaxLLMCalls: 40, MaxEvidence: 12, MaxQuestionsCreated: 30, MaxQuestionsAccepted: 10,
			MaxStagnantRounds: 2, MaxCitationSources: 8, MaxCitationClaims: 8,
		}},
		{"long_laptop", domain.ResearchBudget{
			WallTimeSeconds: 14400, MaxRounds: 8, MaxSteps: 12, MaxSearchQueries: 32, MaxFetches: 24,
			MaxLLMCalls: 72, MaxEvidence: 24, MaxQuestionsCreated: 60, MaxQuestionsAccepted: 20,
			MaxStagnantRounds: 2, MaxCitationSources: 8, MaxCitationClaims: 8,
		}},
		{"overnight_laptop", domain.ResearchBudget{
			WallTimeSeconds: 21600, MaxRounds: 10, MaxSteps: 12, MaxSearchQueries: 48, MaxFetches: 36,
			MaxLLMCalls: 96, MaxEvidence: 32, MaxQuestionsCreated: 80, MaxQuestionsAccepted: 24,
			MaxStagnantRounds: 2, MaxCitationSources: 8, MaxCitationClaims: 8,
		}},
	} {
		t.Run(test.profile, func(t *testing.T) {
			canonical, ok := domain.ResearchBudgetForProfile(test.profile)
			if !ok || canonical != test.budget {
				t.Fatalf("canonical budget=%#v want=%#v found=%v", canonical, test.budget, ok)
			}
			wrapped, ok := researchProfileBudget(test.profile)
			if !ok || wrapped != canonical {
				t.Fatalf("config wrapper budget=%#v canonical=%#v found=%v", wrapped, canonical, ok)
			}
			nominal, err := uncapped.ResearchProfile(test.profile)
			if err != nil || nominal.Budget != test.budget {
				t.Fatalf("nominal budget=%#v want=%#v err=%v", nominal.Budget, test.budget, err)
			}
			wantEffective := test.budget
			wantEffective.MaxSearchQueries = min(wantEffective.MaxSearchQueries, cfg.MaxSearchQueries)
			effective, err := cfg.ResearchProfile(test.profile)
			if err != nil || effective.Budget != wantEffective || cfg.ResearchPolicies()[test.profile] != effective {
				t.Fatalf("effective budget=%#v want=%#v err=%v", effective.Budget, wantEffective, err)
			}
		})
	}
}

func TestResearchBudgetsClampOnlyServerCapsAndPreserveWallTimes(t *testing.T) {
	isolatedResearchEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	uncapped := cfg
	uncapped.MaxSearchQueries = 100
	limited := cfg
	limited.MaxSteps, limited.MaxSearchQueries = 2, 3
	for name, want := range uncapped.ResearchPolicies() {
		got, err := limited.ResearchProfile(name)
		want.Budget.MaxSteps = 2
		want.Budget.MaxSearchQueries = 3
		if err != nil || got != want {
			t.Fatalf("server caps changed unrelated profile limits for %s: got=%#v want=%#v err=%v", name, got, want, err)
		}
	}
	encoded, err := json.Marshal(cfg.ResearchPolicies())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := json.Marshal(cfg.ResearchPolicies())
		if err != nil || string(again) != string(encoded) {
			t.Fatalf("policy map is not deterministic: err=%v", err)
		}
	}
}

func TestResearchLoadLegacyTaskBudgetClampsOnlyImplicitSourceCaps(t *testing.T) {
	for _, budget := range []int64{1, 5_000_000, 16 << 20, 32 << 20} {
		t.Run(strconv.FormatInt(budget, 10), func(t *testing.T) {
			isolatedResearchEnv(t)
			t.Setenv("MAX_FETCH_BYTES", strconv.FormatInt(budget, 10))
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxFetchBytes != budget || cfg.MaxSourceBytes != min(int64(16<<20), budget) || cfg.PDFPolicy.MaxInputBytes != min(int64(64<<20), budget) || cfg.SandboxMaxFileBytes != 5_000_000 || cfg.ArtifactMaxBytes != max(cfg.MaxSourceBytes, cfg.PDFPolicy.MaxInputBytes) {
				t.Fatalf("legacy budget=%d source=%d pdf=%d artifact=%d sandbox=%d", budget, cfg.MaxSourceBytes, cfg.PDFPolicy.MaxInputBytes, cfg.ArtifactMaxBytes, cfg.SandboxMaxFileBytes)
			}
			t.Setenv("MAX_PDF_BYTES", strconv.FormatInt(budget+1, 10))
			if _, err := Load(); err == nil {
				t.Fatal("an explicit PDF cap above the task budget must be rejected")
			}
		})
	}
}

func TestResearchLoadOverridesKeepArtifactAndSandboxLimitsIndependent(t *testing.T) {
	isolatedResearchEnv(t)
	for key, value := range map[string]string{"MAX_FETCH_BYTES": "268435456", "MAX_SOURCE_BYTES": "8388608", "MAX_PDF_BYTES": "33554432", "PDF_MAX_PAGES": "12", "PDF_MAX_TEXT_RUNES": "12345", "PDF_EXTRACTION_TIMEOUT_SECONDS": "60", "PDF_DOWNLOAD_TIMEOUT_SECONDS": "300", "PDF_EXTRACTOR_MEMORY_MB": "256", "PDF_EXTRACTOR_TMP_MB": "32", "PDF_ALLOW_UNSANDBOXED_FALLBACK": "true", "ARTIFACT_MAX_BYTES": "104857600", "SANDBOX_MAX_FILE_BYTES": "123456", "FETCH_TEMP_TASK_BYTES": "268435456", "FETCH_TEMP_GLOBAL_BYTES": "536870912"} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArtifactMaxBytes != 100<<20 || cfg.SandboxMaxFileBytes != 123456 || cfg.MaxFetchBytes != 256<<20 || cfg.MaxSourceBytes != 8<<20 || cfg.PDFPolicy.MaxInputBytes != 32<<20 || cfg.PDFPolicy.MaxPages != 12 || cfg.PDFPolicy.MaxTextRunes != 12345 || cfg.PDFPolicy.ExtractionTimeout != time.Minute || cfg.PDFPolicy.MemoryBytes != 256<<20 || cfg.PDFPolicy.TempBytes != 32<<20 || !cfg.PDFPolicy.AllowUnsandboxedFallback || cfg.FetchPolicy().PDFDownloadTimeout != 5*time.Minute {
		t.Fatal("explicit research settings were coupled or discarded")
	}
	for name, policy := range cfg.ResearchPolicies() {
		if policy.TaskFetchBudgetBytes != 256<<20 || policy.SourceFetchBytes != 8<<20 || policy.PDFBytes != 32<<20 || policy.PDFPages != 12 || policy.PDFTextRunes != 12345 || policy.PDFDownloadTimeoutSec != 300 || policy.PDFExtractTimeoutSec != 60 || policy.PDFMemoryBytes != 256<<20 || policy.PDFTempBytes != 32<<20 || policy.PDFCPULimit != cfg.PDFPolicy.CPUs || policy.PDFPIDLimit != cfg.PDFPolicy.PIDs || !policy.AllowUnsandboxedPDF {
			t.Fatalf("profile %s discarded effective fetch/PDF configuration: %#v", name, policy)
		}
	}
}

func TestResearchPolicyValidationRejectsImpossibleRelationships(t *testing.T) {
	isolatedResearchEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := cfg.ResearchProfile("standard")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.ResearchPolicy){
		func(p *domain.ResearchPolicy) { p.Budget.WallTimeSeconds = math.MaxInt64 },
		func(p *domain.ResearchPolicy) { p.Budget.MaxQuestionsAccepted = p.Budget.MaxQuestionsCreated + 1 },
		func(p *domain.ResearchPolicy) { p.Budget.MaxStagnantRounds = p.Budget.MaxRounds + 1 },
		func(p *domain.ResearchPolicy) { p.Budget.MaxCitationSources = p.Budget.MaxEvidence + 1 },
		func(p *domain.ResearchPolicy) { p.SourceFetchBytes = p.TaskFetchBudgetBytes + 1 },
		func(p *domain.ResearchPolicy) { p.PDFBytes = p.TaskFetchBudgetBytes + 1 },
		func(p *domain.ResearchPolicy) { p.PDFCPULimit = math.NaN() },
		func(p *domain.ResearchPolicy) { p.PDFExtractTimeoutSec = math.MaxInt },
		func(p *domain.ResearchPolicy) { p.AdaptiveEnabled = true },
	} {
		invalid := valid
		mutate(&invalid)
		if err := ValidateResearchPolicy(invalid); err == nil {
			t.Fatalf("accepted impossible research policy=%#v", invalid)
		}
	}
	cfg.MaxSourceBytes = cfg.MaxFetchBytes + 1
	if _, err := cfg.ResearchProfile("standard"); err == nil {
		t.Fatal("profile builder accepted a configured source cap above the task budget")
	}
}

func TestResearchLoadRejectsInvalidAndOverflowPolicyValues(t *testing.T) {
	for name, values := range map[string][]string{
		"MAX_FETCH_BYTES":                {"0", "-1", "9223372036854775807", "9223372036854775808", "1.5"},
		"MAX_SOURCE_BYTES":               {"0", "-1", "134217729", "9223372036854775808"},
		"MAX_PDF_BYTES":                  {"0", "-1", "134217729", "9223372036854775808"},
		"PDF_MAX_PAGES":                  {"0", "-1", "9223372036854775808", "twenty"},
		"PDF_MAX_TEXT_RUNES":             {"0", "-1", "9223372036854775807", "9223372036854775808"},
		"PDF_EXTRACTION_TIMEOUT_SECONDS": {"0", "-1", "601", "9223372036854775807", "9223372036854775808", "1.5"},
		"PDF_DOWNLOAD_TIMEOUT_SECONDS":   {"0", "-1", "3601", "9223372036854775807", "9223372036854775808"},
		"PDF_EXTRACTOR_MEMORY_MB":        {"0", "31", "8193", "9223372036854775807", "9223372036854775808"},
		"PDF_EXTRACTOR_TMP_MB":           {"0", "-1", "4097", "9223372036854775807", "9223372036854775808"},
		"PDF_ALLOW_UNSANDBOXED_FALLBACK": {"yes", "", "2"},
		"ARTIFACT_MAX_BYTES":             {"0", "-1", "67108863", "9223372036854775807", "9223372036854775808"},
		"SANDBOX_MAX_FILE_BYTES":         {"0", "-1", "104857601", "9223372036854775807", "9223372036854775808"},
		"FETCH_TEMP_DIR":                 {"", "   "},
		"FETCH_TEMP_TASK_BYTES":          {"0", "-1", "67108863", "268435457", "9223372036854775808"},
		"FETCH_TEMP_GLOBAL_BYTES":        {"0", "-1", "134217727", "9223372036854775808"},
	} {
		for _, value := range values {
			t.Run(name+"="+value, func(t *testing.T) {
				isolatedResearchEnv(t)
				t.Setenv(name, value)
				if _, err := Load(); err == nil {
					t.Fatal("real Load accepted invalid research policy")
				}
			})
		}
	}
}
