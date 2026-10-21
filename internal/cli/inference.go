package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/api"
	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/llm/openai"
	"github.com/slager2/qwenchik/internal/workerlock"
)

type doctorReport struct {
	Runtime        map[string]any         `json:"runtime,omitempty"`
	Status         string                 `json:"status"`
	Checks         map[string]doctorCheck `json:"checks"`
	ResearchLimits map[string]any         `json:"research_limits,omitempty"`
	PDFExtractor   map[string]any         `json:"pdf_extractor,omitempty"`
}

func makeDoctorReport(checks map[string]doctorCheck, limits, pdf map[string]any) doctorReport {
	status := "ok"
	for _, check := range checks {
		if check.Status == "error" {
			status = "error"
			break
		}
		if check.Status == "warning" {
			status = "warning"
		}
	}
	return doctorReport{Status: status, Checks: checks, ResearchLimits: limits, PDFExtractor: pdf}
}

func unavailableAppDoctor() doctorReport {
	return makeDoctorReport(map[string]doctorCheck{
		"app": {Status: "warning", Message: "Running application diagnostics are unavailable; local policy and inference were not substituted"},
		"llm": {Status: "warning", Message: "Server model readiness is unknown; standalone inference was skipped"},
	}, nil, nil)
}

func doctorDelegationClient() *http.Client {
	return &http.Client{Timeout: 34 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func runningAppDoctor(ctx context.Context, cfg config.Config) doctorReport {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cfg.BindAddr+"/api/doctor", nil)
	if err != nil {
		return unavailableAppDoctor()
	}
	response, err := doctorDelegationClient().Do(request)
	if err != nil {
		return unavailableAppDoctor()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return unavailableAppDoctor()
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return unavailableAppDoctor()
	}
	var raw doctorReport
	if json.Unmarshal(body, &raw) != nil || len(raw.Checks) == 0 {
		return unavailableAppDoctor()
	}
	checks := map[string]doctorCheck{}
	for _, name := range []string{"app", "worker", "database", "artifacts", "frontend", "sandbox", "llm", "pdf_extractor", "search", "search_probe", "search_searxng", "search_tavily", "search_duckduckgo"} {
		if check, ok := raw.Checks[name]; ok && (check.Status == "ok" || check.Status == "warning" || check.Status == "error") {
			checks[name] = doctorCheck{Status: check.Status, Message: "Running application reported " + check.Status}
		}
	}
	if len(checks) == 0 {
		return unavailableAppDoctor()
	}
	if _, ok := checks["llm"]; !ok {
		checks["llm"] = doctorCheck{Status: "warning", Message: "Server did not report model readiness; standalone inference was skipped"}
	}
	limits := safeDoctorLimits(raw.ResearchLimits)
	if limits == nil || limits["configured"] != true {
		checks["research_policy"] = doctorCheck{Status: "warning", Message: "Running application did not report an effective research policy"}
	}
	var pdf map[string]any
	if available, ok := raw.PDFExtractor["isolated_available"].(bool); ok {
		if fallback, ok := raw.PDFExtractor["fallback_allowed"].(bool); ok {
			state := "unavailable"
			if available {
				state = "available"
			}
			pdf = map[string]any{"isolated_available": available, "isolated_status": state, "fallback_allowed": fallback}
		}
	}
	if pdf == nil {
		checks["pdf_extractor"] = doctorCheck{Status: "warning", Message: "Running application did not report PDF runtime readiness"}
	}
	if raw.Status == "error" || response.StatusCode == http.StatusServiceUnavailable {
		checks["app"] = doctorCheck{Status: "error", Message: "Running application reports a required readiness check in error"}
	} else if raw.Status == "warning" && makeDoctorReport(checks, nil, nil).Status == "ok" {
		checks["app"] = doctorCheck{Status: "warning", Message: "Running application reports a readiness warning"}
	}
	report := makeDoctorReport(checks, limits, pdf)
	report.Runtime = api.SafeRuntimeStatus(raw.Runtime)
	return report
}

func safeDoctorLimits(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := map[string]any{"configured": false, "pdf_allow_unsandboxed_fallback": false}
	for _, key := range []string{"configured", "pdf_allow_unsandboxed_fallback"} {
		if value, ok := input[key].(bool); ok {
			result[key] = value
		}
	}
	for key, maximum := range map[string]float64{
		"task_fetch_budget_bytes": 1 << 40, "source_fetch_bytes": 1 << 40, "pdf_bytes": 1 << 40,
		"pdf_memory_bytes": 1 << 40, "pdf_temp_bytes": 1 << 40, "artifact_max_bytes": 1 << 40,
		"sandbox_max_file_bytes": 1 << 40, "fetch_temp_task_bytes": 1 << 40, "fetch_temp_global_bytes": 1 << 40,
		"pdf_pages": 10000, "pdf_text_runes": 10000000, "pdf_extraction_timeout_seconds": 3600,
		"pdf_download_timeout_seconds": 3600, "pdf_pid_limit": 10000, "pdf_cpu_limit": 64,
	} {
		var value float64
		switch number := input[key].(type) {
		case int:
			value = float64(number)
		case int64:
			value = float64(number)
		case float64:
			value = number
		default:
			continue
		}
		if value > 0 && value <= maximum && !math.IsNaN(value) && !math.IsInf(value, 0) && (key == "pdf_cpu_limit" || math.Trunc(value) == value) {
			result[key] = value
		}
	}
	return result
}

func configuredModelClient(cfg config.Config) *llm.GuardedClient {
	return llm.NewGuardedClient(openai.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, time.Duration(cfg.LLMRequestTimeoutSecs)*time.Second, cfg.LLMMaxOutputTokens, cfg.LLMTemperature, cfg.LLMTopP, cfg.LLMEnableThinking))
}

