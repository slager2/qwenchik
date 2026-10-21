package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/provenance"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/tools"
)

type sourceFailure struct {
	AttemptID           string    `json:"attempt_id"`
	PlanAttempt         int       `json:"plan_attempt"`
	StepID              string    `json:"step_id"`
	Tool                string    `json:"tool"`
	TargetURL           string    `json:"target_url,omitempty"`
	Fingerprint         string    `json:"source_fingerprint"`
	Status              string    `json:"status"`
	Code                string    `json:"error_code"`
	Phase               string    `json:"phase,omitempty"`
	DeclaredBytes       *int64    `json:"declared_bytes"`
	EffectiveLimitBytes int64     `json:"effective_limit_bytes"`
	FetchedBytes        int64     `json:"fetched_bytes"`
	MIMEType            string    `json:"mime_type"`
	LimitKind           string    `json:"limit_kind,omitempty"`
	Retryable           bool      `json:"retryable"`
	TryNextSource       bool      `json:"try_next_source"`
	UserRetryTask       bool      `json:"user_retry_task"`
	RemainingCandidates int       `json:"remaining_candidates"`
	CandidateIndex      int       `json:"candidate_index"`
	FallbackAttempted   bool      `json:"fallback_attempted"`
	Timestamp           time.Time `json:"timestamp"`
}

func sourceFallbackCode(code string) bool {
	switch code {
	case "source_response_too_large", "pdf_source_too_large", "pdf_extraction_input_too_large", "pdf_page_limit_exceeded", "pdf_text_limit_exceeded", "pdf_resource_exceeded", "pdf_timeout", "pdf_extractor_unavailable", "pdf_malformed", "pdf_encrypted", "pdf_no_text", "pdf_extraction_failed", "source_unavailable", "fetch_source_unavailable", "fetch_request_failed", "fetch_dns_failed", "fetch_timeout", "extraction_empty", "extraction_challenge", "unsupported_content":
		return true
	}
	return false
}

func sameURLRetryable(code string) bool {
	switch code {
	case "source_unavailable", "fetch_source_unavailable", "fetch_request_failed", "fetch_dns_failed", "fetch_timeout":
		return true
	}
	return false
}

func knownFetchFailureCode(code string) bool {
	if sourceFallbackCode(code) {
		return true
	}
	switch code {
	case "task_fetch_budget_exceeded", "url_rejected_ssrf", "invalid_tool_arguments", "approval_required", "approval_expired", "tool_denied", "unknown_research_target":
		return true
	}
	return false
}

func fetchFailureCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, research.ErrUnsafeURL) {
		return "url_rejected_ssrf"
	}
	if errors.Is(err, research.ErrDNSLookupFailed) {
		return "fetch_dns_failed"
	}
	if errors.Is(err, tools.ErrInvalidArguments) {
		return "invalid_tool_arguments"
	}
	if errors.Is(err, tools.ErrToolDenied) {
		return "tool_denied"
	}
	if errors.Is(err, tools.ErrUnknownTarget) {
		return "unknown_research_target"
	}
	if errors.Is(err, tools.ErrApprovalRequired) {
		return "approval_required"
	}
	if errors.Is(err, errFetchBudgetExceeded) {
		return "task_fetch_budget_exceeded"
	}
	var failure *research.FetchFailure
	if errors.As(err, &failure) && knownFetchFailureCode(failure.Code) {
		return failure.Code
	}
	for _, item := range []struct {
		cause error
		code  string
	}{
		{pdfextract.ErrInputTooLarge, "pdf_extraction_input_too_large"},
		{pdfextract.ErrTextTooLarge, "pdf_text_limit_exceeded"},
		{pdfextract.ErrOutputExceeded, "pdf_resource_exceeded"},
		{pdfextract.ErrResourceExceeded, "pdf_resource_exceeded"},
		{pdfextract.ErrTimeout, "pdf_timeout"},
		{pdfextract.ErrMalformed, "pdf_malformed"},
		{pdfextract.ErrEncrypted, "pdf_encrypted"},
		{pdfextract.ErrTooManyPages, "pdf_page_limit_exceeded"},
		{pdfextract.ErrUnavailable, "pdf_extractor_unavailable"},
		{pdfextract.ErrNoText, "pdf_no_text"},
		{research.ErrPDFEncrypted, "pdf_encrypted"},
		{research.ErrPDFTooManyPages, "pdf_page_limit_exceeded"},
		{research.ErrPDFExtractorUnavailable, "pdf_extractor_unavailable"},
		{research.ErrPDFNoText, "pdf_no_text"},
		{research.ErrPDFTooLarge, "pdf_extraction_input_too_large"},
		{research.ErrPDFExtractionFailed, "pdf_extraction_failed"},
		{research.ErrResponseTooBig, "source_response_too_large"},
		{research.ErrExtractionEmpty, "extraction_empty"},
		{research.ErrExtractionChallenge, "extraction_challenge"},
		{research.ErrUnsupportedMIME, "unsupported_content"},
		{research.ErrFetchSourceUnavailable, "source_unavailable"},
		{research.ErrFetchRequestFailed, "source_unavailable"},
		{context.DeadlineExceeded, "fetch_timeout"},
	} {
		if errors.Is(err, item.cause) {
			return item.code
		}
	}
	var networkError net.Error
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &networkError) {
		return "source_unavailable"
	}
	return ""
}

