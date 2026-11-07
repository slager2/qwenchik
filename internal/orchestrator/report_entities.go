package orchestrator

import (
	"fmt"
	"regexp"
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

var reportEntityCandidate = regexp.MustCompile(`\p{Lu}[\p{L}\p{N}'’-]*(?:[ \t]+(?:\p{Lu}[\p{L}\p{N}'’-]*|of|de|du|der|für|and|&))*`)

// Words and acronyms that name attributes, subjects or document types, not
// entities to compare.
var reportEntityNoise = map[string]bool{"it": true, "ai": true, "ml": true, "cs": true, "gpa": true, "ielts": true, "toefl": true, "sat": true, "act": true, "gre": true, "gmat": true, "pdf": true, "html": true, "usd": true, "eur": true, "kzt": true, "faq": true, "computer science": true, "software engineering": true, "data science": true, "original task": true, "research": true, "bachelor": true, "master": true, "phd": true, "need": true, "full": true}

// reportQuestionEntities extracts Latin-script proper names and acronyms of
// the question (e.g. "Stipendium Hungaricum", "GKS", "MEXT").
func reportQuestionEntities(goal string) []string {
	entities := []string{}
	seen := map[string]bool{}
	for _, match := range reportEntityCandidate.FindAllString(goal, -1) {
		match = strings.TrimSpace(strings.TrimRight(match, " &-"))
		for _, word := range []string{" and", " of", " de", " du", " der", " für"} {
			match = strings.TrimSuffix(match, word)
		}
		latin := false
		for _, r := range match {
			if r < 0x0400 && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= 0xC0) {
				latin = true
				break
			}
		}
		key := strings.ToLower(match)
		if !latin || len([]rune(match)) < 2 || reportEntityNoise[key] || seen[key] {
			continue
		}
		// A single capitalised word that is not an acronym is too ambiguous
		// (sentence starts, common nouns).
		if !strings.Contains(match, " ") && strings.ToUpper(match) != match {
			continue
		}
		seen[key] = true
		entities = append(entities, match)
	}
	return entities
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
