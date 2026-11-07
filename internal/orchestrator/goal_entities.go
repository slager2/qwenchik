package orchestrator

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// One extractor names the entities of a research goal for search facets,
// the freshness/official-source rule, site discovery and the report
// outline. A live run treated "Computer Science", "Software Engineering"
// and "Data Science" of a scholarship request as entities: their facets
// fetched conference call-for-papers pages and the freshness rule demanded
// "an official source for Computer Science" while the named programmes'
// official sites stayed unfetched.

// maxGoalEntities bounds the named entities taken from one research goal.
const maxGoalEntities = 8

// goalEntity is one named entity of a goal with an optional hint given in
// parentheses after it ("GKS (Корея)" → GKS, hint Корея).
type goalEntity struct {
	Name string
	Hint string
}

// enumerationMarkers introduce an explicit list of examples.
var enumerationMarkerPattern = regexp.MustCompile(`(?i)(?:^|[\s(,;:])(например|к примеру|в том числе|такие как|таких как|e\.g\.|for example|for instance|such as|including)[\s:,]+`)

// programListPattern matches a parenthesised list after a programme word:
// "стипендии (Chevening, Fulbright)".
var programListPattern = regexp.MustCompile(`(?i)(?:стипенди\p{L}*|программ\p{L}*|грант\p{L}*|университет\p{L}*|вуз\p{L}*|scholarships?|programs?|programmes?|universities|fellowships?|grants?)\s*\(([^()]{3,300})\)`)

var enumerationSplitPattern = regexp.MustCompile(`\s*(?:[,;]|\s(?:и|или|либо|а также|and|or|as well as)\s)\s*`)

var trailingHintPattern = regexp.MustCompile(`^(.*?)\s*\(([^()]{2,40})\)\s*$`)

// enumerationLeadWords are conjunctions that may start a list item.
var enumerationLeadWords = map[string]bool{"и": true, "или": true, "а": true, "также": true, "and": true, "or": true, "also": true}

// genericGoalWords are fields of study, degrees, tests, formats and other
// generic terms: a candidate made only of such words is no entity.
var genericGoalWords = map[string]bool{}

func init() {
	for _, word := range strings.Fields(`
		computer computing science sciences software engineering engineer data ai it ml cs ds se
		machine learning deep artificial intelligence information technology technologies
		cybersecurity cyber security robotics mathematics math maths physics chemistry biology
		economics business administration management finance medicine law design
		bachelor bachelors master masters mba phd doctorate doctoral undergraduate graduate
		postgraduate degree degrees bsc msc ba ma bs ms s
		gpa ielts toefl sat act gre gmat duolingo det pte cefr ap ib a-level
		stem need based need-based merit merit-based full ride fully funded tuition free
		online offline pdf url html api faq llm gpu cpu json csv docx xlsx vs ok
		usd eur kzt rub gbp research original task international students student
		visa essay essays sop cv interview
	`) {
		genericGoalWords[word] = true
	}
}

// genericGoalTerm reports whether every word of the name is generic.
func genericGoalTerm(name string) bool {
	words := strings.Fields(strings.ToLower(strings.NewReplacer("/", " ", "-", " ", "'", " ", "’", " ").Replace(name)))
	if len(words) == 0 {
		return true
	}
	for _, word := range words {
		if !genericGoalWords[latinFold.Replace(word)] {
			return false
		}
	}
	return true
}

// enumerationName cleans one list item into an entity, or reports that the
// list ended (the item is no name: it starts with a lower-case word).
func enumerationName(item string) (entity goalEntity, ok, end bool) {
	item = strings.TrimSpace(strings.Trim(item, " .:!?\"'«»“”„"))
	for {
		first, rest, found := strings.Cut(item, " ")
		if !found || !enumerationLeadWords[strings.ToLower(first)] {
			break
		}
		item = strings.TrimSpace(rest)
	}
	if item == "" {
		return goalEntity{}, false, false
	}
	if match := trailingHintPattern.FindStringSubmatch(item); match != nil {
		item = strings.TrimSpace(match[1])
		entity.Hint = parentheticalHint(match[2])
	}
	if item == "" || len(strings.Fields(item)) > 6 {
		return goalEntity{}, false, true
	}
	first, _ := utf8.DecodeRuneInString(item)
	if !unicode.IsUpper(first) && !unicode.IsDigit(first) {
		return goalEntity{}, false, true
	}
	entity.Name = strings.Join(strings.Fields(item), " ")
	return entity, !genericGoalTerm(entity.Name), false
}

