package orchestrator

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
)

// The report layout is rendered by code from verified state only: TL;DR
// bullets, fact records, kept section claims and the source catalogue. The
// model never writes tables, headings or links. Citation markers stay
// [<evidence-id>] in the stored Markdown; the chat UI and the PDF export
// render them as numbered links [1], [2] to the source list.

type reportLayoutLabels struct {
	TLDR, KeyNumbers, Comparison, Gaps, Checks, Facts                         string
	Entity, Attribute, Value, Source, Section, Status, Note                   string
	SourceNo, SourceTitle, SourceSite, SourceOfficial, SourceType, SourceYear string
	Yes, No, NotFound, NothingFound, NoTLDR, FactsVerified, TLDRModel         string
	TLDRFallback, TLDRCount, ComparisonNote, OfficialNote                     string
	FactsOnly, ExcerptsIntro, NoSectionData, Unrepresented                    string
}

var reportLayoutRU = reportLayoutLabels{
	TLDR: "Коротко", KeyNumbers: "Ключевые цифры", Comparison: "Сравнение", Gaps: "Что не найдено", Checks: "Проверка", Facts: "Факты",
	Entity: "Объект", Attribute: "Показатель", Value: "Значение", Source: "Источник", Section: "Раздел", Status: "Статус", Note: "Примечание",
	SourceNo: "№", SourceTitle: "Источник", SourceSite: "Сайт", SourceOfficial: "Офиц.", SourceType: "Тип", SourceYear: "Год",
	Yes: "да", No: "нет", NotFound: "не найдено в проверенных источниках", NothingFound: "подтверждённых сведений не найдено",
	NoTLDR:        "Проверенных выводов нет: ни одно утверждение не подтвердилось выдержками источников.",
	FactsVerified: "фактов проверено: %d", TLDRModel: "проверено по подтверждённым утверждениям", TLDRFallback: "первые подтверждённые утверждения разделов", TLDRCount: "выводов: %d",
	ComparisonNote: "Ячейка: значение (год данных) и источник. «—» — значение не найдено в проверенных источниках.",
	OfficialNote:   "«Офиц.» определяется по домену (государственные, образовательные и сайты программ).",
	FactsOnly:      "Связного текста не получилось подтвердить; ниже только проверенные факты.",
	ExcerptsIntro:  "Связного текста не получилось подтвердить; ниже короткие выдержки из источников.",
	NoSectionData:  "Подтверждённых сведений по этому разделу в полученных источниках не найдено.",
	Unrepresented:  "Не все пункты вопроса раскрыты подтверждёнными разделами (%d); пробелы перечислены в разделе «Что не найдено».",
}

var reportLayoutEN = reportLayoutLabels{
	TLDR: "TL;DR", KeyNumbers: "Key numbers", Comparison: "Comparison", Gaps: "Not found", Checks: "Verification", Facts: "Facts",
	Entity: "Entity", Attribute: "Attribute", Value: "Value", Source: "Source", Section: "Section", Status: "Status", Note: "Note",
	SourceNo: "#", SourceTitle: "Source", SourceSite: "Site", SourceOfficial: "Official", SourceType: "Type", SourceYear: "Year",
	Yes: "yes", No: "no", NotFound: "not found in verified sources", NothingFound: "no verified information found",
	NoTLDR:        "No verified conclusions: no statement was supported by the source excerpts.",
	FactsVerified: "facts verified: %d", TLDRModel: "checked against verified statements", TLDRFallback: "first verified statement of each section", TLDRCount: "conclusions: %d",
	ComparisonNote: "Cell: value (year of data) and source. \"—\": not found in verified sources.",
	OfficialNote:   "\"Official\" is derived from the domain (government, education and program sites).",
	FactsOnly:      "The section text could not be verified; only verified facts are shown.",
	ExcerptsIntro:  "The section text could not be verified; short source excerpts follow.",
	NoSectionData:  "No verified information for this section was found in the fetched sources.",
	Unrepresented:  "Not every part of the question is covered by a verified section (%d); gaps are listed under \"Not found\".",
}

