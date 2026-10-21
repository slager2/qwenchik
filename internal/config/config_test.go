package config

import (
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsDotEnvFromWorkingDirectory(t *testing.T) {
	clearConfigEnv(t)
	workingDir := t.TempDir()
	chdirForTest(t, workingDir)
	writeDotEnv(t, workingDir, `
# comment
malformed line
=missing-key
APP_BIND_ADDR = 127.0.0.1:8188 # unquoted inline comment
DATABASE_PATH="./data/db # backup.sqlite" # quoted inline comment
LLM_MODEL='local # model' # single-quoted value
SEARXNG_URL="http://127.0.0.1:8081" # configured search URL
`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "127.0.0.1:8188" {
		t.Fatal("unquoted value or inline comment was parsed incorrectly")
	}
	if cfg.DatabasePath != "./data/db # backup.sqlite" {
		t.Fatal("double-quoted value or inline comment was parsed incorrectly")
	}
	if cfg.LLMModel != "local # model" {
		t.Fatal("single-quoted value or inline comment was parsed incorrectly")
	}
	if cfg.SearXNGURL != "http://127.0.0.1:8081" {
		t.Fatal("SEARXNG_URL was not loaded from .env")
	}
}

func TestLoadDotEnvFallsBackToExecutableDirectory(t *testing.T) {
	clearConfigEnv(t)
	workingDir := t.TempDir()
	executableDir := t.TempDir()
	chdirForTest(t, workingDir)
	writeDotEnv(t, workingDir, "APP_BIND_ADDR=127.0.0.1:8188\n")
	writeDotEnv(t, executableDir, "APP_BIND_ADDR=127.0.0.1:8189\nSEARXNG_URL=http://127.0.0.1:8081\n")

	if err := loadDotEnvFromDirs(workingDir, executableDir); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "127.0.0.1:8188" {
		t.Fatal("working-directory .env did not take precedence")
	}
	if cfg.SearXNGURL != "http://127.0.0.1:8081" {
		t.Fatal("executable-directory .env fallback was not loaded")
	}
}

func TestLoadDoesNotOverrideNonEmptyEnvironmentValue(t *testing.T) {
	clearConfigEnv(t)
	workingDir := t.TempDir()
	chdirForTest(t, workingDir)
	writeDotEnv(t, workingDir, "SEARXNG_URL=http://127.0.0.1:8081\n")
	if err := os.Setenv("SEARXNG_URL", "http://127.0.0.1:8082"); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SearXNGURL != "http://127.0.0.1:8082" {
		t.Fatal("non-empty environment value was overwritten by .env")
	}
}

func TestDDGFallbackCanBeExplicitlyDisabled(t *testing.T) {
	clearConfigEnv(t)
	chdirForTest(t, t.TempDir())
	t.Setenv("SEARCH_ENABLE_DDG", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SearchEnableDDG {
		t.Fatal("explicit privacy setting was ignored")
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"APP_BIND_ADDR", "DATABASE_PATH", "ARTIFACTS_PATH", "WORKSPACE_ROOT",
		"SANDBOX_RUNTIME", "SANDBOX_IMAGE", "SANDBOX_NETWORK", "PDF_EXTRACTOR_IMAGE",
		"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL", "LLM_CONTEXT_LENGTH",
		"LLM_MAX_OUTPUT_TOKENS", "LLM_TEMPERATURE", "LLM_TOP_P", "LLM_ENABLE_THINKING",
		"LLM_REQUEST_TIMEOUT_SECONDS", "LLM_MAX_CONCURRENCY", "SEARXNG_URL",
		"TAVILY_API_KEY", "MAX_STEPS", "TASK_TIMEOUT_SECONDS", "MAX_SEARCH_QUERIES",
		"MAX_FETCH_BYTES", "RESEARCH_DEFAULT_PROFILE", "RESEARCH_ADAPTIVE_ENABLED", "ALLOWED_DOMAINS", "SEARCH_ENABLE_DDG", "SEARCH_PROVIDER_POLICY",
		"RESOURCE_GUARD_ENABLED", "MIN_FREE_RAM_BYTES", "MIN_FREE_VRAM_MB", "GPU_TEMP_MAX_C", "PROCESS_RSS_MAX_BYTES", "LLAMA_HEALTH_TIMEOUT_MS",
		"RESOURCE_CHECK_INTERVAL_SECONDS", "RESOURCE_MAX_RESUME_ATTEMPTS", "RESOURCE_BACKOFF_SECONDS", "RESOURCE_BACKOFF_MAX_SECONDS", "WORKER_LEASE_SECONDS", "KEEP_AWAKE",
	} {
		name := name
		value, wasSet := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("clear environment variable: %v", err)
		}
		t.Cleanup(func() {
			var err error
			if wasSet {
				err = os.Setenv(name, value)
			} else {
				err = os.Unsetenv(name)
			}
			if err != nil {
				t.Errorf("restore environment variable: %v", err)
			}
		})
	}
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func writeDotEnv(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTrimsOptionalTavilyAPIKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "  test-key  ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TavilyAPIKey != "test-key" {
		t.Fatalf("Tavily API key was not trimmed")
	}

	t.Setenv("TAVILY_API_KEY", " \t ")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TavilyAPIKey != "" {
		t.Fatal("blank optional Tavily key should disable Tavily")
	}
}

func TestLoadAcceptsOnlyImmutablePDFExtractorImageReferences(t *testing.T) {
	const imageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("PDF_EXTRACTOR_IMAGE", imageID)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PDFExtractorImage != imageID {
		t.Fatalf("PDF extractor image = %q, want configured local image ID", cfg.PDFExtractorImage)
	}

	t.Setenv("PDF_EXTRACTOR_IMAGE", "poppler:latest")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("mutable PDF extractor image validation error = %v", err)
	}
}

func TestValidateRejectsNonLoopbackBindAndLLM(t *testing.T) {
	cfg := Config{
		ResourcePolicy: resources.DefaultPolicy(), WorkerLeaseSeconds: 30,
		BindAddr: "0.0.0.0:8080", DatabasePath: "./data/test.db", ArtifactsPath: "./data/artifacts", WorkspaceRoot: "./data/workspaces", SandboxRuntime: "auto", SandboxImage: "golang:1.23-alpine", SandboxNetwork: "disabled",
		LLMBaseURL: "http://127.0.0.1:8000/v1", LLMModel: "test", LLMContextLength: 8192,
		LLMMaxOutputTokens: 1024, LLMTemperature: 0.2, LLMTopP: 0.8, LLMRequestTimeoutSecs: 30,
		LLMMaxConcurrency: 1, MaxSteps: 8, TaskTimeoutSeconds: 3600, MaxSearchQueries: 12, MaxFetchBytes: 1024,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback bind validation error = %v", err)
	}
	cfg.BindAddr = "127.0.0.1:8080"
	cfg.LLMBaseURL = "https://api.example.com/v1"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "remote LLM") {
		t.Fatalf("remote LLM validation error = %v", err)
	}
	cfg.LLMBaseURL = "http://127.0.0.1:8000/v1"
	cfg.SearXNGURL = "http://search.example.com:8081"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "SEARXNG_URL") {
		t.Fatalf("remote SearXNG validation error = %v", err)
	}
	cfg.SearXNGURL = "http://127.0.0.1:8081"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("local SearXNG validation failed: %v", err)
	}
}