// parentheticalHint keeps a short parenthesised word or two (a country or
// region), not lists, years or other notes.
func parentheticalHint(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, ",;:/0123456789") || len(strings.Fields(text)) > 2 {
		return ""
	}
	return text
}

// enumeratedGoalEntities returns the names the goal lists explicitly:
// after "например", "e.g.", "such as" (up to the end of the sentence) and
// in parentheses after a programme word.
func enumeratedGoalEntities(goal string) []goalEntity {
	type span struct{ start, end int }
	spans := []span{}
	for _, match := range enumerationMarkerPattern.FindAllStringIndex(goal, -1) {
		end := len(goal)
		if index := strings.IndexAny(goal[match[1]:], ".!?\n:"); index >= 0 {
			end = match[1] + index
		}
		spans = append(spans, span{match[1], end})
	}
	for _, match := range programListPattern.FindAllStringSubmatchIndex(goal, -1) {
		spans = append(spans, span{match[2], match[3]})
	}
	entities := []goalEntity{}
	for _, item := range spans {
		text := goal[item.start:item.end]
		// Keep parenthesised hints whole while splitting the list.
		depth, cleaned := 0, []rune{}
		for _, r := range text {
			switch r {
			case '(':
				depth++
			case ')':
				depth = max(0, depth-1)
			}
			if depth > 0 && (r == ',' || r == ';') {
				r = '\x00'
			}
			cleaned = append(cleaned, r)
		}
		for _, part := range enumerationSplitPattern.Split(string(cleaned), -1) {
			if strings.ContainsRune(part, '\x00') {
				part = strings.ReplaceAll(part, "\x00", ",")
			}
			entity, ok, end := enumerationName(part)
			if end {
				break
			}
			if ok {
				entities = append(entities, entity)
			}
		}
	}
	return entities
}

// extractGoalEntities returns the named entities of a research goal
// (programmes, universities, products): explicitly enumerated names first,
// then quoted names and proper names (see goalNameCandidates). Generic
// fields, degrees and tests are dropped, a parenthesised country after a
// name becomes its hint, and each entity is returned once, at most
// maxGoalEntities.
func extractGoalEntities(goal string) []goalEntity {
	entities := []goalEntity{}
	add := func(candidate goalEntity) {
		name := strings.Join(strings.Fields(strings.Trim(candidate.Name, " -&")), " ")
		if utf8.RuneCountInString(name) < 2 || len(entities) >= maxGoalEntities || genericGoalTerm(name) {
			return
		}
		folded := entityFold(name)
		if folded == "" {
			return
		}
		for index, known := range entities {
			knownFolded := entityFold(known.Name)
			if strings.Contains(" "+knownFolded+" ", " "+folded+" ") {
				if known.Hint == "" {
					entities[index].Hint = candidate.Hint
				}
				return
			}
			if strings.Contains(" "+folded+" ", " "+knownFolded+" ") {
				entities[index].Name = name
				if candidate.Hint != "" {
					entities[index].Hint = candidate.Hint
				}
				return
			}
		}
		entities = append(entities, goalEntity{Name: name, Hint: candidate.Hint})
	}
	for _, entity := range enumeratedGoalEntities(goal) {
		add(entity)
	}
	for _, name := range goalNameCandidates(goal) {
		add(goalEntity{Name: name, Hint: goalNameHint(goal, name)})
	}
	return entities
}

// goalNameHint is the parenthesised hint right after the name in the goal.
func goalNameHint(goal, name string) string {
	pattern, err := regexp.Compile(regexp.QuoteMeta(name) + `\s*\(([^()]{2,40})\)`)
	if err != nil {
		return ""
	}
	if match := pattern.FindStringSubmatch(goal); match != nil {
		return parentheticalHint(match[1])
	}
	return ""
}

// goalEntities returns the entity names of a goal (see extractGoalEntities).
func goalEntities(goal string) []string {
	names := []string{}
	for _, entity := range extractGoalEntities(goal) {
		names = append(names, entity.Name)
	}
	return names
}
