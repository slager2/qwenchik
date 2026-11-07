package orchestrator

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

// Display rules for verified fact records. A record is verified against its
// quote when it is extracted; these rules only decide whether and where it
// is shown: values must be short, complete and in the report language, a
// section shows only the facts of its own entity, and each entity shows one
// value per attribute. The quote stays in the state for verification and is
// never rendered.
const (
	reportDisplayValueRunes = 90
	reportDisplayValueWords = 14
)

// reportAttributeCategory groups attribute labels that mean the same thing
// ("дедлайн", "дедлайн приёма", "deadline") so tables line up and gaps can be
// named per entity.
type reportAttributeCategory struct {
	key, labelRU, labelEN string
	attribute, question   *regexp.Regexp
	priority              int // key-numbers order, lower first
}

// Order matters: an attribute takes the first category it matches ("сбор за
// подачу" is a fee, not a deadline).
var reportAttributeCategories = []reportAttributeCategory{
	{key: "fee", labelRU: "сборы и расходы", labelEN: "fees and costs", priority: 6,
		attribute: regexp.MustCompile(`сбор|взнос|пошлин|стоимост|стоит|апостил|виз[аы]|\bfee|\bcost|\bvisa`),
		question:  regexp.MustCompile(`сбор|стоит|стоимост|расход|\bfee|\bcost`)},
	{key: "stipend", labelRU: "стипендия", labelEN: "stipend", priority: 1,
		attribute: regexp.MustCompile(`стипенди|в месяц|stipend|monthly|allowance`),
		question:  regexp.MustCompile(`стипенди|stipend`)},
	{key: "coverage", labelRU: "покрытие", labelEN: "coverage", priority: 8,
		attribute: regexp.MustCompile(`покры|cover|что входит|включ`),
		question:  regexp.MustCompile(`покры|cover`)},
	{key: "deadline", labelRU: "дедлайн", labelEN: "deadline", priority: 2,
		attribute: regexp.MustCompile(`дедлайн|срок|при[её]м|заявк|подач|deadline|application period`),
		question:  regexp.MustCompile(`дедлайн|срок|deadline`)},
	{key: "places", labelRU: "места и конкурс", labelEN: "places and competition", priority: 3,
		attribute: regexp.MustCompile(`мест|квот|конкурс|принят|поступил|seats|places|quota|admitted`),
		question:  regexp.MustCompile(`статистик|конкурс|сколько поступ|мест|statistic|places|seats`)},
	{key: "gpa", labelRU: "средний балл", labelEN: "GPA", priority: 4,
		attribute: regexp.MustCompile(`балл|gpa|оценк|аттестат|успеваем|grade`),
		question:  regexp.MustCompile(`gpa|балл|grade`)},
	{key: "language", labelRU: "язык (IELTS/TOEFL)", labelEN: "language (IELTS/TOEFL)", priority: 5,
		attribute: regexp.MustCompile(`язык|ielts|toefl|duolingo|language|english`),
		question:  regexp.MustCompile(`ielts|toefl|язык|language`)},
	{key: "age", labelRU: "возраст", labelEN: "age", priority: 7,
		attribute: regexp.MustCompile(`возраст|\bage\b`),
		question:  regexp.MustCompile(`возраст|\bage\b`)},
}

func reportCategoryOf(attribute string) (reportAttributeCategory, bool) {
	text := strings.ToLower(attribute)
	for _, category := range reportAttributeCategories {
		if category.attribute.MatchString(text) {
			return category, true
		}
	}
	return reportAttributeCategory{}, false
}

// reportAttributeKey is the table column of an attribute label.
func reportAttributeKey(attribute string) string {
	if category, ok := reportCategoryOf(attribute); ok {
		return "category:" + category.key
	}
	return looseQuoteText(attribute)
}

func reportAttributePriority(attribute string) int {
	if category, ok := reportCategoryOf(attribute); ok {
		return category.priority
	}
	return 9
}

// reportQuestionCategories are the attribute categories the question asks
// about, in category order.
func reportQuestionCategories(question string) []reportAttributeCategory {
	text := strings.ToLower(question)
	categories := []reportAttributeCategory{}
	for _, category := range reportAttributeCategories {
		if category.question.MatchString(text) {
			categories = append(categories, category)
		}
	}
	return categories
}

var (
	reportEmptyValue     = regexp.MustCompile(`не указан|нет данных|нет информации|неизвестн|not stated|not specified|not available|unknown|^n/?a$|^[-—–]$`)
	reportDanglingTail   = regexp.MustCompile(`[\s,;:(\-–—/]+$`)
	reportConnectorWords = map[string]bool{"and": true, "or": true, "of": true, "the": true, "a": true, "an": true, "to": true, "for": true, "with": true, "и": true, "или": true, "в": true, "на": true, "с": true, "по": true, "для": true, "от": true, "к": true}
	reportLatinWord      = regexp.MustCompile(`\p{Latin}+`)
	reportLatinAllowed   = map[string]bool{"phd": true, "bsc": true, "msc": true, "online": true}
)