func reportLayoutFor(question string) reportLayoutLabels {
	if reportLanguage(question) == "ru" {
		return reportLayoutRU
	}
	return reportLayoutEN
}

// reportCell escapes model/source text for one table cell; reportText
// already escapes "|" (and every other Markdown control character).
func reportCell(text string) string {
	return reportText(text)
}

func reportMarkers(ids []string) string {
	var b strings.Builder
	for _, id := range ids {
		b.WriteString("[" + id + "]")
	}
	return b.String()
}

// reportFactValue is "value (year)"; the year is left out when the value
// already states it.
func reportFactValue(fact domain.ReportFact) string {
	cell := reportCell(fact.Value)
	if fact.Year != "" && !strings.Contains(fact.Value, fact.Year) {
		cell += " (" + reportCell(fact.Year) + ")"
	}
	return cell
}

func reportFactCell(fact domain.ReportFact) string {
	return reportFactValue(fact) + " [" + fact.EvidenceID + "]"
}

// reportEntityGroups maps each fact entity to a display entity: a question
// or outline entity it names, else the first spelling seen.
type reportEntityGroups struct {
	order   []string
	display map[string]string
	known   []string
}

func newReportEntityGroups(known []string) *reportEntityGroups {
	return &reportEntityGroups{display: map[string]string{}, known: known}
}

func (g *reportEntityGroups) key(entity string) string {
	for _, name := range g.known {
		if looseQuoteText(name) == looseQuoteText(entity) || reportEntityNamed(name, entity) && reportEntityNamed(entity, name) {
			entity = name
			break
		}
	}
	key := looseQuoteText(entity)
	if _, ok := g.display[key]; !ok {
		g.display[key] = entity
		g.order = append(g.order, key)
	}
	return key
}

type reportFactTable struct {
	entities   []string // keys in order
	attributes []string // keys in order
	attrName   map[string]string
	cells      map[string][]domain.ReportFact // entity|attribute
	groups     *reportEntityGroups
}

func buildReportFactTable(sections [][]domain.ReportFact, known []string) reportFactTable {
	table := reportFactTable{attrName: map[string]string{}, cells: map[string][]domain.ReportFact{}, groups: newReportEntityGroups(known)}
	seenEntity := map[string]bool{}
	for _, facts := range sections {
		for _, fact := range facts {
			entity := table.groups.key(fact.Entity)
			attribute := reportAttributeKey(fact.Attribute)
			if !seenEntity[entity] {
				seenEntity[entity] = true
				table.entities = append(table.entities, entity)
			}
			if _, ok := table.attrName[attribute]; !ok {
				table.attrName[attribute] = fact.Attribute
				table.attributes = append(table.attributes, attribute)
			}
			table.cells[entity+"|"+attribute] = append(table.cells[entity+"|"+attribute], fact)
		}
	}
	return table
}

// comparisonColumns are at most five attributes: those shared by most
// entities first, then by key-number priority.
func (t reportFactTable) comparisonColumns() []string {
	counts := map[string]int{}
	for _, attribute := range t.attributes {
		for _, entity := range t.entities {
			if len(t.cells[entity+"|"+attribute]) > 0 {
				counts[attribute]++
			}
		}
	}
	byPriority := func(columns []string) []string {
		sort.SliceStable(columns, func(i, j int) bool {
			return reportAttributePriority(t.attrName[columns[i]]) < reportAttributePriority(t.attrName[columns[j]])
		})
		return columns
	}
	shared, single := []string{}, []string{}
	for _, attribute := range t.attributes {
		if counts[attribute] >= 2 {
			shared = append(shared, attribute)
		} else {
			single = append(single, attribute)
		}
	}
	columns := byPriority(shared)
	if len(columns) < 2 {
		// Entities with no attribute in common are still compared on
		// their most important attributes.
		columns = append(columns, byPriority(single)...)
		return columns[:min(4, len(columns))]
	}
	return columns[:min(5, len(columns))]
}

