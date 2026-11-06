package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type adaptiveMutationKey struct{}

// Follow-up questions are search suggestions, not part of the assessment: a
// replayed verification may derive them against the already advanced
// frontier, so they never make an identical assessment conflict.
func adaptiveAssessmentHash(verdict, summary string, missing, supplementaryMissing []string, criteria []domain.VerificationCriterionResult) string {
	raw, _ := json.Marshal([]any{verdict, summary, missing, supplementaryMissing, criteria})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func adaptiveState(state map[string]any) (domain.AdaptiveResearchState, error) {
	var adaptive domain.AdaptiveResearchState
	if raw, exists := state["adaptive_research"]; exists {
		encoded, err := json.Marshal(raw)
		if err != nil || len(encoded) > 512<<10 {
			return adaptive, ErrCheckpointInvalid
		}
		if err := json.Unmarshal(encoded, &adaptive); err != nil || adaptive.Version != 1 || len(adaptive.Rounds) > 10 || len(adaptive.Questions) > 80 {
			return adaptive, ErrCheckpointInvalid
		}
	} else {
		adaptive.Version = 1
	}
	return adaptive, nil
}

func (s *Store) StopAdaptiveResearch(ctx context.Context, taskID, reason string) error {
	switch reason {
	case "max_rounds", "step_budget", "research_budget", "stagnation", "question_budget_exhausted", "empty_frontier", "total_steps", "search_budget", "fetch_budget", "evidence_budget", "llm_budget", "fetch_bytes", "deadline":
	default:
		return ErrCheckpointInvalid
	}
	tx, checkpointCtx, err := s.beginCheckpointTx(ctx)
	if err != nil {
		return err
	}
	ctx = checkpointCtx
	defer tx.Rollback()
	if err := invocationAllowed(ctx, tx, taskID, true); err != nil {
		return err
	}
	session, err := researchSession(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if !session.Policy.AdaptiveEnabled {
		return ErrConflict
	}
	state, version, err := s.invocationCheckpointState(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if _, pending := state["pending_invocation_id"]; pending {
		return ErrConflict
	}
	if approval, _ := state["pending_approval"].(bool); approval {
		return ErrConflict
	}
	if tool, _ := state["pending_tool"].(string); tool != "" {
		idempotent, _ := state["pending_tool_idempotent"].(bool)
		if !idempotent || (tool != "search" && tool != "fetch_url") {
			return ErrConflict
		}
		state["pending_tool"] = ""
		state["pending_arguments"] = nil
		state["pending_summary"] = ""
		state["pending_target"] = ""
		state["pending_risk"] = ""
	}
	adaptive, err := adaptiveState(state)
	if err != nil {
		return err
	}
	adaptive.StopReason = reason
	if _, err := tx.ExecContext(ctx, `UPDATE research_sessions SET stop_reason=? WHERE task_id=?`, reason, taskID); err != nil {
		return err
	}
	state["adaptive_research"] = adaptive
	seq, err := appendEvent(ctx, tx, taskID, "research.round.stopped", stagePointer(domain.StageVerification), "user", map[string]any{"reason": reason, "number": session.CurrentRound + 1})
	if err != nil {
		return err
	}
	if _, err := s.saveCheckpointTx(context.WithValue(ctx, adaptiveMutationKey{}, true), tx, taskID, version, seq, state); err != nil {
		return err
	}
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	return s.commitCheckpointTx(ctx, tx)
}

func assessAdaptiveRound(ctx context.Context, tx *sql.Tx, taskID string, state map[string]any, verdict, summary string, missing, supplementaryMissing []string, criteria []domain.VerificationCriterionResult, followUps []string) error {
	session, err := researchSession(ctx, tx, taskID)
	if err == ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if !session.Policy.AdaptiveEnabled {
		return nil
	}
	adaptive, err := adaptiveState(state)
	if err != nil {
		return err
	}
	number := session.CurrentRound + 1
	if number > min(10, session.Budget.MaxRounds) {
		return ErrResearchBudgetExceeded
	}
	if len(adaptive.Rounds) > 0 && adaptive.Rounds[len(adaptive.Rounds)-1].Number == number {
		return nil
	}
	ids := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			if id, ok := item["evidence_id"].(string); ok {
				ids[id] = true
			}
			for _, child := range item {
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(state["tool_results"])
	evidence := []string{}
	for id := range ids {
		evidence = append(evidence, id)
	}
	sort.Strings(evidence)
	if err := validateSnapshotEvidence(ctx, tx, taskID, evidence); err != nil {
		return err
	}
	prior := map[string]bool{}
	priorCoverage := map[string]bool{}
	if len(adaptive.Rounds) > 0 {
		for _, id := range adaptive.Rounds[len(adaptive.Rounds)-1].AcceptedEvidence {
			prior[id] = true
		}
		for _, criterion := range adaptive.Rounds[len(adaptive.Rounds)-1].Coverage {
			if criterion.Met {
				priorCoverage[fmt.Sprintf("criterion:%s:%d", criterion.StepID, criterion.Criterion)] = true
			}
		}
	}
	progress := false
	for _, id := range evidence {
		if !prior[id] {
			progress = true
		}
	}
	for _, criterion := range criteria {
		if session.Policy.Version == 2 && criterion.StepID != "goal" {
			continue
		}
		if criterion.Met && len(criterion.EvidenceIDs) > 0 && !priorCoverage[fmt.Sprintf("criterion:%s:%d", criterion.StepID, criterion.Criterion)] {
			progress = true
		}
	}
	if progress {
		adaptive.StagnantRounds = 0
	} else {
		adaptive.StagnantRounds++
	}
	open := map[string]string{}
	resolved := map[string]bool{}
	for _, criterion := range criteria {
		id := fmt.Sprintf("criterion:%s:%d", criterion.StepID, criterion.Criterion)
		if session.Policy.Version == 2 && criterion.StepID != "goal" {
			continue
		}
		if criterion.Classification == "critical" && !criterion.Met {
			open[id] = "Unresolved critical " + id
		} else if criterion.Met {
			resolved[id] = true
		}
	}
	if len(missing) > 0 {
		open["goal:critical_coverage"] = truncateResearchQuestion(strings.Join(missing, "; "))
	}
	if verdict == "PASS" || verdict == "PARTIAL" {
		resolved["goal:critical_coverage"] = true
	}
	questionIDs := []string{}
	for id := range open {
		questionIDs = append(questionIDs, id)
	}
	sort.Strings(questionIDs)
	for _, id := range questionIDs {
		text := open[id]
		found := false
		for index := range adaptive.Questions {
			if adaptive.Questions[index].ID == id {
				adaptive.Questions[index].Status = "open"
				adaptive.Questions[index].Text = text
				found = true
				break
			}
		}
		if !found {
			if len(adaptive.Questions) >= min(80, session.Budget.MaxQuestionsCreated) || session.Counters.QuestionsAccepted >= session.Budget.MaxQuestionsAccepted {
				adaptive.StopReason = "question_budget_exhausted"
				continue
			}
			adaptive.Questions = append(adaptive.Questions, domain.ResearchQuestion{ID: id, Text: text, Status: "open", AcceptedOnce: true})
			session.Counters.QuestionsAccepted++
		}
	}
	covered := verdict == "PASS" || verdict == "PARTIAL"
	explored := false // an earlier round already covered the criteria
	for _, prior := range adaptive.Rounds {
		explored = explored || prior.Outcome == "PASS" || prior.Outcome == "PARTIAL"
	}
	explorationStop := ""
	if covered {
		explorationStop = domain.ExplorationStopReason(session, time.Now())
		if explorationStop == "" && explored && adaptive.StagnantRounds >= session.Budget.MaxStagnantRounds {
			explorationStop = "stagnation"
		}
	}
	// Follow-up questions asked by this round's searches are answered by its
	// evidence; new ones come from the verifier for the next round.
	for index := range adaptive.Questions {
		if strings.HasPrefix(adaptive.Questions[index].ID, domain.FollowUpQuestionPrefix) && adaptive.Questions[index].Status == "open" {
			adaptive.Questions[index].Status = "resolved"
		}
	}
	if !covered || explorationStop == "" {
		added := 0
		for _, text := range followUps {
			text = truncateResearchQuestion(strings.Join(strings.Fields(text), " "))
			if text == "" || added >= maxFollowUpsPerRound {
				continue
			}
			digest := sha256.Sum256([]byte(strings.ToLower(text)))
			id := domain.FollowUpQuestionPrefix + hex.EncodeToString(digest[:8])
			known := false
			for _, question := range adaptive.Questions {
				known = known || question.ID == id
			}
			if known || len(adaptive.Questions) >= min(80, session.Budget.MaxQuestionsCreated) || session.Counters.QuestionsAccepted >= session.Budget.MaxQuestionsAccepted {
				continue // an exhausted follow-up budget ends exploration, not the run
			}
			adaptive.Questions = append(adaptive.Questions, domain.ResearchQuestion{ID: id, Text: text, Status: "open", AcceptedOnce: true})
			session.Counters.QuestionsAccepted++
			added++
		}
	}
	adaptive.Frontier = []string{}
	for index := range adaptive.Questions {
		question := &adaptive.Questions[index]
		if resolved[question.ID] {
			question.Status = "resolved"
		} else if question.Status == "open" {
			adaptive.Frontier = append(adaptive.Frontier, question.ID)
		}
	}
	session.Counters.QuestionsCreated = max(session.Counters.QuestionsCreated, len(adaptive.Questions))
	session.Counters.Rounds = max(session.Counters.Rounds, number)
	session.Counters.StagnantRounds = adaptive.StagnantRounds
	if covered {
		for _, id := range adaptive.Frontier {
			if !strings.HasPrefix(id, domain.FollowUpQuestionPrefix) {
				return ErrConflict
			}
		}
		if explorationStop == "" && len(adaptive.Frontier) == 0 {
			explorationStop = "empty_frontier"
		}
		if explorationStop == "stagnation" {
			adaptive.StopReason = "stagnation"
		} else if explorationStop != "" {
			adaptive.StopReason = "criteria_covered"
		}
	}
	if verdict != "PASS" && verdict != "PARTIAL" && adaptive.StagnantRounds >= session.Budget.MaxStagnantRounds {
		adaptive.StopReason = "stagnation"
	}
	if len(missing) > 0 && number >= session.Budget.MaxRounds {
		adaptive.StopReason = "max_rounds"
	}
	adaptive.CurrentRound = number
	round := domain.ResearchRound{Number: number, Goal: session.Goal, AcceptedEvidence: evidence, CriticalMissing: append([]string{}, missing...), OpenQuestions: append([]string{}, missing...), Frontier: append([]string{}, adaptive.Frontier...), Outcome: verdict}
	round.AssessmentHash = adaptiveAssessmentHash(verdict, summary, missing, supplementaryMissing, criteria)
	round.Coverage = append([]domain.VerificationCriterionResult{}, criteria...)
	encoded, _ := json.Marshal(state["completed_steps"])
	_ = json.Unmarshal(encoded, &round.CompletedSteps)
	encoded, _ = json.Marshal(state["source_failures"])
	_ = json.Unmarshal(encoded, &round.SourceFailures)
	if len(round.SourceFailures) > 64 {
		round.SourceFailures = round.SourceFailures[:64]
	}
	for _, failure := range round.SourceFailures {
		if identity, ok := failure["source_fingerprint"].(string); ok {
			round.FailedSources = append(round.FailedSources, identity)
		}
	}
	coveredCriteria := []string{}
	for _, criterion := range criteria {
		if criterion.Met && len(criterion.EvidenceIDs) > 0 {
			coveredCriteria = append(coveredCriteria, fmt.Sprintf("%s:%d", criterion.StepID, criterion.Criterion))
		}
	}
	sort.Strings(coveredCriteria)
	fingerprint, _ := json.Marshal([]any{evidence, coveredCriteria})
	digest := sha256.Sum256(fingerprint)
	round.ProgressFingerprint = hex.EncodeToString(digest[:])
	round.RemainingBudgets, err = snapshotBudget(ctx, tx, session)
	if err != nil {
		return err
	}
	adaptive.Rounds = append(adaptive.Rounds, round)
	state["adaptive_research"] = adaptive
	if err := writeResearchCounters(ctx, tx, taskID, session.Counters); err != nil {
		return err
	}
	_, err = appendEvent(ctx, tx, taskID, "research.round.assessed", stagePointer(domain.StageVerification), "user", map[string]any{"number": number, "outcome": verdict, "stop_reason": adaptive.StopReason, "frontier_count": len(adaptive.Frontier), "evidence_count": len(evidence), "critical_missing_count": len(missing), "stagnant_rounds": adaptive.StagnantRounds, "questions_created": len(adaptive.Questions), "exploring": covered && adaptive.StopReason == "", "exploration_stop": explorationStop})
	return err
}

// maxFollowUpsPerRound bounds verifier follow-up questions accepted per round:
// about one per search step of the widest round shape.
const maxFollowUpsPerRound = 6

func truncateResearchQuestion(text string) string {
	runes := []rune(text)
	return string(runes[:min(500, len(runes))])
}
