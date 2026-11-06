package orchestrator

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/tools"
)

const (
	maxSearchQueryWords = 12
	maxSearchQueryRunes = 160
)

// leadingSearchInstructions are imperative prefixes planners put in front of
// a facet ("Search for ..."). They carry no search signal and would be sent
// to the engine verbatim.
var leadingSearchInstructions = []string{
	"search the web for", "search online for", "search for", "look up", "look for", "find information about",
	"find information on", "find sources about", "find sources on", "find sources for", "find", "research",
	"identify", "investigate", "найти информацию о", "найти информацию об", "найди информацию о", "найти", "найди",
	"поиск информации о", "поиск", "поищи", "искать", "исследовать",
}

// conciseSearchQuery turns planner text into a short keyword query: it keeps
// the untrusted-text protections of tools.SafeSearchQuery, drops a leading
// search instruction and trailing punctuation, and keeps at most 12 words.
// It returns "" when nothing usable remains.
func conciseSearchQuery(text string) string {
	query := tools.SafeSearchQuery(strings.TrimSpace(text))
	if query == "" {
		return ""
	}
	lower := strings.ToLower(query)
	for _, prefix := range leadingSearchInstructions {
		if len(lower) == len(query) && strings.HasPrefix(lower, prefix+" ") {
			query = strings.TrimSpace(query[len(prefix):])
			break
		}
	}
	// Brackets and quotes only fragment keyword queries once words are cut.
	query = strings.Map(func(r rune) rune {
		if strings.ContainsRune("()[]{}\"«»“”", r) {
			return ' '
		}
		return r
	}, query)
	words := strings.Fields(query)
	if len(words) > maxSearchQueryWords {
		words = words[:maxSearchQueryWords]
	}
	query = strings.Join(words, " ")
	for utf8.RuneCountInString(query) > maxSearchQueryRunes {
		index := strings.LastIndex(query, " ")
		if index < 1 {
			query = string([]rune(query)[:maxSearchQueryRunes])
			break
		}
		query = query[:index]
	}
	query = strings.TrimRightFunc(query, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
	query = strings.TrimLeftFunc(query, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
	if !strings.ContainsFunc(query, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
		return ""
	}
	return query
}
