package api

import (
	"context"
	"time"
)

func (s *Server) SetResearchPolicy(policy map[string]any, pdfCheck func(context.Context) error) {
	snapshot := map[string]any{"configured": true, "pdf_allow_unsandboxed_fallback": false}
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

func (s *Server) researchPolicyStatus(ctx context.Context) (map[string]any, map[string]any, map[string]string) {
	s.healthMu.RLock()
	policy := map[string]any{"configured": false, "pdf_allow_unsandboxed_fallback": false}
	for key, value := range s.researchPolicy {
		policy[key] = value
	}
	check := s.pdfCheck
	s.healthMu.RUnlock()
	available := false
	if check != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		available = check(checkCtx) == nil
		cancel()
	}
	fallback := policy["pdf_allow_unsandboxed_fallback"] == true
	runtime := map[string]any{"isolated_available": available, "isolated_status": "unavailable", "fallback_allowed": fallback}
	readiness := map[string]string{"status": "warning", "message": "Isolated PDF runtime is unavailable; unsandboxed fallback is disabled"}
	if available {
		runtime["isolated_status"] = "available"
		readiness = map[string]string{"status": "ok", "message": "Local isolated PDF runtime is available; document extraction has not been tested by doctor"}
	} else if fallback {
		readiness["message"] = "Isolated PDF runtime is unavailable; policy permits a process-bounded fallback without OS sandbox isolation"
	}
	return policy, runtime, readiness
}
