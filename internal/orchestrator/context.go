package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type contextRepository interface {
	ContextSnapshot(context.Context, string) (domain.ContextSnapshot, error)
	SaveContextSnapshot(context.Context, string, domain.ContextSnapshot, string) (domain.ContextSnapshot, error)
	RoleContext(context.Context, string, string) (json.RawMessage, error)
	PinRoleContext(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
}

type rolePinContextKey struct{}
type roleRepairContextKey struct{}
type pinnedRole struct{ Slot, Role string }

func roleCallSlot(ctx context.Context, slot string) context.Context {
	return context.WithValue(ctx, roleRepairContextKey{}, slot)
}

func (w *Worker) roleContext(ctx context.Context, taskID, role, goal string) (context.Context, *domain.ContextSnapshot, json.RawMessage, error) {
	return w.roleContextWithConstraints(ctx, taskID, role, goal, nil)
}

func (w *Worker) roleContextWithConstraints(ctx context.Context, taskID, role, goal string, extra []string) (context.Context, *domain.ContextSnapshot, json.RawMessage, error) {
	repo, ok := w.repo.(contextRepository)
	if !ok {
		return ctx, nil, nil, nil
	}
	task, err := w.repo.GetTask(ctx, taskID)
	if err != nil {
		return ctx, nil, nil, err
	}
	messages, err := w.repo.Messages(ctx, taskID)
	if err != nil {
		return ctx, nil, nil, err
	}
	if goal == "" {
		goal = composeGoal(task.Description, messages)
	}
	if len(goal) > 32<<10 {
		return ctx, nil, nil, errInputBudgetExceeded
	}
	state, err := w.currentCheckpoint(ctx, taskID)
	if err != nil {
		return ctx, nil, nil, err
	}
	constraints := append([]string{}, extra...)
	for _, message := range messages {
		if message.Role == "user" {
			constraints = append(constraints, message.Content)
		}
	}
	failures := make([]string, 0, len(state.SourceFailures))
	for _, failure := range state.SourceFailures {
		failures = append(failures, failure.AttemptID+":"+failure.Code)
	}
	slot, err := invocationHash(struct {
		Role, Step, Goal                 string
		Attempt                          int
		Completed, Failures, Constraints []string
	}{role, state.CurrentStep, goal, state.PlanAttempt, append([]string{}, state.CompletedSteps...), failures, constraints})
	if err != nil {
		return ctx, nil, nil, err
	}
	ctx = context.WithValue(ctx, rolePinContextKey{}, pinnedRole{Slot: slot, Role: role})
	if input, err := repo.RoleContext(ctx, taskID, slot); err == nil {
		var pinned struct {
			Snapshot *domain.ContextSnapshot `json:"context_snapshot"`
		}
		if json.Unmarshal(input, &pinned) != nil || pinned.Snapshot == nil {
			return ctx, nil, nil, sqlite.ErrCheckpointInvalid
		}
		return ctx, pinned.Snapshot, input, nil
	} else if !errors.Is(err, sqlite.ErrNotFound) {
		return ctx, nil, nil, err
	}
	snapshot, err := repo.ContextSnapshot(ctx, taskID)
	if err != nil && !errors.Is(err, sqlite.ErrNotFound) {
		return ctx, nil, nil, err
	}
	if errors.Is(err, sqlite.ErrNotFound) {
		events, eventErr := w.planSafetyEvents(ctx, taskID)
		if eventErr != nil {
			return ctx, nil, nil, eventErr
		}
		for _, event := range events {
			if strings.HasPrefix(event.Type, "invocation.") {
				return ctx, nil, nil, ErrRecoveryRequired
			}
		}
	}
	snapshot.Version = 1
	if snapshot.PlanAttempt != state.PlanAttempt {
		snapshot.AcceptedEvidenceIDs = []string{}
		snapshot.OpenQuestions = []string{}
		snapshot.Contradictions = []string{}
		snapshot.FailedAttempts = []string{}
		snapshot.WorkingSet = map[string]string{}
	}
	snapshot.PlanAttempt = state.PlanAttempt
	snapshot.Goal = task.Description
	snapshot.Constraints = uniqueContextValues(append(snapshot.Constraints, constraints...))
	snapshot.CompletedSteps = append([]string{}, state.CompletedSteps...)
	snapshot.FailedAttempts = uniqueContextValues(append(snapshot.FailedAttempts, failures...))
	snapshot.NextAction = role
	snapshot.Summary = state.Summary
	if len(snapshot.Summary) > 2000 {
		snapshot.Summary = truncateBytesUTF8(snapshot.Summary, 2000)
	}
	evidence, err := w.repo.Evidence(ctx, taskID)
	if err != nil {
		return ctx, nil, nil, err
	}
	activeEvidence := map[string]bool{}
	for _, id := range collectEvidenceIDs(state.ToolResults) {
		activeEvidence[id] = true
	}
	snapshot.AcceptedEvidenceIDs = []string{}
	for _, source := range evidence {
		if activeEvidence[source.ID] && source.HTTPStatus >= 200 && source.HTTPStatus < 300 && len(source.QualityFlags) == 0 && strings.TrimSpace(source.Excerpt) != "" {
			snapshot.AcceptedEvidenceIDs = append(snapshot.AcceptedEvidenceIDs, source.ID)
		}
	}
	snapshot.AcceptedEvidenceIDs = uniqueContextValues(snapshot.AcceptedEvidenceIDs)
	snapshot, err = repo.SaveContextSnapshot(ctx, taskID, snapshot, "role_boundary")
	if err != nil {
		return ctx, nil, nil, err
	}
	return ctx, &snapshot, nil, nil
}

func uniqueContextValues(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (w *Worker) pinRoleInput(ctx context.Context, taskID, input string) (string, error) {
	pin, ok := ctx.Value(rolePinContextKey{}).(pinnedRole)
	if !ok {
		return input, nil
	}
	repo, ok := w.repo.(contextRepository)
	if !ok {
		return "", sqlite.ErrCheckpointInvalid
	}
	data, err := repo.PinRoleContext(ctx, taskID, pin.Slot, json.RawMessage(input))
	return string(data), err
}

func chatUserConstraints(run domain.Run, messages []domain.ConversationMessage) ([]string, error) {
	byID := map[string]domain.ConversationMessage{}
	for _, message := range messages {
		if message.ConversationID == run.ConversationID {
			byID[message.ID] = message
		}
	}
	id := run.UserMessageID
	if run.Operation == "continue" {
		id = run.ContinueFromMessageID
	}
	seen := map[string]bool{}
	constraints := []string{}
	for id != "" {
		if seen[id] {
			return nil, errInvalidModelOutput
		}
		seen[id] = true
		message, ok := byID[id]
		if !ok {
			return nil, errInvalidModelOutput
		}
		if message.Role == "user" {
			constraints = append(constraints, message.Content)
		}
		id = message.ParentMessageID
	}
	for i, j := 0, len(constraints)-1; i < j; i, j = i+1, j-1 {
		constraints[i], constraints[j] = constraints[j], constraints[i]
	}
	return constraints, nil
}