func (t reportFactTable) cell(entity, attribute string) string {
	facts := t.cells[entity+"|"+attribute]
	parts := []string{}
	for _, fact := range facts[:min(2, len(facts))] {
		parts = append(parts, reportFactCell(fact))
	}
	return strings.Join(parts, "; ")
}

func writeComparison(b *strings.Builder, table reportFactTable, columns []string, layout reportLayoutLabels) {
	fmt.Fprintf(b, "## %s\n\n| %s |", layout.Comparison, layout.Entity)
	for _, column := range columns {
		fmt.Fprintf(b, " %s |", reportCell(table.attrName[column]))
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(columns)) + "\n")
	for _, entity := range table.entities {
		fmt.Fprintf(b, "| **%s** |", reportCell(table.groups.display[entity]))
		for _, column := range columns {
			cell := table.cell(entity, column)
			if cell == "" {
				cell = "—"
			}
			fmt.Fprintf(b, " %s |", cell)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "\n%s\n\n", layout.ComparisonNote)
}

const (
	reportKeyNumbersPerEntity = 2
	reportKeyNumbersMax       = 8
)

// keyNumbers is the dashboard: round-robin over entities, each giving its
// most important numeric facts (stipend, deadline, places, GPA/language)
// first, at most two per entity.
func keyNumbers(table reportFactTable) []domain.ReportFact {
	candidates := map[string][]domain.ReportFact{}
	for _, entity := range table.entities {
		for _, attribute := range table.attributes {
			if facts := table.cells[entity+"|"+attribute]; len(facts) > 0 && len(reportNumbers(facts[0].Value)) > 0 {
				candidates[entity] = append(candidates[entity], facts[0])
			}
		}
		sort.SliceStable(candidates[entity], func(i, j int) bool {
			return reportAttributePriority(candidates[entity][i].Attribute) < reportAttributePriority(candidates[entity][j].Attribute)
		})
	}
	picked := []domain.ReportFact{}
	for round := 0; round < reportKeyNumbersPerEntity; round++ {
		for _, entity := range table.entities {
			if round < len(candidates[entity]) && len(picked) < reportKeyNumbersMax {
				picked = append(picked, candidates[entity][round])
			}
		}
	}
	return picked
}

func writeFactsTable(b *strings.Builder, facts []domain.ReportFact, layout reportLayoutLabels, withEntity bool) {
	if withEntity {
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n|---|---|---|---|\n", layout.Entity, layout.Attribute, layout.Value, layout.Source)
	} else {
		fmt.Fprintf(b, "| %s | %s | %s |\n|---|---|---|\n", layout.Attribute, layout.Value, layout.Source)
	}
	for _, fact := range facts {
		value := reportFactValue(fact)
		if withEntity {
			fmt.Fprintf(b, "| %s | %s | %s | [%s] |\n", reportCell(fact.Entity), reportCell(fact.Attribute), value, fact.EvidenceID)
		} else {
			fmt.Fprintf(b, "| %s | %s | [%s] |\n", reportCell(fact.Attribute), value, fact.EvidenceID)
		}
	}
	b.WriteString("\n")
}

var reportYearPattern = regexp.MustCompile(`\b(20[0-3][0-9])\b`)

// reportSourceYear is the latest year named by the source title or URL.
func reportSourceYear(source citationEvidence) string {
	year := ""
	for _, text := range []string{source.Title, source.URL} {
		for _, match := range reportYearPattern.FindAllStringSubmatch(text, -1) {
			if match[1] > year {
				year = match[1]
			}
		}
	}
	return year
}

var reportOfficialLabels = map[string]bool{"gov": true, "edu": true, "ac": true, "go": true, "gouv": true, "gob": true, "govt": true, "int": true, "mil": true}

