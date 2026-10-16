package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
)

type Config struct {
	BindAddr              string
	DatabasePath          string
	ArtifactsPath         string
	WorkspaceRoot         string
	SandboxRuntime        string
	SandboxImage          string
	SandboxNetwork        string
	PDFExtractorImage     string
	LLMBaseURL            string
	LLMAPIKey             string
	LLMModel              string
	LLMContextLength      int
	LLMMaxOutputTokens    int
	LLMTemperature        float64
	LLMTopP               float64
	LLMEnableThinking     bool
	LLMRequestTimeoutSecs int
	LLMMaxConcurrency     int
	SearXNGURL            string
	TavilyAPIKey          string
	MaxSteps              int
	TaskTimeoutSeconds    int
	MaxSearchQueries      int
	MaxFetchBytes         int64
	AllowedDomains        []string
}

func Load() (Config, error) {
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}

	cfg := Config{
		BindAddr:          env("APP_BIND_ADDR", "127.0.0.1:8080"),
		DatabasePath:      env("DATABASE_PATH", "./data/qagent.db"),
		ArtifactsPath:     env("ARTIFACTS_PATH", "./data/artifacts"),
		WorkspaceRoot:     env("WORKSPACE_ROOT", "./data/workspaces"),
		SandboxRuntime:    env("SANDBOX_RUNTIME", "auto"),
		SandboxImage:      env("SANDBOX_IMAGE", "golang:1.23-alpine"),
		SandboxNetwork:    env("SANDBOX_NETWORK", "disabled"),
		PDFExtractorImage: strings.TrimSpace(env("PDF_EXTRACTOR_IMAGE", "")),
		LLMBaseURL:        env("LLM_BASE_URL", "http://127.0.0.1:8000/v1"),
		LLMAPIKey:         env("LLM_API_KEY", "EMPTY"),
		LLMModel:          env("LLM_MODEL", "local-qwen"),
		SearXNGURL:        env("SEARXNG_URL", ""),
		TavilyAPIKey:      strings.TrimSpace(env("TAVILY_API_KEY", "")),
	}
	var err error
	if cfg.LLMContextLength, err = intEnv("LLM_CONTEXT_LENGTH", 131072); err != nil {
		return Config{}, err
	}
	if cfg.LLMMaxOutputTokens, err = intEnv("LLM_MAX_OUTPUT_TOKENS", 4096); err != nil {
		return Config{}, err
	}
	if cfg.LLMTemperature, err = floatEnv("LLM_TEMPERATURE", 0.2); err != nil {
		return Config{}, err
	}
	if cfg.LLMTopP, err = floatEnv("LLM_TOP_P", 0.8); err != nil {
		return Config{}, err
	}
	if cfg.LLMEnableThinking, err = boolEnv("LLM_ENABLE_THINKING", false); err != nil {
		return Config{}, err
	}
	if cfg.LLMRequestTimeoutSecs, err = intEnv("LLM_REQUEST_TIMEOUT_SECONDS", 120); err != nil {
		return Config{}, err
	}
	if cfg.LLMMaxConcurrency, err = intEnv("LLM_MAX_CONCURRENCY", 1); err != nil {
		return Config{}, err
	}
	if cfg.MaxSteps, err = intEnv("MAX_STEPS", 12); err != nil {
		return Config{}, err
	}
	if cfg.TaskTimeoutSeconds, err = intEnv("TASK_TIMEOUT_SECONDS", 7200); err != nil {
		return Config{}, err
	}
	if cfg.MaxSearchQueries, err = intEnv("MAX_SEARCH_QUERIES", 12); err != nil {
		return Config{}, err
	}
	if cfg.MaxFetchBytes, err = int64Env("MAX_FETCH_BYTES", 5_000_000); err != nil {
		return Config{}, err
	}
	cfg.AllowedDomains = splitList(os.Getenv("ALLOWED_DOMAINS"))
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadDotEnv() error {
	var dirs []string
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(executable))
	}
	return loadDotEnvFromDirs(dirs...)
}

func loadDotEnvFromDirs(dirs ...string) error {
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			absDir = filepath.Clean(dir)
		}
		if _, ok := seen[absDir]; ok {
			continue
		}
		seen[absDir] = struct{}{}

		contents, err := os.ReadFile(filepath.Join(absDir, ".env"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read .env: %w", err)
		}

		for _, line := range strings.Split(string(contents), "\n") {
			key, value, ok := parseDotEnvLine(line)
			if !ok || strings.ContainsRune(value, '\x00') {
				continue
			}
			if current, ok := os.LookupEnv(key); ok && current != "" {
				continue
			}
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set environment variable from .env: %w", err)
			}
		}
	}
	return nil
}

func parseDotEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	keyText, valueText, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key := strings.TrimSpace(keyText)
	if !validDotEnvKey(key) {
		return "", "", false
	}

	valueText = strings.TrimSpace(valueText)
	if valueText == "" {
		return key, "", true
	}
	if quote := valueText[0]; quote == '\'' || quote == '"' {
		end := dotEnvQuoteEnd(valueText, quote)
		if end < 0 {
			return "", "", false
		}
		remainder := strings.TrimSpace(valueText[end+1:])
		if remainder != "" && remainder[0] != '#' {
			return "", "", false
		}
		return key, valueText[1:end], true
	}

	if comment := dotEnvCommentStart(valueText); comment >= 0 {
		valueText = valueText[:comment]
	}
	return key, strings.TrimSpace(valueText), true
}

func validDotEnvKey(key string) bool {
	if key == "" {
		return false
	}
	if !isASCIIAlpha(key[0]) && key[0] != '_' {
		return false
	}
	for i := 1; i < len(key); i++ {
		if !isASCIIAlpha(key[i]) && (key[i] < '0' || key[i] > '9') && key[i] != '_' {
			return false
		}
	}
	return true
}

