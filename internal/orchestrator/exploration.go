package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

// errExplorationContinued tells execute that a covered round of an exploring
// profile started another bounded round instead of finalizing.
var errExplorationContinued = errors.New("research exploration round requested")

const maxVerifierFollowUps = 6

const followUpInstruction = ` Also return follow_up_queries: up to 6 concise web search queries (3 to 12 keywords each, no URLs, no operators) for the next research round. Address missing items first. Then go deeper and broader: combine a specific entity named in the evidence (for example a university, program, scholarship, company or organization) with a fact the evidence does not state yet (requirements, deadlines, costs and fees, admission statistics, minimum scores, eligibility), or name further candidate entities of the same kind. Write each query in the language most likely to find the official source. Never repeat executed_queries. Follow-up queries are search suggestions, not findings, and never make an answer ready or unready.`

// explorationVerifier reports whether this round's verifier also proposes
// follow-up searches. It depends only on durable inputs (policy, budget and
// round number) so a resumed verifier call keeps the same schema.
func explorationVerifier(ctx context.Context, state checkpointState) bool {
	session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession)
	return ok && domain.Exploring(session, state.Completion.Continuations+1)
}

// explorationRound is a round that follows an already covered round: its job
// is breadth and depth, so missing novelty is stagnation, not a blocking gap.
func explorationRound(state checkpointState) bool {
	if state.Adaptive == nil {
		return false
	}
	for _, round := range state.Adaptive.Rounds {
		if round.Number <= state.Completion.Continuations && (round.Outcome == "PASS" || round.Outcome == "PARTIAL") {
			return true
		}
	}
	return false
}

func withFollowUpQueries(schema json.RawMessage) json.RawMessage {
	var decoded map[string]any
	if json.Unmarshal(schema, &decoded) != nil {
		return schema
	}
	properties, _ := decoded["properties"].(map[string]any)
	if properties == nil {
		return schema
	}
	// Optional: an omitted list falls back to deterministic follow-ups and
	// never turns a readiness assessment into invalid model output.
	properties["follow_up_queries"] = boundedArraySchema(boundedStringSchema(3, 160), 0, maxVerifierFollowUps)
	return encodeSchema(decoded)
}

func executedQueries(state checkpointState) []string {
	queries := []string{}
	for _, query := range append(append([]string{}, state.LifetimeSearchQueryHistory...), state.SearchQueryHistory...) {
		if !contains(queries, query) {
			queries = append(queries, query)
		}
	}
	return queries[max(0, len(queries)-48):]
}

// researchFollowUps keeps verifier follow-ups that are safe, new keyword
// queries and pads them with deterministic goal x deliverable queries, so an
// exploring round always has entity- or criterion-level searches to run.
func researchFollowUps(proposed []string, goal string, plan domain.Plan, state checkpointState) []string {
	previous := executedQueries(state)
	if state.Adaptive != nil {
		for _, question := range state.Adaptive.Questions {
			previous = append(previous, question.Text)
		}
	}
	selected := []string{}
	add := func(text string) {
		query := conciseSearchQuery(text)
		if query == "" || len(selected) >= maxVerifierFollowUps || duplicateResearchQuery(query, previous) || duplicateResearchQuery(query, selected) {
			return
		}
		selected = append(selected, query)
	}
	for _, text := range proposed {
		add(text)
	}
	if len(selected) < 2 {
		topic := goalKeywordQuery(goal, 5)
		for _, criterion := range plan.DeliverableCriteria {
			add(topic + " " + goalKeywordQuery(criterion, 6))
		}
	}
	return selected
}

func followUpQuestionTexts(state checkpointState) []string {
	texts := []string{}
	if state.Adaptive == nil {
		return texts
	}
	for _, id := range state.Adaptive.Frontier {
		for _, question := range state.Adaptive.Questions {
			if question.ID == id && question.Status == "open" && strings.HasPrefix(id, domain.FollowUpQuestionPrefix) && len(texts) < 20 {
				texts = append(texts, truncateRunes(question.Text, 500))
			}
		}
	}
	return texts
}

// requestExplorationContinuation starts the next bounded round after a
// covered verification when the store kept follow-up questions open. Budgets,
// deadline, rounds and stagnation were checked when the round was assessed and
// are checked again transactionally by the round admission.
func (w *Worker) requestExplorationContinuation(ctx context.Context, taskID string) (bool, error) {
	session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession)
	if !ok || !session.Policy.AdaptiveEnabled || session.Policy.Version != 2 {
		return false, nil
	}
	repo, ok := w.repo.(interface {
		RequestResearchContinuation(context.Context, string, int, string, string, []string) error
		StopAdaptiveResearch(context.Context, string, string) error
	})
	if !ok {
		return false, nil
	}
	state, err := w.currentCheckpoint(ctx, taskID)
	if err != nil {
		return false, err
	}
	if state.Adaptive == nil || state.Adaptive.StopReason != "" || len(state.Adaptive.Rounds) == 0 {
		return false, nil
	}
	if last := state.Adaptive.Rounds[len(state.Adaptive.Rounds)-1]; last.Outcome != "PASS" && last.Outcome != "PARTIAL" {
		return false, nil
	}
	questions := followUpQuestionTexts(state)
	if len(questions) == 0 {
		return false, nil
	}
	task, err := w.repo.GetTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	if task.Plan == nil || state.ResearchPlans == nil || state.PendingTool != "" || state.PendingApproval {
		return false, ErrRecoveryRequired
	}
	stop := func(reason string) (bool, error) {
		if err := repo.StopAdaptiveResearch(ctx, taskID, reason); err != nil {
			return false, err
		}
		return false, nil
	}
	if !requiresWebResearch(task.Title+"\n"+task.Description) || !w.tools.Has("search") || !w.tools.Has("fetch_url") {
		return stop("empty_frontier")
	}
	fingerprint, err := completionFingerprint(*task.Plan, state)
	if err != nil {
		return false, err
	}
	sorted := append([]string{}, questions...)
	sort.Strings(sorted)
	failure, err := invocationHash(sorted)
	if err != nil {
		return false, err
	}
	err = repo.RequestResearchContinuation(ctx, taskID, state.Completion.Continuations, fingerprint, failure, questions)
	if errors.Is(err, sqlite.ErrResearchBudgetExceeded) {
		return stop("research_budget")
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
