package orchestrator

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// A report claim unit is one paragraph line or one markdown list item that
// ends with one or more source markers, e.g. "- Python is used [1][3].".
// Units are the granularity of fact checking: each one is reviewed, kept or
// dropped on its own.
type reportUnit struct {
	Prefix      string   // list marker such as "- " or "2. ", empty for a paragraph
	Claim       string   // text without list marker and source markers
	EvidenceIDs []string // resolved, de-duplicated evidence IDs in marker order
}

func (u reportUnit) line() string {
	var markers strings.Builder
	for _, id := range u.EvidenceIDs {
		markers.WriteString("[" + id + "]")
	}
	return u.Prefix + u.Claim + " " + markers.String()
}

var reportListPrefix = regexp.MustCompile(`^(?:[-*+]|\d{1,2}[.)])\s+`)
var reportMarker = regexp.MustCompile(`\s*\[([^\[\]\n]{1,40})\]`)
var reportTrailingPunctuation = regexp.MustCompile(`^[\s.,;:!?…]*$`)

// reportLineProblem reports why a line cannot appear inside a section body.
// The backend renders headings, links and the source list itself.
func reportLineProblem(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "://") || strings.Contains(lower, "www.") || strings.Contains(lower, "mailto:") || strings.Contains(line, "]("):
		return "links and URLs are not allowed"
	case strings.ContainsAny(line, "<>"):
		return "HTML and angle brackets are not allowed"
	case strings.HasPrefix(line, "#"):
		return "headings are not allowed"
	case strings.HasPrefix(line, ">") || strings.HasPrefix(line, "```"):
		return "quotes and code blocks are not allowed"
	}
	return ""
}

// parseReportLine turns one non-empty body line into a unit. resolve maps a
// marker (alias or raw evidence ID) to an allowed evidence ID.
func parseReportLine(line string, resolve func(string) (string, bool)) (reportUnit, string) {
	line = strings.TrimSpace(line)
	if problem := reportLineProblem(line); problem != "" {
		return reportUnit{}, problem
	}
	prefix := reportListPrefix.FindString(line)
	text := line[len(prefix):]
	matches := reportMarker.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return reportUnit{}, "a line has no source marker like [1] at the end"
	}
	// Markers must close the unit; only punctuation may follow the last one.
	if !reportTrailingPunctuation.MatchString(text[matches[len(matches)-1][1]:]) {
		return reportUnit{}, "source markers must be at the end of each paragraph or list item"
	}
	ids := []string{}
	for _, match := range matches {
		marker := text[match[2]:match[3]]
		id, ok := resolve(marker)
		if !ok {
			return reportUnit{}, fmt.Sprintf("unknown source marker [%s]", marker)
		}
		if !contains(ids, id) {
			ids = append(ids, id)
		}
	}
	tail := strings.TrimSpace(text[matches[len(matches)-1][1]:])
	claim := strings.TrimSpace(reportMarker.ReplaceAllString(text, ""))
	if tail != "" {
		// "Fact [1]." becomes "Fact." and "Fact. [1]." stays "Fact.".
		claim = strings.TrimSpace(strings.TrimSuffix(claim, tail))
		if last, _ := utf8.DecodeLastRuneInString(claim); !strings.ContainsRune(".!?…:;,", last) {
			claim += tail
		}
	}
	claim = strings.Join(strings.Fields(claim), " ")
	if claim == "" || len(claim) > 1600 || reportTrailingPunctuation.MatchString(claim) {
		return reportUnit{}, "a source marker has no claim text"
	}
	return reportUnit{Prefix: normalizeReportPrefix(prefix), Claim: claim, EvidenceIDs: ids}, ""
}

func normalizeReportPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	switch prefix {
	case "":
		return ""
	case "*", "+":
		prefix = "-"
	}
	return prefix + " "
}

// parseReportDraft extracts claim units from a model draft. Lines that are
// not valid units are dropped and described in problems; units beyond limit
// are dropped too. The caller decides whether anything usable remains.
func parseReportDraft(body string, resolve func(string) (string, bool), limit int) ([]reportUnit, []string) {
	units := []reportUnit{}
	problems := []string{}
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			problems = appendUnique(problems, "quotes and code blocks are not allowed")
			continue
		}
		if line == "" || inFence {
			continue
		}
		unit, problem := parseReportLine(line, resolve)
		if problem != "" {
			problems = appendUnique(problems, problem)
			continue
		}
		if len(units) == limit {
			problems = appendUnique(problems, fmt.Sprintf("at most %d paragraphs or list items are allowed", limit))
			continue
		}
		units = append(units, unit)
	}
	return units, problems
}