// reportCompleteValue trims a dangling tail ("… 6,500 TL (" → "… 6,500 TL")
// and reports whether what is left reads as a complete value.
func reportCompleteValue(value, quote string) (string, bool) {
	value = reportDanglingTail.ReplaceAllString(value, "")
	if strings.Count(value, "(") > strings.Count(value, ")") {
		value = reportDanglingTail.ReplaceAllString(value[:strings.LastIndex(value, "(")], "")
	}
	if value == "" || strings.Count(value, "(") != strings.Count(value, ")") {
		return value, false
	}
	words := strings.Fields(strings.ToLower(value))
	if reportConnectorWords[strings.Trim(words[len(words)-1], ".,;:!?")] {
		return value, false
	}
	return value, !reportValueCutInQuote(value, quote)
}

// reportValueCutInQuote reports a value copied from the quote that starts
// or ends inside a word of the quote ("… many programmes in Turk").
func reportValueCutInQuote(value, quote string) bool {
	needle, haystack := looseQuoteText(value), looseQuoteText(reportPlainText(quote))
	if needle == "" {
		return false
	}
	found := false
	for offset := 0; ; {
		index := strings.Index(haystack[offset:], needle)
		if index < 0 {
			break
		}
		start, end := offset+index, offset+index+len(needle)
		found = true
		if (start == 0 || haystack[start-1] == ' ') && (end == len(haystack) || haystack[end] == ' ') {
			return false
		}
		offset = start + 1
	}
	return found
}

func reportHasCyrillic(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// reportLatinTokenAllowed are Latin words that stay Latin in a Russian
// value: codes and acronyms (TL, KRW, IELTS, GPA, CET), degree names.
func reportLatinTokenAllowed(word string) bool {
	if reportLatinAllowed[strings.ToLower(word)] {
		return true
	}
	return utf8.RuneCountInString(word) <= 6 && strings.ToUpper(word) == word
}

// reportValueRussian reports whether a value reads as Russian: numbers and
// codes alone, or Cyrillic words at least as many as other Latin words.
func reportValueRussian(value string) bool {
	cyrillic, latin := 0, 0
	for _, word := range strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		switch {
		case reportHasCyrillic(word):
			cyrillic++
		case reportLatinWord.MatchString(word) && !reportLatinTokenAllowed(word):
			latin++
		}
	}
	if cyrillic == 0 {
		return latin == 0
	}
	return latin <= cyrillic
}

var (
	reportMonthsRU = map[string][2]string{
		"january": {"январь", "января"}, "february": {"февраль", "февраля"}, "march": {"март", "марта"}, "april": {"апрель", "апреля"},
		"may": {"май", "мая"}, "june": {"июнь", "июня"}, "july": {"июль", "июля"}, "august": {"август", "августа"},
		"september": {"сентябрь", "сентября"}, "october": {"октябрь", "октября"}, "november": {"ноябрь", "ноября"}, "december": {"декабрь", "декабря"},
		"jan": {"январь", "января"}, "feb": {"февраль", "февраля"}, "mar": {"март", "марта"}, "apr": {"апрель", "апреля"}, "jun": {"июнь", "июня"},
		"jul": {"июль", "июля"}, "aug": {"август", "августа"}, "sep": {"сентябрь", "сентября"}, "sept": {"сентябрь", "сентября"}, "oct": {"октябрь", "октября"},
		"nov": {"ноябрь", "ноября"}, "dec": {"декабрь", "декабря"},
	}
	reportDateWordsRU = map[string]string{"to": "–", "until": "до", "till": "до", "by": "до", "from": "с", "and": "и", "or": "или", "usually": "обычно", "between": "между", "each": "каждый", "year": "год"}
)

// reportTranslateDate turns an English date value ("Usually 10 January – 20
// February", "15 to 30 September 2026") into Russian. Values with any other
// English word are returned unchanged; numbers are never touched.
func reportTranslateDate(value string) string {
	matches := reportLatinWord.FindAllStringIndex(value, -1)
	months := 0
	for _, match := range matches {
		word := strings.ToLower(value[match[0]:match[1]])
		if _, ok := reportMonthsRU[word]; ok {
			months++
			continue
		}
		if _, ok := reportDateWordsRU[word]; !ok && !reportLatinTokenAllowed(value[match[0]:match[1]]) {
			return value
		}
	}
	if months == 0 {
		return value
	}
	var b strings.Builder
	last := 0
	for _, match := range matches {
		word := value[match[0]:match[1]]
		b.WriteString(value[last:match[0]])
		last = match[1]
		lower := strings.ToLower(word)
		if forms, ok := reportMonthsRU[lower]; ok {
			before := strings.TrimRight(value[:match[0]], " ")
			if before != "" && unicode.IsDigit(rune(before[len(before)-1])) {
				b.WriteString(forms[1])
			} else {
				b.WriteString(forms[0])
			}
			continue
		}
		if replacement, ok := reportDateWordsRU[lower]; ok {
			b.WriteString(replacement)
			continue
		}
		b.WriteString(word)
	}
	b.WriteString(value[last:])
	text := strings.Join(strings.Fields(b.String()), " ")
	if text != "" {
		first, size := utf8.DecodeRuneInString(text)
		text = string(unicode.ToUpper(first)) + text[size:]
	}
	return text
}

