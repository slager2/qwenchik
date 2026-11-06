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

// goalKeywordStopWords are function words and conversational request words
// that carry no search signal in a user's research question.
var goalKeywordStopWords = map[string]struct{}{}

func init() {
	for _, word := range strings.Fields(`a an the and or of for to in on with about what which how is are be i me my we our you your please can could would should
		tell explain give make create write find search show list do does did need want help like just some any it its this that these those from by as at into
		so also then than more most there here who whom why when where will shall may might must have has had was were been being not no if
		original task user update
		и в во на с со по для о об от до из у к ко а но или ли же бы не ни как что это эти этот эта то так мне меня мой моя мои ты вы вас ваш ваша
		пожалуйста можешь можете помоги помогите расскажи расскажите объясни объясните составь составьте сделай сделайте напиши напишите найди найдите
		подскажи подскажите дай дайте покажи покажите хочу хотел хотела нужно надо нужен нужна нужны какой какая какие каких какое каком который которая которые
		где когда почему зачем сколько чтобы если есть быть будет был была были очень ещё еще уже тоже также только все всё весь вся её ее его их им нам нас
		мы я он она они при над под через между про чем чём кто куда откуда`) {
		goalKeywordStopWords[word] = struct{}{}
	}
}

// goalKeywordQuery builds a concise keyword query in the language of the
// user's own request: content words of the goal without function words,
// request phrasing, URLs or composeGoal wrappers, at most maxWords words.
// It returns "" when the goal has no usable keywords.
func goalKeywordQuery(goal string, maxWords int) string {
	goal = userURLPattern.ReplaceAllString(goal, " ")
	words := []string{}
	seen := map[string]bool{}
	for _, token := range strings.FieldsFunc(goal, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !strings.ContainsRune("-+#", r)
	}) {
		token = strings.Trim(token, "-")
		lower := strings.ToLower(token)
		if _, stop := goalKeywordStopWords[lower]; stop || seen[lower] || utf8.RuneCountInString(token) < 2 && !unicode.IsNumber([]rune(token + " ")[0]) {
			continue
		}
		seen[lower] = true
		words = append(words, token)
		if len(words) == maxWords {
			break
		}
	}
	return conciseSearchQuery(strings.Join(words, " "))
}