func (f sourceFailure) payload() map[string]any {
	encoded, _ := json.Marshal(f)
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	_ = decoder.Decode(&payload)
	payload["code"], payload["failure"] = f.Code, f.Code
	payload["message"] = publicError(f.Code)
	return payload
}

func clearPendingFetch(state *checkpointState) {
	state.PendingTool = ""
	state.PendingToolIdempotent = false
	state.PendingApproval = false
	state.PendingArguments = nil
	state.PendingSummary = ""
	state.PendingTarget = ""
	state.PendingRisk = ""
}

func applySourceFailure(state *checkpointState, failure sourceFailure) bool {
	for _, previous := range state.SourceFailures {
		if previous.AttemptID == failure.AttemptID {
			return false
		}
	}
	state.SourceFailures = append(state.SourceFailures, failure)
	state.Partial = true
	if state.CurrentStep == failure.StepID {
		state.CurrentCandidate = max(state.CurrentCandidate, failure.CandidateIndex)
	}
	if failure.TargetURL != "" {
		if canonical, err := provenance.CanonicalURL(failure.TargetURL); err == nil {
			if !contains(state.FailedFetchURLs, canonical) {
				state.FailedFetchURLs = append(state.FailedFetchURLs, canonical)
			}
			if !contains(state.RejectedFetchURLs, canonical) {
				state.RejectedFetchURLs = append(state.RejectedFetchURLs, canonical)
			}
		}
	}
	return true
}

func safeSourceIdentity(rawURL string) (string, string) {
	canonical, err := provenance.CanonicalURL(rawURL)
	if err != nil {
		canonical = rawURL
	}
	hash := sha256.Sum256([]byte(canonical))
	fingerprint := hex.EncodeToString(hash[:])
	parsed, err := url.Parse(rawURL)
	if err != nil || research.URLHasSensitiveData(parsed) || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fingerprint
	}
	return canonical, fingerprint
}

func filterFailedSourceIdentities(urls []string, failures []sourceFailure) []string {
	rejected := make(map[string]bool, len(failures))
	for _, failure := range failures {
		rejected[failure.Fingerprint] = true
	}
	filtered := make([]string, 0, len(urls))
	for _, rawURL := range urls {
		_, fingerprint := safeSourceIdentity(rawURL)
		if !rejected[fingerprint] {
			filtered = append(filtered, rawURL)
		}
	}
	return filtered
}

