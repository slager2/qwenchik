package orchestrator

import (
	"fmt"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
)

// Questions that name several programs, universities or products read best
// one section per entity plus a couple of cross-cutting sections, not one
// section per attribute (deadlines, requirements) that repeats every
// entity. The outline model is asked for that grouping and for the entity
// names; the backend keeps only names that the question or a source
// mentions, and falls back to entity sections built from the question's
// names when the outline call fails.

const (
	reportOutlineFormat = "entities_v1"
	reportEntityMax     = 8
)

// reportQuestionEntities are the named entities of the question (e.g.
// "Stipendium Hungaricum", "GKS", "MEXT"), from the shared goal extractor
// that also drives entity search facets and the official-source rule.
func reportQuestionEntities(goal string) []string {
	return goalEntities(goal)
}

// reportEntityEvidence lists the catalogue items that name the entity.
func reportEntityEvidence(entity string, catalogue []citationEvidence) []string {
	ids := []string{}
	for _, item := range catalogue {
		if reportEntityNamed(entity, item.Title+" "+item.Excerpt) {
			ids = append(ids, item.EvidenceID)
		}
	}
	return ids
}

// filterReportEntities keeps distinct names that the question or a source
// mentions.
func filterReportEntities(names []string, goal string, catalogue []citationEvidence) []string {
	kept := []string{}
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.Join(strings.Fields(name), " ")
		key := looseQuoteText(name)
		if key == "" || seen[key] || len(reportEntityTokens(name)) == 0 || reportLineProblem(name) != "" || len(kept) == reportEntityMax {
			continue
		}
		if !reportEntityNamed(name, goal) && len(reportEntityEvidence(name, catalogue)) == 0 {
			continue
		}
		seen[key] = true
		kept = append(kept, name)
	}
	return kept
}

// reportEntityOutline builds per-entity sections for entities that sources
// mention, plus one cross-cutting section over all sources when room is
// left. It returns nil when fewer than two entities have sources.
func reportEntityOutline(entities []string, catalogue []citationEvidence, criteria []string, limit int, labels reportLabels) []domain.ReportSection {
	sections := []domain.ReportSection{}
	for _, entity := range entities {
		ids := reportEntityEvidence(entity, catalogue)
		if len(ids) == 0 || len(sections) == limit {
			continue
		}
		sections = append(sections, domain.ReportSection{ID: fmt.Sprintf("section_%d", len(sections)+1), Title: entity, EvidenceIDs: ids, Criteria: []string{}, Status: "pending"})
	}
	if len(sections) < 2 {
		return nil
	}
	if len(sections) < limit {
		ids := []string{}
		for _, item := range catalogue {
			ids = append(ids, item.EvidenceID)
		}
		sections = append(sections, domain.ReportSection{ID: fmt.Sprintf("section_%d", len(sections)+1), Title: labels.CrossSection, EvidenceIDs: ids, Criteria: append([]string{}, criteria...), Status: "pending"})
	} else {
		sections[0].Criteria = append([]string{}, criteria...)
	}
	return sections
}

func reportOutlinePinnedLegacy(pinned string) bool {
	return !strings.Contains(pinned, `"outline_format":"`+reportOutlineFormat+`"`)
}