// reportOfficialHost recognises government, education and university hosts.
func reportOfficialHost(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	labels := strings.Split(host, ".")
	for _, label := range labels[:max(0, len(labels)-1)] {
		if reportOfficialLabels[label] {
			return true
		}
	}
	if len(labels) > 0 && reportOfficialLabels[labels[len(labels)-1]] {
		return true
	}
	for _, word := range []string{"university", "univ", "uni-", "college", "stipendiumhungaricum", "turkiyeburslari", "daad", "campuschina", "studyinkorea", "studyinjapan", "mext"} {
		if strings.Contains(host, word) {
			return true
		}
	}
	return false
}

func reportSourceType(source citationEvidence) string {
	if source.PDF || strings.HasSuffix(strings.ToLower(strings.SplitN(source.URL, "?", 2)[0]), ".pdf") {
		return "PDF"
	}
	return "HTML"
}

func reportLinkURL(raw string) string {
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "|", "%7C", "<", "%3C", ">", "%3E", "[", "%5B", "]", "%5D").Replace(raw)
}

func writeSourcesTable(b *strings.Builder, catalogue []citationEvidence, labels reportLabels, layout reportLayoutLabels) {
	fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n|---|---|---|---|---|---|\n", layout.SourceNo, layout.SourceTitle, layout.SourceSite, layout.SourceOfficial, layout.SourceType, layout.SourceYear)
	for _, source := range catalogue {
		host := ""
		if parsed, err := url.Parse(source.URL); err == nil {
			host = strings.TrimPrefix(parsed.Hostname(), "www.")
		}
		official := layout.No
		if reportOfficialHost(host) {
			official = layout.Yes
		}
		title := reportCell(truncateRunes(source.Title, 140))
		if title == "" {
			title = reportCell(host)
		}
		if strings.HasPrefix(source.URL, "http://") || strings.HasPrefix(source.URL, "https://") {
			title = "[" + title + "](" + reportLinkURL(source.URL) + ")"
		}
		if source.SourceText != "" {
			title += " · " + reportCell(labels.PassageSource)
		}
		year := reportSourceYear(source)
		if year == "" {
			year = "—"
		}
		fmt.Fprintf(b, "| [%s] | %s | %s | %s | %s | %s |\n", source.EvidenceID, title, reportCell(host), official, reportSourceType(source), year)
	}
	b.WriteString("\n")
}