// reportDisplayValue is the value as shown in tables, or ok=false when the
// record must not be shown: too long, cut, "not stated", or (in a Russian
// report) not in Russian. Every number of the shown value still occurs in
// the quote.
func reportDisplayValue(fact domain.ReportFact, russian bool) (string, bool) {
	value, ok := reportCompleteValue(reportPlainText(fact.Value), fact.Quote)
	if !ok {
		return "", false
	}
	if russian {
		value = reportTranslateDate(value)
	}
	if reportEmptyValue.MatchString(strings.ToLower(strings.Trim(value, " .!"))) || utf8.RuneCountInString(value) > reportDisplayValueRunes || len(strings.Fields(value)) > reportDisplayValueWords {
		return "", false
	}
	if russian && !reportValueRussian(value) {
		return "", false
	}
	if !reportNumbersGrounded(value, fact.Quote) {
		return "", false
	}
	return value, true
}

// reportSectionEntity is the one known entity a section title names, or "".
func reportSectionEntity(title string, known []string) string {
	found := ""
	for _, name := range known {
		if looseQuoteText(name) == looseQuoteText(title) {
			return name
		}
		if reportEntityNamed(name, title) && reportEntityNamed(title, name) {
			if found != "" {
				return ""
			}
			found = name
		}
	}
	return found
}

// reportValueScore orders duplicate values: Russian first, then shorter.
func reportValueScore(value string) (int, int) {
	other := 1
	if reportHasCyrillic(value) {
		other = 0
	}
	return other, utf8.RuneCountInString(value)
}

// reportDisplayFacts returns the facts each section shows. A fact about
// another entity moves to that entity's section (or is dropped when no
// section covers it); a section about one entity never shows facts of
// another. Each entity keeps one value per attribute: the Russian, shortest
// complete one.
func reportDisplayFacts(report *domain.ResearchReportState, question string) [][]domain.ReportFact {
	russian := reportLanguage(question) == "ru"
	groups := newReportEntityGroups(report.Entities)
	sectionEntity := make([]string, len(report.Sections))
	sectionOf := map[string]int{}
	for index, section := range report.Sections {
		if name := reportSectionEntity(section.Title, report.Entities); name != "" {
			sectionEntity[index] = groups.key(name)
			if _, ok := sectionOf[sectionEntity[index]]; !ok {
				sectionOf[sectionEntity[index]] = index
			}
		}
	}
	type placed struct {
		section, order int
		fact           domain.ReportFact
	}
	best := map[string]placed{}
	order := 0
	for index, section := range report.Sections {
		for _, fact := range section.Facts {
			value, ok := reportDisplayValue(fact, russian)
			if !ok {
				continue
			}
			fact.Value = value
			entity := groups.key(fact.Entity)
			target := index
			if sectionEntity[index] != "" && sectionEntity[index] != entity {
				owner, covered := sectionOf[entity]
				if !covered {
					continue
				}
				target = owner
			}
			key := entity + "|" + reportAttributeKey(fact.Attribute)
			if current, ok := best[key]; ok {
				otherA, lengthA := reportValueScore(current.fact.Value)
				otherB, lengthB := reportValueScore(value)
				if otherA < otherB || otherA == otherB && lengthA <= lengthB {
					continue
				}
				best[key] = placed{section: current.section, order: current.order, fact: fact}
				continue
			}
			best[key] = placed{section: target, order: order, fact: fact}
			order++
		}
	}
	all := make([]placed, 0, len(best))
	for _, item := range best {
		all = append(all, item)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].order < all[j].order })
	facts := make([][]domain.ReportFact, len(report.Sections))
	for _, item := range all {
		facts[item.section] = append(facts[item.section], item.fact)
	}
	return facts
}

const (
	reportExcerptChars   = 500
	reportExcerptSources = 2
)