func doctorModel(ctx context.Context, cfg config.Config, appResponding bool) doctorCheck {
	client := &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	if appResponding {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cfg.BindAddr+"/api/doctor", nil)
		if err != nil {
			return doctorCheck{Status: "error", Message: "local application doctor request is invalid"}
		}
		response, err := client.Do(request)
		if err != nil {
			return doctorCheck{Status: "warning", Message: "Server model check is unavailable; standalone inference was skipped"}
		}
		defer response.Body.Close()
		var result struct {
			Checks map[string]doctorCheck `json:"checks"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil {
			return doctorCheck{Status: "warning", Message: "Server model check is invalid; standalone inference was skipped"}
		}
		if check, ok := result.Checks["llm"]; ok && (check.Status == "ok" || check.Status == "warning" || check.Status == "error") {
			return check
		}
		return doctorCheck{Status: "warning", Message: "Server did not report model readiness; standalone inference was skipped"}
	}
	lock, err := workerlock.Acquire(cfg.DatabasePath + ".worker.lock")
	if err != nil {
		return doctorCheck{Status: "warning", Message: "Worker ownership could not be acquired; standalone inference was skipped"}
	}
	defer lock.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.LLMBaseURL, "/")+"/models", nil)
	if err != nil {
		return doctorCheck{Status: "error", Message: "local model health request is invalid"}
	}
	if cfg.LLMAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	}
	healthCtx, healthCancel := context.WithTimeout(ctx, 3*time.Second)
	defer healthCancel()
	response, err := client.Do(request.WithContext(healthCtx))
	if err != nil {
		return doctorCheck{Status: "error", Message: "local model endpoint is unavailable"}
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return doctorCheck{Status: "error", Message: "local model health request failed"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probeClient := llm.NewGuardedClient(openai.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, 30*time.Second, 24, 0, 1, cfg.LLMEnableThinking))
	err = llm.ProbeStructuredOutput(probeCtx, probeClient)
	if errors.Is(err, llm.ErrInferenceBusy) {
		return doctorCheck{Status: "warning", Message: "Model inference is busy; structured smoke was skipped"}
	}
	if err != nil {
		return doctorCheck{Status: "error", Message: "local model failed the bounded structured JSON smoke request"}
	}
	return doctorCheck{Status: "ok", Message: "local model passed the bounded structured JSON smoke request"}
}
