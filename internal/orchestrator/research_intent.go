package orchestrator

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	maxResearchSearchFacets = 3
	maxResearchFetches      = 4
)

var quotedResearchTermPattern = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|“[^”]*”|‘[^’]*’|«[^»]*»`)
var selfContainedTextOperationPattern = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:count|how\s+many|summari[sz]e|translate|extract|find|identify|analy[sz]e|посчитай|сосчитай|подсчитай|сколько|суммируй|переведи|извлеки|найди|определи|проанализируй)[^\n]{0,160}(?:text|paragraph|sentence|document|текст|абзац|предложени\p{L}*|документ)`)
var suppliedTextReferencePattern = regexp.MustCompile(`(?i)(?:this|these|following|provided|supplied|below|этот|эта|это|эти|переданн\p{L}*|предоставленн\p{L}*|следующ\p{L}*|ниже)`)
var informationalNounPattern = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:news|changelog|change\s+log|release(?:s)?|release\s+notes|documentation|docs|updates?|prices?|hours|address|addresses|reviews?|weather|schedule|opening\s+hours|новост\p{L}*|новинк\p{L}*|хронолог\p{L}*|список\s+изменен\p{L}*|изменен\p{L}*|обновлен\p{L}*|документац\p{L}*|цен\p{L}*|расписан\p{L}*|адрес\p{L}*|отзыв\p{L}*|погод\p{L}*)(?:$|[^\p{L}\p{N}])`)
var locationEntityPattern = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:libraries?|restaurants?|cafes?|coffee\s+shops?|hotels?|hostels?|museums?|parks?|hospitals?|clinics?|pharmacies?|schools?|universities?|bookstores?|shops?|stores?|business(?:es)?|services?|attractions?|events?|concerts?|airports?|banks?|post\s+offices?|библиотек\p{L}*|ресторан\p{L}*|кафе|кофейн\p{L}*|отел\p{L}*|гостиниц\p{L}*|музе\p{L}*|парк\p{L}*|больниц\p{L}*|клиник\p{L}*|аптек\p{L}*|школ\p{L}*|университет\p{L}*|книжн\p{L}*\s+магазин\p{L}*|магазин\p{L}*|компани\p{L}*|сервис\p{L}*|достопримечательност\p{L}*|событи\p{L}*|концерт\p{L}*|аэропорт\p{L}*|банк\p{L}*)(?:$|[^\p{L}\p{N}])`)
var locationPrepositionPattern = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(?i:in|at|near|within|в|на|около|рядом\s+с|возле)\s+(?:(?i:city\s+of|городе|стране)\s+)?\p{Lu}[\p{L}.-]{1,}(?:$|[^\p{L}\p{N}])`)
var whoIsEntityPattern = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:who\s+(?:is|are)|what\s+(?:is|are)|кто\s+(?:такой|такая|такое|такие)|что\s+такое)(?:$|[^\p{L}\p{N}])`)
var interrogativePattern = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:who|what|where|when|which|why|how|кто|что|где|когда|какой|какая|какое|какие|сколько|почему|зачем|как|ли)(?:$|[^\p{L}\p{N}])`)

func requiresWebResearch(value string) bool {
	value = quotedResearchTermPattern.ReplaceAllString(value, " ")
	hasURL := hasUserURL(value)
	value = userURLPattern.ReplaceAllString(value, " ")
	if locationPrepositionPattern.MatchString(value) {
		return true
	}
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "user update:", " ")
	if isSelfContainedTextOperation(value) {
		return false
	}
	if hasURL && containsAny(value, "compare", "versus", " vs ", "сравни", "сопостав") {
		return true
	}
	for _, marker := range []string{
		"research", "investigate", "look up", "find out", "search for", "find current", "latest information", "latest public information", "current information", "recent information", "primary source", "verify the latest", "compare current",
		"исслед", "поищи", "поискать в интернет", "найди в интернете", "найти в интернете", "узнай актуальн", "актуальн", "последние сведения", "первичн источник", "проверь актуальн",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	if whoIsEntityPattern.MatchString(value) || interrogativePattern.MatchString(value) || informationalNounPattern.MatchString(value) || locationEntityPattern.MatchString(value) {
		return true
	}
	return isLocationBoundQuestion(value)
}

func isSelfContainedTextOperation(value string) bool {
	if containsAny(value, "summarize the following text", "summarise the following text", "summarize the text provided below", "summarise the text provided below", "посчитай слова в тексте ниже", "подсчитай слова в тексте ниже", "сосчитай слова в тексте ниже") {
		return true
	}
	return selfContainedTextOperationPattern.MatchString(value) && suppliedTextReferencePattern.MatchString(value)
}

func minimumResearchFetches(value string) int {
	value = strings.ToLower(quotedResearchTermPattern.ReplaceAllString(value, " "))
	if hasUserURL(value) {
		if requiresWebResearch(value) {
			return 2
		}
		return 1
	}
	if requiresWebResearch(value) {
		return 2
	}
	return 0
}

func minimumResearchSearches(value string) int {
	value = strings.ToLower(quotedResearchTermPattern.ReplaceAllString(value, " "))
	if !requiresWebResearch(value) {
		return 0
	}
	return 2
}

func isLocationBoundQuestion(value string) bool {
	if containsAny(value, "where", "где") && containsAny(value, " in ", " within ", " near ", " around ", " at ", " в ", " около ", " рядом с ", " возле ", " недалеко от ") {
		return true
	}
	return locationEntityPattern.MatchString(value)
}

func normalizeResearchText(value string) string {
	words := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	sort.Strings(words)
	return strings.Join(words, " ")
}

func duplicateResearchQuery(query string, previous []string) bool {
	normalized := normalizeResearchText(query)
	if normalized == "" {
		return false
	}
	for _, prior := range previous {
		if normalized == normalizeResearchText(prior) {
			return true
		}
	}
	return false
}

func requiredPlanSteps(taskTitle, goal string) int {
	return len(requiredResearchKinds(taskTitle, goal))
}

func requiredResearchKinds(taskTitle, goal string) []string {
	request := taskTitle + "\n" + goal
	if hasUserURL(goal) {
		if requiresWebResearch(request) {
			return []string{"fetch", "search", "search", "fetch"}
		}
		return []string{"fetch"}
	}
	if !requiresWebResearch(request) {
		return nil
	}
	return []string{"search", "search", "fetch", "fetch"}
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