var (
	reportSentencePattern = regexp.MustCompile(`[^.!?…]+[.!?…]+["»”')\]]*`)
	reportBoilerplate     = regexp.MustCompile(`(?i)cookie|subscribe|sign up|sign in|log in|newsletter|javascript|click here|read more|all rights reserved|privacy policy|terms of use|©|open to you|on this page|подпис|войти|зарегистр|читать далее|политика конфиденциальности|все права защищены`)
)

// reportExcerptSentences are the readable sentences of an excerpt: page
// navigation, counters, table rows and link lists are left out.
func reportExcerptSentences(excerpt string) []string {
	sentences := []string{}
	for _, line := range strings.Split(excerpt, "\n") {
		line = reportPlainText(line)
		if strings.HasPrefix(line, "|") || strings.Contains(line, "·") || len(strings.Fields(line)) < 6 {
			continue
		}
		for _, sentence := range reportSentencePattern.FindAllString(line, -1) {
			sentence = strings.TrimSpace(sentence)
			if len(strings.Fields(sentence)) < 6 || utf8.RuneCountInString(sentence) > reportExcerptChars || reportBoilerplate.MatchString(sentence) || strings.Contains(sentence, "[...]") {
				continue
			}
			letters := 0
			for _, r := range sentence {
				if unicode.IsLetter(r) {
					letters++
				}
			}
			if letters*2 < utf8.RuneCountInString(sentence) {
				continue
			}
			sentences = append(sentences, sentence)
		}
	}
	return sentences
}

type reportExcerpt struct {
	Text, EvidenceID string
}

// reportShortExcerpts picks at most two short excerpts (whole sentences, at
// most 500 characters each) for a section without verified text. A section
// about one entity shows only sentences that name it.
func reportShortExcerpts(sources []citationEvidence, entity string) []reportExcerpt {
	excerpts := []reportExcerpt{}
	seen := map[string]bool{}
	for _, source := range sources {
		if len(excerpts) == reportExcerptSources {
			break
		}
		text := ""
		for _, sentence := range reportExcerptSentences(source.Excerpt) {
			key := looseQuoteText(sentence)
			if seen[key] || entity != "" && !reportEntityNamed(entity, sentence) {
				continue
			}
			if utf8.RuneCountInString(text)+1+utf8.RuneCountInString(sentence) > reportExcerptChars {
				break
			}
			seen[key] = true
			text = strings.TrimSpace(text + " " + sentence)
		}
		if text != "" {
			excerpts = append(excerpts, reportExcerpt{Text: text, EvidenceID: source.EvidenceID})
		}
	}
	return excerpts
}

var (
	reportOfficialGap       = regexp.MustCompile(`^No official source has been fetched yet for (.+?)\.?$`)
	reportFreshnessGap      = regexp.MustCompile(`^The accepted evidence names no year after (\d{4}); current \((\d{4})\)`)
	reportContradictions    = regexp.MustCompile(`^Unresolved contradictions remain`)
	reportInternalCriterion = regexp.MustCompile(`(?i)criterion|\bstep\b|"[a-z]+_\d+[a-z_\d]*"|\bgoal\.\d|tool result|evidence id|adaptive round|verifier`)
)

// reportLimitationItems turns verification gaps into reader-facing lines.
// Execution criteria (step ids, fetch and acceptance bookkeeping) and, in a
// Russian report, untranslated verifier text are not listed one by one but
// counted in one line.
func reportLimitationItems(items []string, russian bool) []string {
	lines, official := []string{}, []string{}
	hidden := 0
	for _, item := range items {
		item = strings.TrimSpace(item)
		switch match, years := reportOfficialGap.FindStringSubmatch(item), reportFreshnessGap.FindStringSubmatch(item); {
		case match != nil:
			official = append(official, match[1])
		case years != nil && russian:
			lines = append(lines, "Источники не называют годов позже "+years[1]+": актуальные ("+years[2]+") официальные даты и дедлайны не найдены.")
		case reportContradictions.MatchString(item) && russian:
			lines = append(lines, "В источниках остались противоречия; спорные утверждения не приводятся как установленные факты.")
		case reportInternalCriterion.MatchString(item) || russian && !reportHasCyrillic(item):
			hidden++
		default:
			lines = append(lines, item)
		}
	}
	if len(official) > 0 {
		line := "No official site was fetched for: " + strings.Join(official, ", ") + "; their data comes from other sites."
		if russian {
			line = "Официальные сайты не загружены для: " + strings.Join(official, ", ") + "; сведения о них взяты со сторонних сайтов."
		}
		lines = append([]string{line}, lines...)
	}
	if hidden > 0 {
		line := "Internal research checks not passed: " + strconv.Itoa(hidden) + " (details in the research panel)."
		if russian {
			line = "Не пройдено служебных проверок хода исследования: " + strconv.Itoa(hidden) + " (подробности — в панели исследования)."
		}
		lines = append(lines, line)
	}
	return lines
}
