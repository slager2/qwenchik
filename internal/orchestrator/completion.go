package orchestrator

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

var errCompletionBlocked = errors.New("bounded completion governor blocked further continuation")

type completionState = domain.CompletionState

func isCompletionStep(id string) bool { return strings.HasPrefix(id, "continue_") }

func baseCompletionPlan(plan domain.Plan) domain.Plan {
	steps := make([]domain.PlanStep, 0, len(plan.Subtasks))
	for _, step := range plan.Subtasks {
		if !isCompletionStep(step.ID) {
			steps = append(steps, step)
		}
	}
	if len(steps) != len(plan.Subtasks) {
		plan.Subtasks, plan.MaxSteps = steps, len(steps)
	}
	return plan
}

func completionFingerprint(plan domain.Plan, state checkpointState) (string, error) {
	tools := []string{}
	for _, step := range plan.Subtasks {
		if contains(state.CompletedSteps, step.ID) && !contains(state.SkippedSteps, step.ID) {
			tools = append(tools, step.Kind+":"+step.ID)
		}
	}
	queries := []string{}
	for _, query := range state.SearchQueryHistory {
		queries = append(queries, strings.Join(strings.Fields(strings.ToLower(query)), " "))
	}
	sort.Strings(tools)
	sort.Strings(queries)
	return invocationHash([]any{tools, queries, collectEvidenceIDs(state.ToolResults)})
}

func (w *Worker) requestCompletionContinuation(ctx context.Context, taskID string, cause error) (bool, error) {
	repo, ok := w.repo.(interface {
		GetResearchSession(context.Context, string) (domain.ResearchSession, error)
		RequestResearchContinuation(context.Context, string, int, string, string, []string) error
	})
	if !ok {
		return false, nil
	}
	session, err := repo.GetResearchSession(ctx, taskID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if session.Policy.MaxContinuations == 0 {
		return false, nil
	}
	if err := w.ensureActive(ctx, taskID); err != nil {
		return false, err
	}
	state, err := w.currentCheckpoint(ctx, taskID)
	if err != nil {
		return false, err
	}
	task, err := w.repo.GetTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	if task.Plan == nil || state.PendingTool != "" || state.PendingApproval {
		return false, ErrRecoveryRequired
	}
	fingerprint, err := completionFingerprint(*task.Plan, state)
	if err != nil {
		return false, err
	}
	var incomplete verificationIncompleteError
	if !errors.As(cause, &incomplete) {
		return false, cause
	}
	missing := append([]string{}, incomplete.Missing...)
	if len(missing) > 20 {
		missing = missing[:20]
	}
	for index := range missing {
		missing[index] = truncateRunes(missing[index], 500)
	}
	sort.Strings(missing)
	failure, err := invocationHash(missing)
	if err != nil {
		return false, err
	}
	if state.Completion.Continuations >= min(1, session.Policy.MaxContinuations) || state.Completion.Fingerprint == fingerprint || state.Completion.FailureHash == failure {
		return false, errors.Join(errCompletionBlocked, cause)
	}
	if !requiresWebResearch(task.Title+"\n"+task.Description) || !w.tools.Has("search") || !w.tools.Has("fetch_url") {
		return false, errors.Join(errCompletionBlocked, cause)
	}
	if len(task.Plan.Subtasks)+2 > min(12, session.Budget.MaxSteps) {
		return false, errResearchPlanBudget
	}
	if session.Counters.SearchQueries >= session.Budget.MaxSearchQueries || session.Counters.Fetches >= session.Budget.MaxFetches || session.Counters.LLMCalls+3 > session.Budget.MaxLLMCalls || session.Counters.Evidence >= session.Budget.MaxEvidence || session.Counters.FetchedBytes >= session.Policy.TaskFetchBudgetBytes {
		return false, sqlite.ErrResearchBudgetExceeded
	}
	if err := repo.RequestResearchContinuation(ctx, taskID, state.Completion.Continuations, fingerprint, failure, missing); err != nil {
		return false, err
	}
	return true, nil
}
