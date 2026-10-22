package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

var errResearchRuntimeChanged = errors.New("immutable research settings are incompatible with the current runtime")

type researchExecutionRepository interface {
	GetResearchSession(context.Context, string) (domain.ResearchSession, error)
	ReserveResearchLLMCall(context.Context, string) (domain.ResearchCounters, error)
	ReserveResearchHTTPAttempt(context.Context, string) (domain.ResearchCounters, error)
	ReserveResearchOperation(context.Context, string, string, string) (domain.ResearchCounters, error)
	SetResearchPhase(context.Context, string, string, string) error
}

type researchExecutionKey struct{}

type sessionModelClient struct {
	inner llm.Client
	repo  researchExecutionRepository
	runID string
	model domain.ModelProfileSnapshot
}

func (c *sessionModelClient) prepare(ctx context.Context, request llm.ChatRequest) (llm.ChatRequest, error) {
	if err := checkRunResources(ctx); err != nil {
		return request, err
	}
	if err := ctx.Err(); err != nil {
		return request, err
	}
	if c.repo != nil && !ownsInvocationReservations(c.inner) {
		if _, err := c.repo.ReserveResearchLLMCall(ctx, c.runID); err != nil {
			return request, err
		}
	}
	request.Model, request.BaseURL = c.model.Model, c.model.BaseURL
	request.Stop = append([]string(nil), c.model.StopSequences...)
	if request.MaxTokens <= 0 || request.MaxTokens > c.model.MaxOutputTokens {
		request.MaxTokens = c.model.MaxOutputTokens
	}
	if !request.TemperatureSet {
		request.Temperature = c.model.Temperature
	}
	if !request.TopPSet {
		request.TopP = c.model.TopP
	}
	request.TemperatureSet, request.TopPSet = true, true
	previous := request.BeforeFallbackAttempt
	request.BeforeFallbackAttempt = func() error {
		if c.repo != nil {
			if _, err := c.repo.ReserveResearchHTTPAttempt(ctx, c.runID); err != nil {
				return err
			}
		}
		if previous != nil {
			return previous()
		}
		return nil
	}
	return request, nil
}

func (c *sessionModelClient) Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	request, err := c.prepare(ctx, request)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	return c.inner.Complete(ctx, request)
}

func (c *sessionModelClient) Stream(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	request, err := c.prepare(ctx, request)
	if err != nil {
		return nil, err
	}
	return c.inner.Stream(ctx, request)
}

func (w *Worker) researchExecutor(ctx context.Context, taskID string) (context.Context, *Worker, error) {
	repo, ok := w.repo.(researchExecutionRepository)
	if !ok {
		return ctx, w, nil
	}
	session, err := repo.GetResearchSession(ctx, taskID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return w.chatModelExecutor(ctx, taskID)
	}
	if err != nil {
		return ctx, nil, err
	}
	if err := repo.SetResearchPhase(ctx, taskID, "planning", ""); err != nil {
		return ctx, nil, err
	}
	if w.options.RuntimeResearchPolicy != nil {
		stored, current := session.Policy, *w.options.RuntimeResearchPolicy
		stored.Profile, current.Profile = "", ""
		stored.Budget, current.Budget = domain.ResearchBudget{}, domain.ResearchBudget{}
		stored.KeepAwake, current.KeepAwake = false, false
		stored.MaxContinuations, current.MaxContinuations = 0, 0
		if stored != current {
			return ctx, nil, errResearchRuntimeChanged
		}
	}
	model := session.ModelSnapshot
	if w.options.RuntimeModelContext > 0 && model.ContextLength > w.options.RuntimeModelContext {
		return ctx, nil, errResearchRuntimeChanged
	}
	options := w.options
	options.MaxSteps, options.MaxSearchQueries = session.Budget.MaxSteps, session.Budget.MaxSearchQueries
	options.MaxFetchBytes = session.Policy.TaskFetchBudgetBytes
	runner, err := w.modelExecutor(ctx, model, &sessionModelClient{inner: newInvocationClient(w.repo, w.client, taskID), repo: repo, runID: taskID, model: model}, options)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, researchExecutionKey{}, session), runner, nil
}

