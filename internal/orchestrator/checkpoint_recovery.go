package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/provenance"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

func (w *Worker) currentCheckpoint(ctx context.Context, taskID string) (checkpointState, error) {
	checkpoint, err := w.repo.LatestCheckpoint(ctx, taskID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return checkpointState{PlanVersion: 1, CompletedSteps: []string{}, ToolResults: map[string]any{}}, nil
	}
	if err != nil {
		return checkpointState{}, err
	}
	encoded, err := json.Marshal(checkpoint.State)
	if err != nil {
		return checkpointState{}, err
	}
	var state checkpointState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return checkpointState{}, fmt.Errorf("decode worker checkpoint: %w", err)
	}
	if state.ToolResults == nil {
		state.ToolResults = make(map[string]any)
	}
	return state, nil
}

func (w *Worker) reconcileResearchState(ctx context.Context, taskID string, state checkpointState) (checkpointState, bool, error) {
	changed := false
	if state.ToolResults == nil {
		state.ToolResults = make(map[string]any)
		changed = true
	}
	checkpoint, checkpointErr := w.repo.LatestCheckpoint(ctx, taskID)
	lastEventSeq := int64(0)
	if checkpointErr == nil {
		lastEventSeq = checkpoint.LastEventSeq
	} else if !errors.Is(checkpointErr, sqlite.ErrNotFound) {
		return checkpointState{}, false, checkpointErr
	}
	events, err := w.planSafetyEvents(ctx, taskID)
	if err != nil {
		return checkpointState{}, false, err
	}
	events = invocationRecoveryEvents(events, state.AttemptStartSeq)
	artifacts, err := w.repo.Artifacts(ctx, taskID)
	if err != nil {
		return checkpointState{}, false, err
	}
	artifactSizes := make(map[string]int64, len(artifacts))
	for _, artifact := range artifacts {
		artifactSizes[artifact.ID] = artifact.SizeBytes
	}
	evidence, err := w.repo.Evidence(ctx, taskID)
	if err != nil {
		return checkpointState{}, false, err
	}
	searchAttempts := 0
	var checkpointFetchBytes, subsequentFetchBytes int64
	addFetchBytes := func(total *int64, count int64) error {
		if count < 0 || count > math.MaxInt64-*total {
			return ErrRecoveryRequired
		}
		*total += count
		return nil
	}
	if state.FetchedBytes < 0 {
		return checkpointState{}, false, ErrRecoveryRequired
	}
	completedEvidenceIDs := make(map[string]struct{})
	startedTools := make(map[string]bool)
	completedSinceCheckpoint := make(map[string]any)
	completedToolByStep := make(map[string]string)
	stepCompletedSinceCheckpoint := make(map[string]bool)
	completedOrder := make([]string, 0)
	seenFailureAttempts := make(map[string]bool)
	publicFailureAttempts := make(map[string]bool)
	for _, event := range events {
		if event.Type == "tool.failed" && event.Visibility == "user" {
			var failure sourceFailure
			if json.Unmarshal(event.Payload, &failure) == nil && failure.AttemptID != "" {
				publicFailureAttempts[failure.AttemptID] = true
			}
		}
	}
	for _, event := range events {
		var payload struct {
			Tool         string          `json:"tool"`
			StepID       string          `json:"step_id"`
			Result       json.RawMessage `json:"result"`
			Summary      string          `json:"summary"`
			FetchedBytes int64           `json:"fetched_bytes"`
			Recovered    bool            `json:"recovered"`
			TargetURL    string          `json:"target_url"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			continue
		}
		if event.Type == "source.updated" && event.Seq > state.AttemptStartSeq {
			var update sourceFailure
			if json.Unmarshal(event.Payload, &update) == nil && update.FallbackAttempted {
				for index := range state.SourceFailures {
					if state.SourceFailures[index].AttemptID == update.AttemptID && !state.SourceFailures[index].FallbackAttempted {
						state.SourceFailures[index].FallbackAttempted = true
						changed = true
					}
				}
			}
		}
		if event.Type == "tool.failed" && payload.Tool == "fetch_url" {
			var failure sourceFailure
			if json.Unmarshal(event.Payload, &failure) == nil && failure.AttemptID != "" {
				if seenFailureAttempts[failure.AttemptID] {
					continue
				}
				seenFailureAttempts[failure.AttemptID] = true
				if event.Seq > state.AttemptStartSeq {
					if applySourceFailure(&state, failure) {
						changed = true
					}
					if !publicFailureAttempts[failure.AttemptID] {
						if _, err := w.repo.RecordEvent(ctx, taskID, "tool.failed", event.Stage, "user", failure.payload()); err != nil {
							return checkpointState{}, false, err
						}
					}
				}
			} else {
				if event.Visibility != "audit" {
					continue
				}
				if event.Seq > state.AttemptStartSeq {
					var legacy struct {
						Failure string `json:"failure"`
					}
					_ = json.Unmarshal(event.Payload, &legacy)
					failure.AttemptID = fmt.Sprintf("legacy-fetch-%d", event.Seq)
					failure.Code = legacy.Failure
					if failure.Code == "unsafe_url" {
						failure.Code = "url_rejected_ssrf"
					}
					if failure.Code == "" {
						failure.Code = "source_unavailable"
					}
					failure.TargetURL, failure.Fingerprint = safeSourceIdentity(payload.TargetURL)
					failure.Status = "failed"
					if applySourceFailure(&state, failure) {
						changed = true
					}
				}
			}
		}
		if event.Type == "tool.started" && payload.Tool == "search" {
			searchAttempts++
		}
		if payload.Tool == "fetch_url" && (event.Type == "tool.completed" || event.Type == "tool.failed") {
			var fetchResult struct {
				EvidenceID   string `json:"evidence_id"`
				FetchedBytes int64  `json:"fetched_bytes"`
			}
			if event.Type == "tool.completed" {
				if json.Unmarshal(payload.Result, &fetchResult) != nil {
					return checkpointState{}, false, ErrRecoveryRequired
				}
			} else {
				fetchResult.FetchedBytes = payload.FetchedBytes
			}
			total := &subsequentFetchBytes
			if event.Seq <= lastEventSeq {
				total = &checkpointFetchBytes
			} else if payload.Recovered {
				for _, item := range evidence {
					if item.ID == fetchResult.EvidenceID && !item.RetrievedAt.After(checkpoint.CreatedAt) {
						total = &checkpointFetchBytes
						break
					}
				}
			}
			if err := addFetchBytes(total, fetchResult.FetchedBytes); err != nil {
				return checkpointState{}, false, err
			}
			if fetchResult.EvidenceID != "" {
				completedEvidenceIDs[fetchResult.EvidenceID] = struct{}{}
			}
		}
		if payload.StepID == "" && state.CurrentStep != "" && payload.Tool == state.PendingTool {
			payload.StepID = state.CurrentStep
		}
		key := payload.StepID + "\x00" + payload.Tool
		if event.Seq <= lastEventSeq {
			if event.Type == "tool.started" {
				startedTools[key] = true
			} else if event.Type == "tool.completed" || event.Type == "tool.failed" {
				delete(startedTools, key)
			}
			continue
		}
		switch event.Type {
		case "tool.failed":
			delete(startedTools, key)
			if payload.Tool == "fetch_url" && payload.StepID == state.CurrentStep && state.PendingTool == "fetch_url" {
				state.PendingTool = ""
				state.PendingToolIdempotent = false
				state.PendingArguments = nil
				state.PendingSummary = ""
				state.PendingTarget = ""
				state.PendingRisk = ""
				state.PendingApproval = false
				if canonical, err := provenance.CanonicalURL(payload.TargetURL); err == nil && !contains(state.FailedFetchURLs, canonical) {
					state.FailedFetchURLs = append(state.FailedFetchURLs, canonical)
				}
				changed = true
			}
		case "tool.started":
			startedTools[key] = true
			delete(completedSinceCheckpoint, payload.StepID)
			delete(completedToolByStep, payload.StepID)
			delete(stepCompletedSinceCheckpoint, payload.StepID)
		case "tool.completed":
			if payload.StepID == "" || payload.Tool == "" || len(payload.Result) == 0 {
				continue
			}
			if !startedTools[key] {
				continue
			}
			var result any
			if json.Unmarshal(payload.Result, &result) != nil {
				return checkpointState{}, false, ErrRecoveryRequired
			}
			if _, exists := completedSinceCheckpoint[payload.StepID]; !exists {
				completedOrder = append(completedOrder, payload.StepID)
			}
			completedSinceCheckpoint[payload.StepID] = result
			completedToolByStep[payload.StepID] = payload.Tool
		case "step.completed":
			if payload.StepID != "" {
				stepCompletedSinceCheckpoint[payload.StepID] = true
			}
		case "step.skipped":
			if payload.StepID != "" && !contains(state.SkippedSteps, payload.StepID) {
				state.SkippedSteps = append(state.SkippedSteps, payload.StepID)
				if err := w.repo.SetStepStatus(ctx, taskID, payload.StepID, "skipped"); err != nil && !errors.Is(err, sqlite.ErrNotFound) {
					return checkpointState{}, false, err
				}
				changed = true
			}
		}
	}
	if searchAttempts > state.SearchQueries {
		state.SearchQueries = searchAttempts
		changed = true
	}
	orphanArtifactIDs := make(map[string]struct{})
	for _, item := range evidence {
		if _, completed := completedEvidenceIDs[item.ID]; completed || item.RawArtifactID == "" {
			continue
		}
		if _, counted := orphanArtifactIDs[item.RawArtifactID]; counted {
			continue
		}
		orphanArtifactIDs[item.RawArtifactID] = struct{}{}
		total := &subsequentFetchBytes
		if !item.RetrievedAt.After(checkpoint.CreatedAt) {
			total = &checkpointFetchBytes
		}
		if err := addFetchBytes(total, artifactSizes[item.RawArtifactID]); err != nil {
			return checkpointState{}, false, err
		}
	}
	reconciledFetchBytes := max(state.FetchedBytes, checkpointFetchBytes)
	if err := addFetchBytes(&reconciledFetchBytes, subsequentFetchBytes); err != nil {
		return checkpointState{}, false, err
	}
	if reconciledFetchBytes > state.FetchedBytes {
		state.FetchedBytes = reconciledFetchBytes
		changed = true
	}
	for _, stepID := range completedOrder {
		result := completedSinceCheckpoint[stepID]
		state.ToolResults[stepID] = result
		if !contains(state.CompletedSteps, stepID) {
			state.CompletedSteps = append(state.CompletedSteps, stepID)
		}
		if !isConditionalSearchRetryStepID(stepID) {
			if err := w.repo.SetStepStatus(ctx, taskID, stepID, "completed"); err != nil && !errors.Is(err, sqlite.ErrNotFound) {
				return checkpointState{}, false, err
			}
		}
		if !stepCompletedSinceCheckpoint[stepID] {
			if _, err := w.repo.RecordEvent(ctx, taskID, "step.completed", stagePointer(executionStage(completedToolByStep[stepID])), "user", map[string]any{
				"step_id": stepID, "summary": "Recovered durable tool result after interruption.",
			}); err != nil {
				return checkpointState{}, false, err
			}
		}
		changed = true
	}
	if state.CurrentStep != "" && completedToolByStep[state.CurrentStep] != "" && (state.PendingTool == "" || completedToolByStep[state.CurrentStep] == state.PendingTool) {
		state.CurrentStep = ""
		state.PendingTool = ""
		state.PendingToolIdempotent = false
		state.PendingArguments = nil
		state.PendingSummary = ""
		state.PendingTarget = ""
		state.PendingRisk = ""
		changed = true
	}
	if state.PendingTool == "" || state.CurrentStep == "" || state.PendingApproval {
		return state, changed, nil
	}
	started := false
	completed := false
	var completedResult json.RawMessage
	for _, event := range events {
		var payload struct {
			Tool   string          `json:"tool"`
			StepID string          `json:"step_id"`
			Result json.RawMessage `json:"result"`
		}
		if event.Seq <= state.AttemptStartSeq || json.Unmarshal(event.Payload, &payload) != nil || payload.Tool != state.PendingTool || (payload.StepID != "" && payload.StepID != state.CurrentStep) {
			continue
		}
		if event.Type == "tool.started" {
			started = true
			completed = false
			completedResult = nil
		} else if event.Type == "tool.completed" && started {
			completed = true
			completedResult = append(json.RawMessage(nil), payload.Result...)
		}
	}
	if !started {
		state.PendingTool = ""
		state.PendingToolIdempotent = false
		state.PendingArguments = nil
		state.PendingSummary = ""
		state.PendingTarget = ""
		state.PendingRisk = ""
		return state, true, nil
	}
	var result any
	if completed {
		if len(completedResult) == 0 || json.Unmarshal(completedResult, &result) != nil {
			return checkpointState{}, false, ErrRecoveryRequired
		}
	} else if started && state.PendingTool == "fetch_url" {
		var arguments struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(state.PendingArguments, &arguments) != nil || arguments.URL == "" {
			return checkpointState{}, false, ErrRecoveryRequired
		}
		var fetched *domain.Evidence
		for index := range evidence {
			if (checkpoint.CreatedAt.IsZero() || evidence[index].RetrievedAt.After(checkpoint.CreatedAt)) && evidenceMatchesURL(evidence[index], arguments.URL) {
				fetched = &evidence[index]
				break
			}
		}
		if fetched == nil {
			return checkpointState{}, false, errFetchOutcomeUnknown
		}
		fetchedBytes := artifactSizes[fetched.RawArtifactID]
		result = recoveredEvidenceResult(*fetched, fetchedBytes)
	} else if started && !state.PendingToolIdempotent {
		return checkpointState{}, false, ErrRecoveryRequired
	}
	if result == nil {
		return state, changed, nil
	}
	if state.PendingTool == "fetch_url" {
		resultMap, ok := result.(map[string]any)
		if !ok {
			return checkpointState{}, false, ErrRecoveryRequired
		}
		if evidenceID, _ := resultMap["evidence_id"].(string); evidenceID == "" {
			return checkpointState{}, false, ErrRecoveryRequired
		}
	}
	stepID := state.CurrentStep
	state.ToolResults[stepID] = result
	if !contains(state.CompletedSteps, stepID) {
		state.CompletedSteps = append(state.CompletedSteps, stepID)
	}
	if !isConditionalSearchRetryStepID(stepID) {
		if err := w.repo.SetStepStatus(ctx, taskID, stepID, "completed"); err != nil {
			return checkpointState{}, false, err
		}
	}
	if !completed {
		if _, err := w.repo.RecordEvent(ctx, taskID, "tool.completed", stagePointer(executionStage(state.PendingTool)), "user", map[string]any{
			"step_id": stepID, "tool": state.PendingTool, "result": result, "recovered": true,
		}); err != nil {
			return checkpointState{}, false, err
		}
	}
	if !stepCompletedSinceCheckpoint[stepID] {
		if _, err := w.repo.RecordEvent(ctx, taskID, "step.completed", stagePointer(executionStage(state.PendingTool)), "user", map[string]any{
			"step_id": stepID, "summary": "Recovered persisted tool result after interruption.",
		}); err != nil {
			return checkpointState{}, false, err
		}
	}
	state.CurrentStep = ""
	state.PendingTool = ""
	state.PendingToolIdempotent = false
	state.PendingApproval = false
	state.PendingArguments = nil
	state.PendingSummary = ""
	state.PendingTarget = ""
	state.PendingRisk = ""
	return state, true, nil
}

func recoveredEvidenceResult(item domain.Evidence, rawBytes int64) map[string]any {
	return map[string]any{
		"evidence_id": item.ID, "url": item.CanonicalURL, "requested_url": item.OriginalURL,
		"title": item.Title, "excerpt": item.Excerpt, "content_sha256": item.ContentSHA256,
		"taint": item.Taint, "fetched_bytes": rawBytes,
	}
}

func evidenceMatchesURL(item domain.Evidence, rawURL string) bool {
	requested, err := provenance.CanonicalURL(rawURL)
	if err != nil {
		return false
	}
	for _, candidate := range []string{item.OriginalURL, item.CanonicalURL} {
		canonical, err := provenance.CanonicalURL(candidate)
		if err == nil && canonical == requested {
			return true
		}
	}
	return false
}