func (w *Worker) deferFailedFetchToDiscovery(ctx context.Context, task domain.Task, goal string, plan domain.Plan, step domain.PlanStep, stage *domain.Stage, state *checkpointState, requiredURL bool) (bool, error) {
	if requiredURL || state.PendingTool != "" || state.FetchedBytes >= w.options.MaxFetchBytes {
		return false, nil
	}
	failed := false
	for _, failure := range state.SourceFailures {
		if failure.StepID != step.ID {
			continue
		}
		if !sourceFallbackCode(failure.Code) {
			return false, nil
		}
		failed = true
	}
	if !failed {
		return false, nil
	}
	reachable := make(map[string]bool)
	completedSearches := 0
	for _, candidate := range plan.Subtasks {
		if contains(state.CompletedSteps, candidate.ID) {
			reachable[candidate.ID] = true
			if candidate.Kind == "search" {
				completedSearches++
			}
		} else if candidate.Kind == "search" && contains(state.SkippedSteps, candidate.ID) {
			reachable[candidate.ID] = true
		}
	}
	future := false
	searches, fetches := 0, 0
	for _, candidate := range plan.Subtasks {
		if candidate.ID == step.ID {
			future = true
			continue
		}
		if !future || contains(state.CompletedSteps, candidate.ID) || contains(state.SkippedSteps, candidate.ID) {
			continue
		}
		ready := true
		for _, dependency := range candidate.Dependencies {
			if !reachable[dependency] {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		switch candidate.Kind {
		case "search":
			searches++
		case "fetch":
			fetches++
		default:
			continue
		}
		reachable[candidate.ID] = true
	}
	neededSearches := max(1, minimumResearchSearches(task.Title+"\n"+goal)-completedSearches)
	neededFetches := max(1, minimumResearchFetches(task.Title+"\n"+goal)-distinctResearchSourceCount(state.ToolResults))
	if searches < neededSearches || fetches < neededFetches || w.options.MaxSearchQueries-state.SearchQueries < neededSearches {
		return false, nil
	}
	if err := w.ensureActive(ctx, task.ID); err != nil {
		return false, err
	}
	state.CurrentStep = ""
	if err := w.skipPlanStep(ctx, task.ID, stage, step, "source unavailable; fallback discovery pending in independent planned steps", state); err != nil {
		return false, err
	}
	return true, nil
}

func (w *Worker) recordSourceFailure(ctx context.Context, taskID string, stage *domain.Stage, stepID, rawURL string, cause error, code string, rejected, tryNext bool, remaining int, state *checkpointState) error {
	id, err := newSynthesisID()
	if err != nil {
		return err
	}
	safeURL, fingerprint := safeSourceIdentity(rawURL)
	failure := sourceFailure{
		AttemptID: id, PlanAttempt: state.PlanAttempt, StepID: stepID, Tool: "fetch_url",
		Fingerprint: fingerprint, TargetURL: safeURL, Status: "failed", Code: code,
		Retryable: sameURLRetryable(code), TryNextSource: tryNext, UserRetryTask: true,
		RemainingCandidates: remaining, CandidateIndex: state.CurrentCandidate,
		FallbackAttempted: state.CurrentCandidate > 0, Timestamp: time.Now().UTC(),
	}
	if rejected {
		failure.Status = "rejected"
	}
	var metadata *research.FetchFailure
	if errors.As(cause, &metadata) {
		failure.Phase, failure.DeclaredBytes = metadata.Phase, metadata.DeclaredBytes
		failure.EffectiveLimitBytes, failure.FetchedBytes = metadata.EffectiveLimitBytes, metadata.FetchedBytes
		failure.MIMEType, failure.LimitKind = metadata.MIMEType, metadata.LimitKind
	}
	if failure.FetchedBytes < 0 || state.FetchedBytes < 0 || failure.FetchedBytes > math.MaxInt64-state.FetchedBytes {
		return ErrRecoveryRequired
	}
	if _, err := w.repo.RecordEvent(ctx, taskID, "tool.failed", stage, "audit", failure.payload()); err != nil {
		return err
	}
	state.FetchedBytes += failure.FetchedBytes
	applySourceFailure(state, failure)
	clearPendingFetch(state)
	if _, err := w.repo.SaveCheckpoint(ctx, taskID, 1, state); err != nil {
		return err
	}
	_, err = w.repo.RecordEvent(ctx, taskID, "tool.failed", stage, "user", failure.payload())
	return err
}