func (w *Worker) modelExecutor(ctx context.Context, model domain.ModelProfileSnapshot, client llm.Client, options Options) (*Worker, error) {
	if model.Bundle != nil {
		if model.Bundle.Validate() != nil || model.Bundle.ModelProfile != model.Model || options.RuntimeBundleHash != "" && model.Bundle.BaseHash() != options.RuntimeBundleHash {
			return nil, errResearchRuntimeChanged
		}
		if model.Bundle.Version > 1 {
			reader, ok := w.repo.(interface {
				BundleCandidate(context.Context, string) (domain.BundleCandidate, error)
			})
			if !ok {
				return nil, errResearchRuntimeChanged
			}
			candidate, err := reader.BundleCandidate(ctx, model.Bundle.BundleHash)
			if err != nil {
				return nil, err
			}
			if candidate.Status != "approved" {
				return nil, errResearchRuntimeChanged
			}
		}
	}
	if w.options.RuntimeModelContext > 0 && model.ContextLength > w.options.RuntimeModelContext {
		return nil, errResearchRuntimeChanged
	}
	options.MaxOutputTokens = model.MaxOutputTokens
	options.Temperature, options.TopP = model.Temperature, model.TopP
	availableTokens := model.ContextLength - model.MaxOutputTokens - 2048
	if availableTokens < 256 {
		return nil, errInputBudgetExceeded
	}
	if availableTokens > int(^uint(0)>>1)/4 {
		options.MaxInputBytes = int(^uint(0) >> 1)
	} else {
		options.MaxInputBytes = availableTokens * 4
	}
	return New(w.repo, client, w.tools, options), nil
}

func (w *Worker) chatModelExecutor(ctx context.Context, taskID string) (context.Context, *Worker, error) {
	repo, ok := w.repo.(interface {
		GetRunModelSnapshot(context.Context, string) (domain.ModelProfileSnapshot, error)
	})
	if !ok {
		return ctx, w, nil
	}
	model, err := repo.GetRunModelSnapshot(ctx, taskID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return ctx, w, nil
	}
	if err != nil {
		return ctx, nil, err
	}
	runner, err := w.modelExecutor(ctx, model, &sessionModelClient{inner: newInvocationClient(w.repo, w.client, taskID), runID: taskID, model: model}, w.options)
	return ctx, runner, err
}

func (w *Worker) reserveResearchTool(ctx context.Context, taskID, name string) error {
	if owned, _ := ctx.Value(invocationReservationOwner{}).(bool); owned {
		return nil
	}
	if name != "search" && name != "fetch_url" {
		return nil
	}
	if _, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); !ok {
		return nil
	}
	repo, ok := w.repo.(researchExecutionRepository)
	if !ok {
		return errors.New("research operation storage is unavailable")
	}
	id, err := newSynthesisID()
	if err != nil {
		return err
	}
	_, err = repo.ReserveResearchOperation(ctx, taskID, name, id)
	if err != nil {
		return fmt.Errorf("reserve research %s: %w", name, err)
	}
	return nil
}

func citationLimits(ctx context.Context) (int, int) {
	if session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); ok {
		return session.Budget.MaxCitationSources, session.Budget.MaxCitationClaims
	}
	return maxCitationSources, maxCitationClaims
}

func (w *Worker) runDeadline(ctx context.Context, taskID string) (time.Time, bool) {
	if repo, ok := w.repo.(interface {
		GetResearchSession(context.Context, string) (domain.ResearchSession, error)
	}); ok {
		if session, err := repo.GetResearchSession(ctx, taskID); err == nil {
			return session.DeadlineAt, true
		}
	}
	return time.Now().Add(w.options.TaskTimeout), false
}