func appendUnique(items []string, item string) []string {
	if contains(items, item) {
		return items
	}
	return append(items, item)
}

// renderReportUnits rebuilds a canonical section body: consecutive list items
// stay one list; everything else is separated into paragraphs.
func renderReportUnits(units []reportUnit) string {
	var body strings.Builder
	for index, unit := range units {
		if index > 0 {
			if unit.Prefix != "" && units[index-1].Prefix != "" {
				body.WriteString("\n")
			} else {
				body.WriteString("\n\n")
			}
		}
		body.WriteString(unit.line())
	}
	return body.String()
}

// parseReportBody strictly parses a persisted canonical section body: every
// line must be a valid unit. It also accepts bodies written by the previous
// one-paragraph-one-[evidence_id] format.
func parseReportBody(body string, catalogue []citationEvidence, limit int) ([]reportUnit, error) {
	resolve := reportCatalogueResolver(catalogue, nil)
	units, problems := parseReportDraft(body, resolve, limit)
	if len(problems) > 0 || len(units) == 0 || len(body) > 24_000 {
		return nil, errCitationMapping
	}
	return units, nil
}

// reportCatalogueResolver accepts a numbered alias or a raw evidence ID (as
// written by pinned pre-alias inputs) when it names a catalogue item.
func reportCatalogueResolver(catalogue []citationEvidence, aliases *evidenceAliases) func(string) (string, bool) {
	allowed := map[string]bool{}
	for _, item := range catalogue {
		allowed[item.EvidenceID] = true
	}
	return func(marker string) (string, bool) {
		marker = strings.TrimSpace(marker)
		if aliases != nil {
			marker = aliases.resolve(marker)
		}
		return marker, allowed[marker]
	}
}

// reportCriticClaims is the critic's view of the units: claim text plus the
// cited sources. The evidence_id field keeps the previous critic input shape.
func reportCriticClaims(units []reportUnit) []map[string]any {
	claims := make([]map[string]any, 0, len(units))
	for _, unit := range units {
		claims = append(claims, map[string]any{"claim": unit.Claim, "evidence_id": unit.EvidenceIDs[0], "sources": unit.EvidenceIDs})
	}
	return claims
}

func normalizeReportClaim(text string) string {
	text = strings.ReplaceAll(text, "**", "")
	return strings.Join(strings.Fields(text), " ")
}

func reportQuoteSupported(quote, excerpt string) bool {
	if len(strings.TrimSpace(quote)) < 8 {
		return false
	}
	return strings.Contains(excerpt, quote) || strings.Contains(strings.Join(strings.Fields(excerpt), " "), strings.Join(strings.Fields(quote), " "))
}

// reportClaimSupported checks one review claim against one unit.
func reportClaimSupported(unit reportUnit, claim citationClaim, excerpts map[string]string) bool {
	if normalizeReportClaim(claim.Claim) != normalizeReportClaim(unit.Claim) || claim.Kind != "supported" || !contains(unit.EvidenceIDs, claim.EvidenceID) {
		return false
	}
	excerpt, ok := excerpts[claim.EvidenceID]
	return ok && reportQuoteSupported(claim.Quote, excerpt)
}

func reportExcerpts(catalogue []citationEvidence) map[string]string {
	excerpts := map[string]string{}
	for _, item := range catalogue {
		excerpts[item.EvidenceID] = item.Excerpt
	}
	return excerpts
}

// validateReportReview strictly checks a persisted review against the units
// of a persisted body: one supported claim per unit, in order.
func validateReportReview(units []reportUnit, claims []citationClaim, catalogue []citationEvidence) error {
	if len(units) != len(claims) {
		return errCitationMapping
	}
	excerpts := reportExcerpts(catalogue)
	for index, unit := range units {
		if !reportClaimSupported(unit, claims[index], excerpts) {
			return errCitationMapping
		}
	}
	return nil
}
