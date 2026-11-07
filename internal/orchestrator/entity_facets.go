package orchestrator

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

var quotedNamePattern = regexp.MustCompile(`[«"“„]([^«»"“”„\n]{2,80})[»"”“]`)

// entityConnectors may join capitalized words inside one name ("University
// of Tokyo"); "and" and "the" separate listed names instead.
var entityConnectors = map[string]bool{"of": true, "for": true, "de": true, "der": true, "la": true, "le": true, "du": true, "di": true}

// entityNoise are Latin tokens of Russian requests that name formats or
// generic terms, not entities.
var entityNoise = map[string]bool{"pdf": true, "url": true, "html": true, "api": true, "faq": true, "ai": true, "it": true, "ok": true, "llm": true, "gpu": true, "cpu": true, "json": true, "csv": true, "docx": true, "online": true, "vs": true}

// entityLeadingVerbs are request verbs that start an English sentence and
// would otherwise glue onto the following capitalized name.
var entityLeadingVerbs = map[string]bool{"compare": true, "describe": true, "summarize": true, "summarise": true, "review": true, "analyze": true, "analyse": true, "research": true, "investigate": true, "identify": true, "check": true, "collect": true, "gather": true, "evaluate": true, "rank": true}

type goalToken struct {
	text          string
	sentenceStart bool
}

func goalTokens(goal string) []goalToken {
	goal = userURLPattern.ReplaceAllString(goal, " ")
	tokens := []goalToken{}
	sentenceStart := true
	var current strings.Builder
	flush := func(next bool) {
		if current.Len() > 0 {
			tokens = append(tokens, goalToken{text: strings.Trim(current.String(), "-'’"), sentenceStart: sentenceStart})
			current.Reset()
			sentenceStart = false
		}
		if next {
			sentenceStart = true
		}
	}
	for _, r := range goal {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("-'’&", r):
			current.WriteRune(r)
		case strings.ContainsRune(".!?\n:;", r):
			flush(true)
		case strings.ContainsRune(",()[]{}«»\"“”„/", r):
			flush(false)
			// A list or bracket boundary ends a name.
			tokens = append(tokens, goalToken{text: ""})
		default:
			flush(false)
		}
	}
	flush(false)
	return tokens
}

func latinToken(token string) bool {
	letters := 0
	for _, r := range token {
		if unicode.IsLetter(r) {
			if !unicode.Is(unicode.Latin, r) {
				return false
			}
			letters++
		}
	}
	return letters > 0
}

func capitalizedToken(token string) bool {
	first, _ := utf8.DecodeRuneInString(token)
	return unicode.IsUpper(first)
}

func acronymToken(token string) bool {
	upper := 0
	for _, r := range token {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			upper++
		}
	}
	return upper >= 2
}