// dedupeReportBody drops body lines whose claim an earlier section already
// stated (render-only; the stored body keeps every verified unit).
func dedupeReportBody(body string, seen map[string]bool) string {
	lines := []string{}
	for _, line := range strings.Split(body, "\n") {
		key := looseQuoteText(reportMarkerPattern.ReplaceAllString(strings.TrimPrefix(strings.TrimSpace(line), "- "), ""))
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(regexp.MustCompile(`\n{3,}`).ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// reportGaps lists, per entity, the attributes the question asks about that
// no verified fact or verified section text covers ("Stipendium Hungaricum —
// дедлайн, стипендия"), and entities with no verified information at all.
func reportGaps(report *domain.ResearchReportState, table reportFactTable, goal string, layout reportLayoutLabels) []string {
	russian := reportLanguage(goal) == "ru"
	categories := reportQuestionCategories(goal)
	supported := ""
	bodies := map[string]string{}
	for _, section := range report.Sections {
		if section.Status != "supported" {
			continue
		}
		body := reportMarkerPattern.ReplaceAllString(section.Body, "")
		supported += " " + body
		if name := reportSectionEntity(section.Title, report.Entities); name != "" {
			bodies[table.groups.key(name)] += " " + strings.ToLower(body)
		}
	}
	entities := []string{}
	seen := map[string]bool{}
	for _, name := range report.Entities {
		if key := table.groups.key(name); !seen[key] {
			seen[key] = true
			entities = append(entities, key)
		}
	}
	for _, key := range table.entities {
		if !seen[key] {
			seen[key] = true
			entities = append(entities, key)
		}
	}
	columns := table.comparisonColumns()
	gaps := []string{}
	for _, key := range entities {
		name := table.groups.display[key]
		hasFacts := false
		for _, attribute := range table.attributes {
			hasFacts = hasFacts || len(table.cells[key+"|"+attribute]) > 0
		}
		if !hasFacts && bodies[key] == "" && !reportEntityNamed(name, supported) {
			gaps = append(gaps, fmt.Sprintf("**%s**: %s.", reportText(name), layout.NothingFound))
			continue
		}
		missing := []string{}
		if len(categories) > 0 {
			for _, category := range categories {
				if len(table.cells[key+"|category:"+category.key]) > 0 || category.attribute.MatchString(bodies[key]) {
					continue
				}
				label := category.labelEN
				if russian {
					label = category.labelRU
				}
				missing = append(missing, label)
			}
		} else if len(table.entities) >= 2 {
			for _, column := range columns {
				if len(table.cells[key+"|"+column]) == 0 {
					missing = append(missing, table.attrName[column])
				}
			}
		}
		if len(missing) > 0 {
			gaps = append(gaps, fmt.Sprintf("**%s** — %s: %s.", reportText(name), reportText(strings.Join(missing, ", ")), layout.NotFound))
		}
	}
	return gaps[:min(12, len(gaps))]
}

// writeFallbackSection renders a section whose text did not verify: its
// verified facts when there are any, else at most two short excerpts.
func writeFallbackSection(b *strings.Builder, section domain.ReportSection, facts []domain.ReportFact, entity string, catalogue []citationEvidence, layout reportLayoutLabels) {
	if len(facts) > 0 {
		fmt.Fprintf(b, "%s\n\n", layout.FactsOnly)
		writeFactsTable(b, facts, layout, reportFactEntities(facts) > 1)
		return
	}
	excerpts := reportShortExcerpts(selectReportEvidence(section.EvidenceIDs, catalogue), entity)
	if len(excerpts) == 0 {
		fmt.Fprintf(b, "%s\n\n", layout.NoSectionData)
		return
	}
	fmt.Fprintf(b, "%s\n\n", layout.ExcerptsIntro)
	for _, excerpt := range excerpts {
		fmt.Fprintf(b, "> %s [%s]\n\n", reportText(excerpt.Text), excerpt.EvidenceID)
	}
}

func reportFactEntities(facts []domain.ReportFact) int {
	entities := map[string]bool{}
	for _, fact := range facts {
		entities[looseQuoteText(fact.Entity)] = true
	}
	return len(entities)
}

// renderSectionedReport builds the report Markdown from verified state.
func renderSectionedReport(report *domain.ResearchReportState, goal, taskID string, catalogue []citationEvidence, missing, supplementary []string) string {
	labels, layout := reportLabelsFor(goal), reportLayoutFor(goal)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n## %s\n\n", reportText(report.Title), layout.TLDR)
	if len(report.TLDR) == 0 {
		fmt.Fprintf(&b, "%s\n\n", layout.NoTLDR)
	}
	for _, bullet := range report.TLDR {
		fmt.Fprintf(&b, "- %s %s\n", reportText(bullet.Text), reportMarkers(bullet.EvidenceIDs))
	}
	if len(report.TLDR) > 0 {
		b.WriteString("\n")
	}
	display := reportDisplayFacts(report, goal)
	table := buildReportFactTable(display, report.Entities)
	if numbers := keyNumbers(table); len(numbers) >= 2 {
		fmt.Fprintf(&b, "## %s\n\n", layout.KeyNumbers)
		writeFactsTable(&b, numbers, layout, true)
	}
	if columns := table.comparisonColumns(); len(table.entities) >= 2 && len(columns) > 0 {
		writeComparison(&b, table, columns, layout)
	}
	fmt.Fprintf(&b, "## %s\n\n", labels.ContentsHeading)
	for index, section := range report.Sections {
		fmt.Fprintf(&b, "%d. %s\n", index+1, reportText(section.Title))
	}
	seen := map[string]bool{}
	for index, section := range report.Sections {
		fmt.Fprintf(&b, "\n## %s\n\n", reportText(section.Title))
		facts := display[index]
		if section.Status != "supported" {
			writeFallbackSection(&b, section, facts, reportSectionEntity(section.Title, report.Entities), catalogue, layout)
			continue
		}
		if body := dedupeReportBody(section.Body, seen); strings.TrimSpace(body) != "" {
			fmt.Fprintf(&b, "%s\n\n", body)
		}
		if len(facts) > 0 {
			fmt.Fprintf(&b, "**%s**\n\n", layout.Facts)
			writeFactsTable(&b, facts, layout, reportFactEntities(facts) > 1)
		}
	}
	if gaps := reportGaps(report, table, goal, layout); len(gaps) > 0 {
		fmt.Fprintf(&b, "\n## %s\n\n", layout.Gaps)
		for _, gap := range gaps {
			fmt.Fprintf(&b, "- %s\n", gap)
		}
	}
	fmt.Fprintf(&b, "\n## %s\n\n%s\n", labels.QuestionHeading, reportQuestionText(goal))
	fmt.Fprintf(&b, "\n## %s\n\n%s\n", labels.LimitsHeading, labels.LimitsText)
	for _, item := range reportLimitationItems(append(append([]string{}, missing...), supplementary...), reportLanguage(goal) == "ru") {
		fmt.Fprintf(&b, "- %s\n", reportText(item))
	}
	for _, failure := range report.SourceLimitations {
		fmt.Fprintf(&b, "- "+labels.SourceUnused+"\n", reportText(publicError(failure.Code)), failure.Code, failure.FetchedBytes)
	}
	if len(report.UnrepresentedCriteria) > 0 {
		fmt.Fprintf(&b, "- "+layout.Unrepresented+"\n", len(report.UnrepresentedCriteria))
	}
	fmt.Fprintf(&b, "\n## %s\n\n%s\n", labels.MethodHeading, labels.MethodText)
	for _, note := range reportMethodNotes(labels, catalogue) {
		fmt.Fprintf(&b, "\n%s\n", note)
	}
	fmt.Fprintf(&b, "\n%s\n", layout.OfficialNote)
	fmt.Fprintf(&b, "\nPrivacy mode: `%s`.\n", report.PrivacyMode)
	if report.PrivacyMode == domain.PrivacyOffline {
		fmt.Fprintf(&b, "\n"+labels.OfflineText+"\n", report.OfflineCorpusSHA256)
	}
	fmt.Fprintf(&b, "\n## %s\n\n", labels.SourcesHeading)
	writeSourcesTable(&b, catalogue, labels, layout)
	fmt.Fprintf(&b, "## %s\n\n| %s | %s | %s |\n|---|---|---|\n", layout.Checks, layout.Section, layout.Status, layout.Note)
	tldrNote := layout.TLDRFallback
	if report.TLDRStatus == "model" {
		tldrNote = layout.TLDRModel
	}
	fmt.Fprintf(&b, "| %s | %s | %s |\n", layout.TLDR, tldrNote, fmt.Sprintf(layout.TLDRCount, len(report.TLDR)))
	for index, section := range report.Sections {
		notes := []string{}
		if section.Limitation != "" {
			notes = append(notes, reportCell(labels.limitation(section.Limitation)))
		}
		if len(display[index]) > 0 {
			notes = append(notes, fmt.Sprintf(layout.FactsVerified, len(display[index])))
		}
		note := strings.Join(notes, "; ")
		if note == "" {
			note = "—"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", reportCell(section.Title), labels.status(section.Status), note)
	}
	fmt.Fprintf(&b, "\n## %s\n\n"+labels.CoverageText+"\n\nGenerated at: %s · Run ID: %s\n", labels.CoverageHeading, len(catalogue), len(report.Sections), len(missing), report.Outcome, report.GeneratedAt, taskID)
	return b.String()
}
