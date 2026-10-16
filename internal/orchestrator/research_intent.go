package orchestrator

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var quotedResearchTermPattern = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|“[^”]*”|‘[^’]*’|«[^»]*»`)

func requiresWebResearch(value string) bool {
	value = strings.ToLower(quotedResearchTermPattern.ReplaceAllString(value, " "))
	if hasUserURL(value) && containsAny(value, "compare", "versus", " vs ", "сравни", "сопостав") {
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
	return isLocationBoundQuestion(value)
}

func minimumResearchFetches(value string) int {
	value = strings.ToLower(quotedResearchTermPattern.ReplaceAllString(value, " "))
	if hasUserURL(value) {
		if requiresWebResearch(value) {
			return 3
		}
		return 1
	}
	if requiresWebResearch(value) {
		return 3
	}
	return 0
}

func minimumResearchSearches(value string) int {
	value = strings.ToLower(quotedResearchTermPattern.ReplaceAllString(value, " "))
	if !requiresWebResearch(value) {
		return 0
	}
	return 3
}

func isLocationBoundQuestion(value string) bool {
	if !containsAny(value, "where", "где") {
		return false
	}
	return containsAny(value, " in ", " within ", " near ", " around ", " at ", " в ", " около ", " рядом с ")
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
			return []string{"fetch", "search", "search", "search", "fetch", "fetch"}
		}
		return []string{"fetch"}
	}
	if !requiresWebResearch(request) {
		return nil
	}
	return []string{"search", "search", "search", "fetch", "fetch", "fetch"}
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
