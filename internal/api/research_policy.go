package api

import (
	"context"
	"encoding/hex"
	"github.com/slager2/qwenchik/internal/domain"
	goruntime "runtime"
	"time"
)

func (s *Server) SetResearchPolicy(policy map[string]any, pdfCheck func(context.Context) error) {
	snapshot := map[string]any{"configured": true, "pdf_allow_unsandboxed_fallback": false}
	for _, key := range []string{"adaptive_enabled", "sectioned_report_enabled"} {
		if value, ok := policy[key].(bool); ok {
			snapshot[key] = value
		}
	}
	if hash, ok := policy["offline_corpus_sha256"].(string); ok && len(hash) == 64 {
		if _, err := hex.DecodeString(hash); err == nil {
			snapshot["offline_corpus_sha256"] = hash
		}
	}
	if mode, ok := policy["privacy_mode"].(string); ok && (mode == domain.PrivacyOffline || mode == domain.PrivacyLocalSearXNG || mode == domain.PrivacyExternalSearch) {
		snapshot["privacy_mode"] = mode
	}
	for _, key := range []string{"task_fetch_budget_bytes", "source_fetch_bytes", "pdf_bytes", "pdf_memory_bytes", "pdf_temp_bytes", "artifact_max_bytes", "sandbox_max_file_bytes", "fetch_temp_task_bytes", "fetch_temp_global_bytes"} {
		if value, ok := boundedNumber(policy[key], 1<<40, true); ok && value > 0 {
			snapshot[key] = value
		}
	}
	for key, maximum := range map[string]float64{"pdf_pages": 10000, "pdf_text_runes": 10000000, "pdf_extraction_timeout_seconds": 3600, "pdf_download_timeout_seconds": 3600, "pdf_pid_limit": 10000} {
		if value, ok := boundedNumber(policy[key], maximum, true); ok && value > 0 {
			snapshot[key] = value
		}
	}
	if value, ok := boundedNumber(policy["pdf_cpu_limit"], 64, false); ok && value > 0 {
		snapshot["pdf_cpu_limit"] = value
	}
	if value, ok := policy["pdf_allow_unsandboxed_fallback"].(bool); ok {
		snapshot["pdf_allow_unsandboxed_fallback"] = value
	}
	s.healthMu.Lock()
	s.researchPolicy, s.pdfCheck = snapshot, pdfCheck
	s.healthMu.Unlock()
}

func (s *Server) SetFetchTempCheck(check func(context.Context) error) {
	s.healthMu.Lock()
	s.fetchTempCheck = check
	s.healthMu.Unlock()
}

func (s *Server) researchPolicyStatus(ctx context.Context) (map[string]any, map[string]any, map[string]string) {
	s.healthMu.RLock()
	policy := map[string]any{"configured": false, "pdf_allow_unsandboxed_fallback": false}
	for key, value := range s.researchPolicy {
		policy[key] = value
	}
	check := s.pdfCheck
	s.healthMu.RUnlock()
	isWindows := goruntime.GOOS == "windows"
	available := false
	if check != nil && !isWindows {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		available = check(checkCtx) == nil
		cancel()
	}
	fallback := pdfFallbackAvailable(policy["pdf_allow_unsandboxed_fallback"] == true, goruntime.GOOS)
	runtimeStatus := map[string]any{"isolated_available": available, "isolated_status": "unavailable", "fallback_allowed": fallback}
	runtimeStatus["state"] = "unavailable"
	runtimeStatus["pure_go_fallback_available"] = fallback
	runtimeStatus["smoke_tested"] = false
	if available {
		runtimeStatus["state"] = "isolated_available"
	} else if fallback {
		runtimeStatus["state"] = "pure_go_fallback_available"
	}
	readiness := map[string]string{"status": "warning", "message": "Isolated PDF runtime is unavailable; unsandboxed fallback is disabled"}
	if isWindows {
		readiness["message"] = "PDF extraction is unsupported on Windows; neither isolated execution nor the pure-Go fallback is available"
	} else if available {
		runtimeStatus["isolated_status"] = "available"
		readiness = map[string]string{"status": "ok", "message": "Local isolated PDF runtime is available; document extraction has not been tested by doctor"}
	} else if fallback {
		readiness["message"] = "Isolated PDF runtime is unavailable; policy permits a process-bounded fallback without OS sandbox isolation"
	}
	return policy, runtimeStatus, readiness
}

func pdfFallbackAvailable(policyAllows bool, goos string) bool {
	return policyAllows && goos != "windows"
}
