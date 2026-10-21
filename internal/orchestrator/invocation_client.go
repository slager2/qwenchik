package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type invocationRepository interface {
	BeginInvocation(context.Context, domain.Invocation) (domain.Invocation, error)
	FinishInvocation(context.Context, domain.Invocation, any) error
	InvocationsForOperation(context.Context, string, string) ([]domain.Invocation, error)
}

type invocationClient struct {
	repo  invocationRepository
	inner llm.Client
	runID string
}

func newInvocationClient(repo any, inner llm.Client, runID string) llm.Client {
	if journal, ok := repo.(invocationRepository); ok {
		return &invocationClient{repo: journal, inner: inner, runID: runID}
	}
	return inner
}

func (c *invocationClient) OwnsInvocationReservations() bool { return true }

func ownsInvocationReservations(client llm.Client) bool {
	owner, ok := client.(interface{ OwnsInvocationReservations() bool })
	return ok && owner.OwnsInvocationReservations()
}

func invocationHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(raw) > 2<<20 {
		return "", errInputBudgetExceeded
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func modelInvocationHash(request llm.ChatRequest) (string, error) {
	return invocationHash(struct {
		Model, BaseURL          string
		Stop                    []string
		Messages                []llm.ChatMessage
		Schema                  json.RawMessage
		JSONMode                bool
		MaxTokens               int
		Temperature, TopP       float64
		TemperatureSet, TopPSet bool
	}{request.Model, request.BaseURL, request.Stop, request.Messages, request.JSONSchema, request.JSONMode, request.MaxTokens, request.Temperature, request.TopP, request.TemperatureSet, request.TopPSet})
}

func invocationScope(ctx context.Context, repo any, taskID string) (string, string, int, error) {
	step, phase, planAttempt := "", "planning", 0
	if checkpoints, ok := repo.(interface {
		LatestCheckpoint(context.Context, string) (domain.Checkpoint, error)
	}); ok {
		cp, err := checkpoints.LatestCheckpoint(ctx, taskID)
		if err != nil && !errors.Is(err, sqlite.ErrNotFound) {
			return "", "", 0, err
		}
		if err == nil {
			raw, err := json.Marshal(cp.State)
			if err != nil {
				return "", "", 0, err
			}
			var state struct {
				CurrentStep string `json:"current_step"`
				PlanAttempt int    `json:"plan_attempt"`
			}
			if err := json.Unmarshal(raw, &state); err != nil {
				return "", "", 0, err
			}
			step, planAttempt = state.CurrentStep, state.PlanAttempt
		}
	}
	if tasks, ok := repo.(interface {
		GetTask(context.Context, string) (domain.Task, error)
	}); ok {
		task, err := tasks.GetTask(ctx, taskID)
		if err != nil {
			return "", "", 0, err
		}
		if task.Stage != nil {
			switch value := string(*task.Stage); value {
			case "planning", "search", "fetch", "extract", "analysis", "verification", "finalization", "synthesis", "execution":
				phase = value
			}
		}
	}
	return step, phase, planAttempt, nil
}

func prepareInvocation(ctx context.Context, repo invocationRepository, taskID, kind, role, step, inputHash string) (domain.Invocation, bool, error) {
	current, phase, planAttempt, err := invocationScope(ctx, repo, taskID)
	if err != nil {
		return domain.Invocation{}, false, err
	}
	if step == "" {
		step = current
	}
	if step != "" {
		phase = "step"
	}
	scopeHash, err := invocationHash([]any{kind, inputHash, step, phase, planAttempt})
	if err != nil {
		return domain.Invocation{}, false, err
	}
	operation := kind + "-" + scopeHash
	history, err := repo.InvocationsForOperation(ctx, taskID, operation)
	if err != nil {
		return domain.Invocation{}, false, err
	}
	attempt := 1
	for _, prior := range history {
		if prior.Kind != kind || prior.InputHash != inputHash || prior.StepID != step || prior.Role != role {
			return domain.Invocation{}, false, ErrRecoveryRequired
		}
		if kind == "llm.stream" {
			return domain.Invocation{}, false, ErrRecoveryRequired
		}
		if prior.Status == "completed" {
			return prior, true, nil
		}
		if prior.Attempt >= attempt {
			attempt = prior.Attempt + 1
		}
		if strings.HasPrefix(kind, "llm.") && prior.Status == "failed" && !retryableInvocationFailure(prior.ErrorCode) {
			return domain.Invocation{}, false, llm.ErrInvalidModelOutput
		}
		if kind == "tool.fetch_url" || strings.HasPrefix(kind, "tool.") && kind != "tool.search" {
			return domain.Invocation{}, false, ErrRecoveryRequired
		}
	}
	if attempt > 2 {
		return domain.Invocation{}, false, ErrRecoveryRequired
	}
	in, err := repo.BeginInvocation(ctx, domain.Invocation{TaskID: taskID, StepID: step, Role: role, Kind: kind, OperationID: operation, InputHash: inputHash, IdempotencyKey: fmt.Sprintf("%s-attempt-%d", operation, attempt), Attempt: attempt})
	if err != nil {
		return domain.Invocation{}, false, err
	}
	if in.Status != "started" {
		return domain.Invocation{}, false, ErrRecoveryRequired
	}
	return in, false, nil
}

func retryableInvocationFailure(code string) bool {
	switch code {
	case "model_resource", "model_oom", "model_timeout", "llama_unhealthy", "resource_pause", "cancel_unknown":
		return true
	}
	return false
}

func finishInvocation(ctx context.Context, repo invocationRepository, in domain.Invocation, result any, callErr error) error {
	if callErr != nil {
		if ctx.Err() != nil && !errors.Is(context.Cause(ctx), errResourcePause) && modelResourceReason(callErr) == "" {
			return callErr
		}
		in.Status, in.ErrorCode = "failed", "invocation_failed"
		if strings.HasPrefix(in.Kind, "llm.") {
			in.ErrorCode = "model_error"
		}
		if strings.HasPrefix(in.Kind, "tool.") {
			if code := fetchFailureCode(callErr); code != "" {
				in.ErrorCode = code
			}
		}
		if reason := modelResourceReason(callErr); reason != "" {
			in.ErrorCode = reason
		}
		if errors.Is(callErr, errResourcePause) || errors.Is(context.Cause(ctx), errResourcePause) {
			in.ErrorCode = "resource_pause"
		}
		if errors.Is(callErr, llm.ErrInvalidModelOutput) {
			in.ErrorCode = "invalid_model_output"
		}
		if errors.Is(callErr, context.Canceled) && in.ErrorCode != "resource_pause" {
			in.Status, in.ErrorCode = "unknown", "cancel_unknown"
		}
		result = nil
	} else {
		in.Status = "completed"
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := repo.FinishInvocation(writeCtx, in, result); err != nil {
		if ctx.Err() != nil {
			return callErr
		}
		return err
	}
	return callErr
}

func (c *invocationClient) Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	inputHash, err := modelInvocationHash(request)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	in, cached, err := prepareInvocation(ctx, c.repo, c.runID, "llm.complete", "model", "", inputHash)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	if cached {
		var response llm.ChatResponse
		if len(in.Output) > 100<<10 || json.Unmarshal(in.Output, &response) != nil || !json.Valid([]byte(response.Content)) {
			return response, ErrRecoveryRequired
		}
		return response, nil
	}
	response, err := c.inner.Complete(ctx, request)
	if err != nil {
		return response, finishInvocation(ctx, c.repo, in, nil, err)
	}
	var output any
	if len(request.JSONSchema) > 0 || request.JSONMode {
		encoded, encodeErr := json.Marshal(response)
		if encodeErr != nil || len(encoded) > 100<<10 || !json.Valid([]byte(response.Content)) || len(request.JSONSchema) > 0 && tools.ValidateJSONSchema(request.JSONSchema, []byte(response.Content)) != nil {
			persistErr := finishInvocation(ctx, c.repo, in, nil, llm.ErrInvalidModelOutput)
			if persistErr != nil && !errors.Is(persistErr, llm.ErrInvalidModelOutput) {
				return response, persistErr
			}
			return response, nil
		}
		output = response
	}
	return response, finishInvocation(ctx, c.repo, in, output, nil)
}

func (c *invocationClient) Stream(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	inputHash, err := modelInvocationHash(request)
	if err != nil {
		return nil, err
	}
	in, _, err := prepareInvocation(ctx, c.repo, c.runID, "llm.stream", "model", "", inputHash)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.inner.Stream(streamCtx, request)
	if err != nil {
		cancel()
		return nil, finishInvocation(ctx, c.repo, in, nil, err)
	}
	if stream == nil {
		cancel()
		return nil, finishInvocation(ctx, c.repo, in, nil, llm.ErrStreamInterrupted)
	}
	output := make(chan llm.ChatEvent)
	go func() {
		defer close(output)
		defer cancel()
		var digest hash.Hash = sha256.New()
		var streamErr error
		for event := range stream {
			_, _ = digest.Write([]byte(event.Text))
			if event.Err != nil {
				streamErr = event.Err
			}
			select {
			case output <- event:
			case <-ctx.Done():
				cancel()
			}
		}
		if ctx.Err() != nil && streamErr == nil {
			streamErr = ctx.Err()
		}
		result := map[string]string{"output_sha256": hex.EncodeToString(digest.Sum(nil))}
		persistErr := finishInvocation(ctx, c.repo, in, result, streamErr)
		if persistErr != nil && streamErr == nil {
			select {
			case output <- llm.ChatEvent{Err: persistErr}:
			case <-ctx.Done():
			}
		}
	}()
	return output, nil
}

type invocationReservationOwner struct{}

func (w *Worker) beginToolInvocation(ctx context.Context, taskID, role, name string, arguments json.RawMessage, stepIDs []string) (context.Context, invocationRepository, domain.Invocation, any, bool, error) {
	repo, ok := w.repo.(invocationRepository)
	if !ok {
		return ctx, nil, domain.Invocation{}, nil, false, nil
	}
	var normalized any
	if err := json.Unmarshal(arguments, &normalized); err != nil {
		return ctx, repo, domain.Invocation{}, nil, false, err
	}
	inputHash, err := invocationHash(struct {
		Name           string
		Arguments      any
		FetchByteLimit int64
		StepKey        []string
	}{name, normalized, tools.FetchByteLimit(ctx, 0), stepIDs})
	if err != nil {
		return ctx, repo, domain.Invocation{}, nil, false, err
	}
	step := ""
	if len(stepIDs) > 0 {
		step = stepIDs[0]
	}
	if len(step) > 64 || strings.ContainsAny(step, ":/ ") {
		digest := sha256.Sum256([]byte(step))
		step = "aux_" + hex.EncodeToString(digest[:16])
	}
	in, cached, err := prepareInvocation(ctx, repo, taskID, "tool."+name, role, step, inputHash)
	if err != nil {
		return ctx, repo, in, nil, false, err
	}
	ctx = context.WithValue(ctx, invocationReservationOwner{}, true)
	if !cached {
		return ctx, repo, in, nil, false, nil
	}
	var result any
	if json.Unmarshal(in.Output, &result) != nil {
		return ctx, repo, in, nil, false, ErrRecoveryRequired
	}
	if values, ok := result.(map[string]any); ok {
		if number, ok := values["fetched_bytes"].(float64); ok && number >= 0 && number < 1<<63 && number == float64(int64(number)) {
			values["fetched_bytes"] = int64(number)
		}
	}
	return ctx, repo, in, result, true, nil
}

func CompletedInvocationTool(event domain.Event) (domain.Event, bool) {
	if event.Type != "invocation.completed" {
		return event, false
	}
	var payload struct {
		Invocation domain.Invocation `json:"invocation"`
		StepID     string            `json:"step_id"`
		Tool       string            `json:"tool"`
		Result     json.RawMessage   `json:"result"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || payload.Invocation.Status != "completed" || !strings.HasPrefix(payload.Invocation.Kind, "tool.") || payload.Invocation.Kind != "tool."+payload.Tool || payload.Invocation.TaskID != event.TaskID || payload.StepID != payload.Invocation.StepID || len(payload.Result) == 0 || !json.Valid(payload.Result) {
		return event, false
	}
	digest := sha256.Sum256(payload.Result)
	if payload.Invocation.OutputHash != hex.EncodeToString(digest[:]) {
		return event, false
	}
	event.Type = "tool.completed"
	event.Payload, _ = json.Marshal(map[string]any{"step_id": payload.StepID, "tool": payload.Tool, "result": payload.Result, "recovered": true})
	return event, true
}

func invocationRecoveryEvents(events []domain.Event, attemptStart int64) []domain.Event {
	committed := map[string]bool{}
	key := func(event domain.Event) string {
		var p struct {
			Tool   string `json:"tool"`
			StepID string `json:"step_id"`
		}
		if json.Unmarshal(event.Payload, &p) != nil {
			return ""
		}
		return p.StepID + "\x00" + p.Tool
	}
	for _, event := range events {
		if event.Type == "tool.completed" && event.Seq > attemptStart {
			committed[key(event)] = true
		}
	}
	result := append([]domain.Event(nil), events...)
	for index, event := range result {
		if event.Seq <= attemptStart {
			continue
		}
		if completed, ok := CompletedInvocationTool(event); ok && !committed[key(completed)] {
			result[index] = completed
		}
	}
	return result
}