// goalNameCandidates returns the proper names of a research goal: quoted
// names, runs of Latin words in a non-Latin (for example Russian) request,
// and in a Latin request runs of two or more capitalized words or
// acronyms. Generic terms are skipped; each name is returned once, in order
// of appearance.
func goalNameCandidates(goal string) []string {
	entities := []string{}
	add := func(name string) {
		name = strings.Join(strings.Fields(strings.Trim(name, " -&")), " ")
		if utf8.RuneCountInString(name) < 3 || len(entities) >= 2*maxGoalEntities || entityNoise[strings.ToLower(name)] || genericGoalTerm(name) {
			return
		}
		folded := entityFold(name)
		for index, known := range entities {
			knownFolded := entityFold(known)
			if strings.Contains(" "+knownFolded+" ", " "+folded+" ") {
				return
			}
			if strings.Contains(" "+folded+" ", " "+knownFolded+" ") {
				entities[index] = name
				return
			}
		}
		entities = append(entities, name)
	}
	for _, match := range quotedNamePattern.FindAllStringSubmatch(goal, -1) {
		if strings.ContainsFunc(match[1], unicode.IsLetter) && len(strings.Fields(match[1])) <= 8 {
			add(match[1])
		}
	}
	nonLatin := nonLatinScriptGoal(goal)
	tokens := goalTokens(goal)
	for i := 0; i < len(tokens); {
		if tokens[i].text == "" || !latinToken(tokens[i].text) {
			i++
			continue
		}
		// Collect a run of Latin tokens (connectors allowed inside).
		j := i
		for j < len(tokens) && tokens[j].text != "" && latinToken(tokens[j].text) && (j == i || !tokens[j].sentenceStart) {
			j++
		}
		run := tokens[i:j]
		i = j
		if nonLatin {
			// In a Russian request every Latin run is a name or a term;
			// keep runs with a capitalized word or acronym.
			named := false
			for _, token := range run {
				named = named || (capitalizedToken(token.text) || acronymToken(token.text)) && !entityNoise[strings.ToLower(token.text)]
			}
			if named {
				words := []string{}
				for _, token := range run {
					if !entityNoise[strings.ToLower(token.text)] {
						words = append(words, token.text)
					}
				}
				add(strings.Join(words, " "))
			}
			continue
		}
		// Latin request: split the run into capitalized names.
		var name []string
		flushName := func() {
			for len(name) > 0 && entityConnectors[strings.ToLower(name[len(name)-1])] {
				name = name[:len(name)-1]
			}
			// "Computer Science for Kazakh": a generic field before a
			// connector is no part of the name.
			for index, word := range name {
				if index > 0 && entityConnectors[strings.ToLower(word)] && genericGoalTerm(strings.Join(name[:index], " ")) {
					name = name[index+1:]
					break
				}
			}
			if len(name) >= 2 || len(name) == 1 && acronymToken(name[0]) {
				add(strings.Join(name, " "))
			}
			name = nil
		}
		for index, token := range run {
			lower := strings.ToLower(token.text)
			switch {
			case index == 0 && token.sentenceStart && (entityLeadingVerbs[lower] || isGoalStopWord(lower)):
				// "Compare Seoul National University": the verb is no name.
			case capitalizedToken(token.text) || acronymToken(token.text):
				name = append(name, token.text)
			case len(name) > 0 && entityConnectors[lower]:
				name = append(name, token.text)
			default:
				flushName()
			}
		}
		flushName()
	}
	return entities
}

func isGoalStopWord(word string) bool {
	_, stop := goalKeywordStopWords[word]
	return stop
}

// entityFold lower-cases a name, folds Latin diacritics and keeps letters
// and digits only, so "Türkiye Bursları" matches "turkiye burslari".
func entityFold(text string) string {
	return latinFold.Replace(looseQuoteText(text))
}

// planCoversEntity reports whether a search step already searches for the
// entity: every entity word (2+ characters) occurs in its query or facet.
func planCoversEntity(plan domain.Plan, entity string) bool {
	words := strings.Fields(entityFold(entity))
	for _, step := range plan.Subtasks {
		if step.Kind != "search" {
			continue
		}
		text := " " + entityFold(step.Query+" "+searchStepFacet(step.Description)) + " "
		covered := len(words) > 0
		for _, word := range words {
			covered = covered && strings.Contains(text, " "+word)
		}
		if covered {
			return true
		}
	}
	return false
}

// entityAttributeHints are English keyword hints for what the request asks
// about an entity; official sites of Latin-named entities are mostly in
// English or their own language.
var entityAttributeHints = []struct {
	stems []string
	hint  string
}{
	{[]string{"deadline", "дедлайн", "срок", "calendar", "календар", "dates", "дат"}, "deadline"},
	{[]string{"requirement", "eligib", "требован", "услови", "критери", "admission", "поступлен"}, "requirements"},
	{[]string{"tuition", "fee", "cost", "стоимост", "цен", "оплат", "плат"}, "tuition fees"},
	{[]string{"scholarship", "grant", "стипенди", "грант"}, "scholarship"},
	{[]string{"price", "pricing", "тариф"}, "pricing"},
}

