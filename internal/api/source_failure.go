package api

import (
	"encoding/json"
	"math"
	"mime"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/tools"
)

var safeSourceCodes = map[string]bool{
	"url_rejected_ssrf": true, "url_validation_failed": true, "search_validation_timeout": true,
	"fetch_timeout": true, "fetch_dns_failed": true, "fetch_failed": true, "fetch_source_unavailable": true, "fetch_request_failed": true,
	"fetch_budget_exceeded": true, "task_fetch_budget_exceeded": true, "source_response_too_large": true,
	"pdf_source_too_large": true, "pdf_extraction_input_too_large": true, "pdf_page_limit_exceeded": true,
	"pdf_text_limit_exceeded": true, "pdf_resource_exceeded": true, "pdf_timeout": true,
	"pdf_extractor_unavailable": true, "pdf_malformed": true, "pdf_encrypted": true, "fetch_outcome_unknown": true,
	"extraction_empty": true, "empty_after_extraction": true, "extraction_challenge": true,
	"challenge_detected": true, "unsupported_content": true, "unsupported_mime": true,
	"pdf_extraction_failed": true, "source_unavailable": true, "verification_incomplete": true, "source_failed": true,
	"pdf_no_text": true, "invalid_tool_arguments": true, "approval_required": true, "approval_expired": true,
	"tool_denied": true, "unknown_research_target": true,
}

func sourceErrorCode(code string) string {
	if code == "" || safeSourceCodes[code] {
		return code
	}
	if code == "unsafe_url" {
		return "url_rejected_ssrf"
	}
	return "source_failed"
}

func boundedNumber(value any, maximum float64, integer bool) (float64, bool) {
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case int:
		number = float64(v)
	case int64:
		number = float64(v)
	case json.Number:
		var err error
		number, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && number <= maximum && (!integer || math.Trunc(number) == number)
}

func safeSourceFields(input map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"attempt_id", "step_id"} {
		if value, ok := input[key].(string); ok && safeStepID(value) {
			result[key] = value
		}
	}
	if input["tool"] == "fetch_url" {
		result["tool"] = "fetch_url"
	}
	for _, key := range []string{"retryable", "try_next_source", "user_retry_task", "fallback_attempted"} {
		if value, ok := input[key].(bool); ok {
			result[key] = value
		}
	}
	for _, key := range []string{"declared_bytes", "effective_limit_bytes", "fetched_bytes", "remaining_candidates", "plan_attempt"} {
		maximum := float64(1 << 40)
		if key == "remaining_candidates" || key == "plan_attempt" {
			maximum = 1_000_000
		}
		if value, ok := boundedNumber(input[key], maximum, true); ok {
			result[key] = value
		}
	}
	for _, key := range []string{"error_code", "code", "failure"} {
		if code, ok := input[key].(string); ok && code != "" {
			result["error_code"] = sourceErrorCode(code)
			break
		}
	}
	if value, ok := input["status"].(string); ok {
		switch value {
		case "discovered", "validated", "rejected", "fetching", "extracted", "cited", "failed":
			result["status"] = value
		}
	}
	if value, ok := input["timestamp"].(string); ok {
		if stamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
			result["timestamp"] = stamp.UTC().Format(time.RFC3339Nano)
		}
	}
	if value, ok := input["mime_type"].(string); ok {
		if mediaType, _, err := mime.ParseMediaType(value); err == nil {
			switch mediaType {
			case "application/pdf", "text/html", "application/xhtml+xml", "text/plain", "application/json", "application/octet-stream":
				result["mime_type"] = mediaType
			}
		}
	}
	var rawURL string
	for _, key := range []string{"target_url", "url"} {
		if value, ok := input[key].(string); ok && value != "" {
			rawURL = value
			break
		}
	}
	var fingerprint string
	for _, key := range []string{"source_fingerprint", "fingerprint"} {
		if value, ok := input[key].(string); ok && validFingerprint(value) {
			fingerprint = value
			break
		}
	}
	if rawURL != "" {
		card := tools.SafeSearchHitObservation(research.SearchHitObservation{URL: rawURL, Status: "validated"})
		if card.URL != "" {
			result["target_url"] = card.URL
		}
		if fingerprint == "" {
			fingerprint = sourceFingerprint(rawURL)
		}
	}
	if fingerprint != "" {
		result["source_fingerprint"] = fingerprint
	}
	return result
}

func sourceLifecycleEvent(event domain.Event) (map[string]any, bool) {
	switch event.Type {
	case "tool.started", "tool.failed", "source.updated", "source.fetch.started", "source.fetch.failed", "source.failed", "source.extracted":
	default:
		return nil, false
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return nil, false
	}
	if (event.Type == "tool.started" || event.Type == "tool.failed") && payload["tool"] != "fetch_url" {
		return nil, false
	}
	result := safeSourceFields(payload)
	switch event.Type {
	case "tool.started", "source.fetch.started":
		result["status"] = "fetching"
	case "tool.failed", "source.fetch.failed", "source.failed":
		if result["status"] != "rejected" {
			result["status"] = "failed"
		}
	case "source.extracted":
		result["status"] = "extracted"
	}
	if result["timestamp"] == nil && !event.CreatedAt.IsZero() {
		result["timestamp"] = event.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return result, true
}

func safeResearchWarning(payload json.RawMessage) map[string]any {
	var input map[string]any
	_ = json.Unmarshal(payload, &input)
	result := map[string]any{"code": "research_warning"}
	if input["code"] == "sources_skipped" {
		result["code"] = "sources_skipped"
	}
	if partial, ok := input["partial"].(bool); ok {
		result["partial"] = partial
	}
	failures := []map[string]any{}
	byAttempt := map[string]int{}
	if items, ok := input["source_failures"].([]any); ok {
		for _, item := range items {
			if len(failures) == 256 {
				result["source_failures_truncated"] = true
				break
			}
			if fields, ok := item.(map[string]any); ok {
				safe := safeSourceFields(fields)
				if safe["source_fingerprint"] == nil && safe["attempt_id"] == nil {
					continue
				}
				if safe["status"] != "failed" && safe["status"] != "rejected" {
					continue
				}
				if attempt, ok := safe["attempt_id"].(string); ok {
					if index, exists := byAttempt[attempt]; exists {
						for key, value := range safe {
							if key != "fallback_attempted" || failures[index][key] != true {
								failures[index][key] = value
							}
						}
						continue
					}
					byAttempt[attempt] = len(failures)
				}
				failures = append(failures, safe)
			}
		}
	}
	result["source_failures"] = failures
	return result
}
