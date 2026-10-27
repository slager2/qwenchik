package orchestrator

import (
	"context"
	"encoding/json"
	"github.com/slager2/qwenchik/internal/domain"
	"sort"
)

func roundPlanSession(ctx context.Context) bool {
	s, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession)
	return ok && s.Policy.Version == 2
}
func deliverablePlanSchema(raw json.RawMessage) json.RawMessage {
	var s map[string]any
	_ = json.Unmarshal(raw, &s)
	s["properties"].(map[string]any)["deliverable_criteria"] = boundedArraySchema(boundedStringSchema(1, 500), 1, 8)
	required := s["required"].([]any)
	required = append(required, "deliverable_criteria")
	s["required"] = required
	return encodeSchema(s)
}

// History remains durable; model working set contains the active round and a
// bounded set of earlier fetched evidence, not every old discovery result/plan.
func researchWorkingResults(state checkpointState) map[string]any {
	if state.ResearchPlans == nil {
		return state.ToolResults
	}
	p, err := state.ResearchPlans.Current()
	if err != nil {
		return map[string]any{}
	}
	result := map[string]any{}
	for _, step := range p.Subtasks {
		if value, ok := state.ToolResults[step.ID]; ok {
			result[step.ID] = value
		}
	}
	keys := []string{}
	for key := range state.ToolResults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	evidence := len(collectEvidenceIDs(result))
	for _, key := range keys {
		if _, ok := result[key]; ok {
			continue
		}
		if evidence >= 8 {
			break
		}
		value := state.ToolResults[key]
		if len(collectEvidenceIDs(value)) > 0 {
			result[key] = value
			evidence++
		}
	}
	return result
}

func researchReportEvidenceIDs(state checkpointState, limit int) []string {
	all := collectEvidenceIDs(state.ToolResults)
	if state.ResearchPlans == nil {
		return all
	}
	selected := []string{}
	add := func(id string) {
		if len(selected) < limit && contains(all, id) && !contains(selected, id) {
			selected = append(selected, id)
		}
	}
	if state.Adaptive != nil && len(state.Adaptive.Rounds) > 0 {
		for _, criterion := range state.Adaptive.Rounds[len(state.Adaptive.Rounds)-1].Coverage {
			if criterion.StepID == "goal" && criterion.Met {
				for _, id := range criterion.EvidenceIDs {
					add(id)
				}
			}
		}
	}
	for _, id := range collectEvidenceIDs(researchWorkingResults(state)) {
		add(id)
	}
	for _, id := range all {
		add(id)
	}
	return selected
}

func reportSourceNotes(notes []domain.SourceNote, catalogue []citationEvidence) []domain.SourceNote {
	selected := []domain.SourceNote{}
	for _, source := range catalogue {
		for _, note := range notes {
			if note.EvidenceID == source.EvidenceID {
				selected = append(selected, note)
				break
			}
		}
	}
	return compactSourceNotes(selected)
}