func entityAttributeHint(goal string) string {
	words := strings.FieldsFunc(strings.ToLower(goal), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	hints := []string{}
	for _, item := range entityAttributeHints {
		matched := false
		for _, word := range words {
			for _, stem := range item.stems {
				matched = matched || strings.HasPrefix(word, stem)
			}
		}
		if matched && len(hints) < 2 {
			hints = append(hints, item.hint)
		}
	}
	if len(hints) == 0 {
		return "official"
	}
	return strings.Join(hints, " ")
}

// researchYear is the year a date-sensitive request is about: the latest
// current-or-future year it names, else the current year. It is 0 when the
// request is not date-sensitive.
func researchYear(goal string, now time.Time) int {
	if !dateSensitiveText(goal, now) {
		return 0
	}
	year := now.Year()
	for _, named := range yearsIn(goal) {
		if named > year && named <= now.Year()+2 {
			year = named
		}
	}
	return year
}

// entitySearchQuery is the keyword facet of one named entity:
// "<entity> <attribute hint> <year>".
func entitySearchQuery(entity, goal string, now time.Time) string {
	query := entity + " " + entityAttributeHint(goal)
	if year := researchYear(goal, now); year > 0 && !strings.Contains(entity, strconv.Itoa(year)) {
		query += " " + strconv.Itoa(year)
	}
	return conciseSearchQuery(query)
}

// entitySearchLimit is the most search facets an initial plan may hold
// after entity facets are added: the backend facet bound, half the
// session's search query budget (so later rounds keep queries) and the
// worker's search limit.
func entitySearchLimit(ctx context.Context, workerLimit int) int {
	limit := maxBackendSearchFacets
	if session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); ok && session.Budget.MaxSearchQueries > 0 {
		limit = min(limit, max(1, session.Budget.MaxSearchQueries/2))
	}
	if workerLimit > 0 {
		limit = min(limit, workerLimit)
	}
	return limit
}

// ensureEntitySearchFacets adds one backend search facet per named entity
// of the goal that no planned search covers, so a request naming several
// programmes or universities searches each of them. Facets are inserted
// after the last search and every discovered-source fetch depends on them.
// searchLimit bounds the total search count; one slot stays free for the
// user-language facet of a non-Latin request whose planned queries are all
// in another script.
func ensureEntitySearchFacets(plan domain.Plan, goal string, searchLimit, capacity int, now time.Time) domain.Plan {
	entities := goalEntities(goal)
	if len(entities) == 0 {
		return plan
	}
	searches, lastSearch := 0, -1
	used := map[string]bool{}
	queries := []string{}
	userLanguageQuery := false
	for index, step := range plan.Subtasks {
		used[step.ID] = true
		if step.Kind == "search" {
			searches++
			lastSearch = index
			query := step.Query
			if query == "" {
				query = canonicalStepSearchQuery(step)
			}
			queries = append(queries, query)
			userLanguageQuery = userLanguageQuery || nonLatinQuery(query)
		}
	}
	if lastSearch < 0 {
		return plan
	}
	limit := searchLimit
	if nonLatinScriptGoal(goal) && !userLanguageQuery {
		limit--
	}
	added := []domain.PlanStep{}
	for _, entity := range entities {
		if searches+len(added) >= limit || len(plan.Subtasks)+len(added) >= capacity {
			break
		}
		if planCoversEntity(plan, entity) {
			continue
		}
		query := entitySearchQuery(entity, goal, now)
		if query == "" || duplicateResearchQuery(query, queries) {
			continue
		}
		id := ""
		for n := 1; id == ""; n++ {
			if candidate := fmt.Sprintf("search_%d", n); !used[candidate] {
				id = candidate
			}
		}
		used[id] = true
		queries = append(queries, query)
		facet := "Official sources for the named entity " + entity + ": " + query
		description := deterministicSearchFacetPrefix + facet + ". User request: " + goal
		if utf8.RuneCountInString(description) > 1000 {
			description = facet
		}
		added = append(added, domain.PlanStep{
			ID: id, Kind: "search", Description: description, Query: query,
			Dependencies: []string{}, ExpectedOutput: "Discovery leads for one named entity",
			SuccessCriteria: []string{"The discovery query was executed and its results were recorded; search snippets are leads, not verified facts."},
		})
	}
	if len(added) == 0 {
		return plan
	}
	subtasks := make([]domain.PlanStep, 0, len(plan.Subtasks)+len(added))
	subtasks = append(subtasks, plan.Subtasks[:lastSearch+1]...)
	subtasks = append(subtasks, added...)
	subtasks = append(subtasks, plan.Subtasks[lastSearch+1:]...)
	userURLFetch := hasUserURL(goal)
	for index := range subtasks {
		step := &subtasks[index]
		if step.Kind != "fetch" || index == 0 && userURLFetch {
			continue
		}
		step.Dependencies = append([]string{}, step.Dependencies...)
		for _, search := range added {
			step.Dependencies = append(step.Dependencies, search.ID)
		}
	}
	plan.Subtasks = subtasks
	plan.MaxSteps = len(subtasks)
	return plan
}
