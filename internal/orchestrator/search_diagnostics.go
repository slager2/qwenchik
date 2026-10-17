package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"regexp"
	"sync"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/tools"
)

func (w *Worker) executeObservedTool(ctx context.Context, taskID string, stage *domain.Stage, role, name string, arguments json.RawMessage, grant string, stepIDs ...string) (any, error) {
	toolCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var eventErr error
	var observed []research.ProviderOutcome
	var eventStage *domain.Stage
	if stage != nil {
		copy := *stage
		eventStage = &copy
	}
	toolCtx = tools.WithSearchOutcomeObserver(toolCtx, func(outcome research.ProviderOutcome) {
		mu.Lock()
		defer mu.Unlock()
		if eventErr != nil {
			return
		}
		observed = append(observed, outcome)
		payload := safeSearchOutcome(outcome)
		var query struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(arguments, &query) == nil {
			hash := sha256.Sum256([]byte(query.Query))
			payload["query_hash"] = hex.EncodeToString(hash[:])
		}
		if len(stepIDs) > 0 {
			payload["step_id"] = stepIDs[0]
		}
		if !outcome.CheckedAt.IsZero() {
			payload["finished_at"] = outcome.CheckedAt
			payload["started_at"] = outcome.CheckedAt.Add(-time.Duration(max(int64(0), outcome.LatencyMS)) * time.Millisecond)
		}
		_, eventErr = w.repo.RecordEvent(toolCtx, taskID, "search.provider", eventStage, "user", payload)
		if eventErr != nil {
			cancel()
		}
	})
	if name == "fetch_url" {
		toolCtx = tools.WithExtractionObserver(toolCtx, func() error {
			if err := w.ensureActive(toolCtx, taskID); err != nil {
				return err
			}
			extractStage := domain.StageExtract
			_, err := w.repo.TransitionTask(toolCtx, taskID, domain.StatusRunning, &extractStage, "task.status_changed", "user", nil)
			if err == nil && stage != nil {
				*stage = extractStage
			}
			return err
		})
	}
	var result any
	var err error
	if grant != "" {
		result, err = w.tools.ExecuteApproved(toolCtx, role, name, arguments, grant)
	} else {
		result, err = w.tools.Execute(toolCtx, role, name, arguments)
	}
	mu.Lock()
	defer mu.Unlock()
	if eventErr != nil {
		return nil, eventErr
	}
	var failure *research.SearchFailure
	if name == "search" && err != nil && len(observed) > 0 && !errors.As(err, &failure) && (errors.Is(err, research.ErrSearchUnavailable) || errors.Is(err, research.ErrSearchFilteredByPolicy) || errors.Is(err, research.ErrSearchResponseTooBig)) {
		code := "search_unavailable"
		if errors.Is(err, research.ErrSearchFilteredByPolicy) {
			code = "search_filtered_by_policy"
		} else if errors.Is(err, research.ErrSearchResponseTooBig) {
			code = "search_response_too_big"
		}
		err = fmt.Errorf("%w: %w", err, &research.SearchFailure{Code: code, Outcomes: observed})
	}
	return result, err
}

func safeSearchOutcome(outcome research.ProviderOutcome) map[string]any {
	provider := outcome.Provider
	switch provider {
	case "searxng", "tavily", "duckduckgo", "search":
	default:
		provider = "search"
	}
	state := outcome.State
	switch state {
	case "healthy", "degraded", "blocked", "unavailable", "misconfigured", "malformed", "configured", "ok", "no_results", "filtered":
	default:
		state = "unavailable"
	}
	code, message := safeSearchCode(outcome.Code)
	return map[string]any{
		"provider": provider, "state": state, "code": code, "message": message,
		"retryable": outcome.Retryable, "http_status": outcome.HTTPStatus,
		"latency_ms": max(int64(0), outcome.LatencyMS), "checked_at": outcome.CheckedAt,
		"result_count": max(0, outcome.ResultCount), "distinct_domains": max(0, outcome.DistinctDomains),
		"body_size": max(int64(0), outcome.BodySize), "content_type": safeDiagnosticContentType(outcome.ContentType),
		"parser_version": safeDiagnosticParserVersion(outcome.ParserVersion),
	}
}

var diagnosticParserVersionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,55}-v[0-9]{1,6}$`)

func safeDiagnosticParserVersion(value string) string {
	if diagnosticParserVersionPattern.MatchString(value) {
		return value
	}
	return ""
}

func safeDiagnosticContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	switch mediaType {
	case "text/html", "text/plain", "application/json", "application/xhtml+xml", "application/xml", "text/xml", "application/octet-stream":
		return mediaType
	default:
		return ""
	}
}

func safeSearchCode(code string) (string, string) {
	switch code {
	case "search_ok":
		return code, "Provider returned results."
	case "search_no_results":
		return code, "Provider returned no results."
	case "provider_blocked":
		return code, "Provider blocked the request or requires an interactive challenge."
	case "provider_malformed":
		return code, "Provider returned an unexpected response."
	case "provider_misconfigured":
		return code, "Provider configuration or credentials require attention."
	case "provider_rate_limited":
		return code, "Provider rate limit reached."
	case "search_filtered_by_policy":
		return code, "All results were excluded by search policy."
	case "search_response_too_big":
		return code, "Provider response exceeded the size limit."
	case "provider_not_checked":
		return code, "Configured provider has not been checked."
	default:
		return "search_unavailable", "Provider search is unavailable."
	}
}

func searchFailureMetadata(err error) (string, []map[string]any) {
	var failure *research.SearchFailure
	if !errors.As(err, &failure) {
		if errors.Is(err, research.ErrSearchFilteredByPolicy) {
			return "search_filtered_by_policy", []map[string]any{}
		}
		if errors.Is(err, research.ErrSearchResponseTooBig) {
			return "search_response_too_big", []map[string]any{}
		}
		if errors.Is(err, research.ErrSearchUnavailable) || errors.Is(err, errSearchUnavailable) {
			return "search_unavailable", []map[string]any{}
		}
		return "", nil
	}
	code, _ := safeSearchCode(failure.Code)
	if code == "search_ok" || code == "provider_not_checked" {
		code = "search_unavailable"
	}
	outcomes := make([]map[string]any, 0, len(failure.Outcomes))
	for _, outcome := range failure.Outcomes {
		outcomes = append(outcomes, safeSearchOutcome(outcome))
	}
	if code == "search_unavailable" && len(outcomes) > 0 {
		candidate := outcomes[0]["code"].(string)
		if candidate == "provider_blocked" || candidate == "provider_malformed" || candidate == "provider_misconfigured" || candidate == "provider_rate_limited" {
			allSame := true
			for _, outcome := range outcomes[1:] {
				allSame = allSame && outcome["code"] == candidate
			}
			if allSame {
				code = candidate
			}
		}
	}
	return code, outcomes
}

func searchesExhaustedWithoutResults(plan domain.Plan, state checkpointState) bool {
	if len(collectEvidenceIDs(state.ToolResults)) > 0 {
		return false
	}
	searches := 0
	for _, step := range plan.Subtasks {
		if step.Kind != "search" {
			continue
		}
		searches++
		result, exists := state.ToolResults[step.ID]
		if !exists || !contains(state.CompletedSteps, step.ID) || !searchResultIsSuccessfulEmpty(result) {
			return false
		}
	}
	retry, exists := state.ToolResults[conditionalSearchRetryResultKey(firstSearchStepID(plan))]
	return searches > 0 && exists && searchResultIsSuccessfulEmpty(retry)
}

func (w *Worker) searchNoResultsFailure(ctx context.Context, taskID string) error {
	events, err := w.planSafetyEvents(ctx, taskID)
	if err != nil {
		return err
	}
	outcomes := make([]research.ProviderOutcome, 0)
	for _, event := range events {
		if event.Type != "search.provider" {
			continue
		}
		var outcome research.ProviderOutcome
		if err := json.Unmarshal(event.Payload, &outcome); err != nil {
			return err
		}
		outcomes = append(outcomes, outcome)
	}
	return &research.SearchFailure{Code: "search_no_results", Outcomes: outcomes}
}