func isASCIIAlpha(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func dotEnvQuoteEnd(value string, quote byte) int {
	escaped := false
	for i := 1; i < len(value); i++ {
		if escaped {
			escaped = false
			continue
		}
		if value[i] == '\\' {
			escaped = true
			continue
		}
		if value[i] == quote {
			return i
		}
	}
	return -1
}

func dotEnvCommentStart(value string) int {
	previousWasSpace := true
	for i, r := range value {
		if r == '#' && previousWasSpace {
			return i
		}
		previousWasSpace = unicode.IsSpace(r)
	}
	return -1
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.DatabasePath) == "" {
		return errors.New("DATABASE_PATH must not be empty")
	}
	if strings.TrimSpace(c.ArtifactsPath) == "" {
		return errors.New("ARTIFACTS_PATH must not be empty")
	}
	if strings.TrimSpace(c.WorkspaceRoot) == "" || strings.TrimSpace(c.SandboxImage) == "" {
		return errors.New("WORKSPACE_ROOT and SANDBOX_IMAGE must not be empty")
	}
	if err := pdfextract.ValidateImageReference(c.PDFExtractorImage); err != nil {
		return err
	}
	if c.SandboxRuntime != "auto" && c.SandboxRuntime != "docker" {
		return errors.New("SANDBOX_RUNTIME must be auto or docker in this build")
	}
	if c.SandboxNetwork != "disabled" {
		return errors.New("SANDBOX_NETWORK must remain disabled in this build")
	}
	if err := validateLoopbackAddr(c.BindAddr); err != nil {
		return fmt.Errorf("APP_BIND_ADDR: %w", err)
	}
	llmURL, err := url.Parse(c.LLMBaseURL)
	if err != nil || llmURL.Host == "" || (llmURL.Scheme != "http" && llmURL.Scheme != "https") {
		return errors.New("LLM_BASE_URL must be an absolute http(s) URL")
	}
	if llmURL.User != nil || llmURL.RawQuery != "" || llmURL.Fragment != "" {
		return errors.New("LLM_BASE_URL must not contain user info, query parameters, or a fragment")
	}
	if !isLoopbackHost(llmURL.Hostname()) {
		return errors.New("LLM_BASE_URL must use a loopback host; remote LLM providers are not enabled")
	}
	if c.SearXNGURL != "" {
		searchURL, err := url.Parse(c.SearXNGURL)
		if err != nil || searchURL.Host == "" || searchURL.User != nil || searchURL.RawQuery != "" || searchURL.Fragment != "" || (searchURL.Scheme != "http" && searchURL.Scheme != "https") {
			return errors.New("SEARXNG_URL must be an absolute local http(s) URL without credentials or query")
		}
		if !isLoopbackHost(searchURL.Hostname()) {
			return errors.New("SEARXNG_URL must use a loopback host")
		}
		searchPort := searchURL.Port()
		if searchPort == "" {
			if searchURL.Scheme == "https" {
				searchPort = "443"
			} else {
				searchPort = "80"
			}
		}
		_, appPort, _ := net.SplitHostPort(c.BindAddr)
		if searchPort == appPort {
			return errors.New("SEARXNG_URL must use a port different from APP_BIND_ADDR")
		}
	}
	llmPort := llmURL.Port()
	if llmPort == "" {
		if llmURL.Scheme == "https" {
			llmPort = "443"
		} else {
			llmPort = "80"
		}
	}
	_, appPort, _ := net.SplitHostPort(c.BindAddr)
	if appPort == llmPort {
		return errors.New("LLM_BASE_URL must use a different port from APP_BIND_ADDR")
	}
	if c.LLMModel == "" {
		return errors.New("LLM_MODEL must not be empty")
	}
	if c.LLMContextLength < 1024 || c.LLMMaxOutputTokens < 1 || c.LLMContextLength-c.LLMMaxOutputTokens < 2304 {
		return errors.New("LLM token budgets are invalid")
	}
	if c.LLMTemperature < 0 || c.LLMTemperature > 2 || c.LLMTopP <= 0 || c.LLMTopP > 1 {
		return errors.New("LLM_TEMPERATURE must be 0..2 and LLM_TOP_P must be in (0,1]")
	}
	if c.LLMRequestTimeoutSecs < 1 || c.LLMRequestTimeoutSecs > 3600 {
		return errors.New("LLM_REQUEST_TIMEOUT_SECONDS must be between 1 and 3600")
	}
	if c.LLMMaxConcurrency != 1 {
		return errors.New("LLM_MAX_CONCURRENCY must be 1 for the single-slot runtime")
	}
	for _, domain := range c.AllowedDomains {
		domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
		if domain == "" || strings.ContainsAny(domain, "/:@?# \\[]") || (net.ParseIP(domain) == nil && !strings.Contains(domain, ".")) {
			return errors.New("ALLOWED_DOMAINS must contain hostnames or IP addresses, not URLs or paths")
		}
	}
	if c.MaxSteps < 1 || c.MaxSteps > 12 || c.TaskTimeoutSeconds < 1 || c.MaxSearchQueries < 1 || c.MaxFetchBytes < 1 {
		return errors.New("task and tool budgets are invalid")
	}
	return nil
}

func validateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	if !isLoopbackHost(host) {
		return errors.New("must bind to a loopback address; LAN access requires a future authenticated mode")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func intEnv(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func int64Env(name string, fallback int64) (int64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func floatEnv(name string, fallback float64) (float64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be numeric", name)
	}
	return parsed, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
