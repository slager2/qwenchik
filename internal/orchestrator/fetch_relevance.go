package orchestrator

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/provenance"
	"github.com/slager2/qwenchik/internal/providers/research"
)

// A search hit whose title and URL share no word with the goal's entities,
// topic or content words is off-topic: a live scholarship run fetched IEEE
// COMPSAC and FSE 2027 call-for-papers pages found by a "Computer Science"
// query. Such hits are moved behind every relevant candidate, so they are
// fetched only when nothing relevant is left.

// relevanceTopicWords are English words of pages about the topics a goal
// asks about (matched like entityAttributeHints stems).
var relevanceTopicWords = []struct {
	stems []string
	words []string
}{
	{[]string{"стипенди", "грант", "scholarship", "grant", "fellowship", "funding", "financial aid"}, []string{"scholarship", "grant", "fellowship", "stipend", "bursary", "burs", "funding", "funded", "financial", "award"}},
	{[]string{"поступ", "бакалавр", "магистр", "абитуриент", "admission", "apply", "applicant", "bachelor", "undergraduate", "master"}, []string{"admission", "apply", "application", "applicant", "eligibility", "requirement", "undergraduate", "bachelor", "master", "degree", "study"}},
	{[]string{"университет", "вуз", "колледж", "university", "college"}, []string{"university", "universities", "college", "institute", "study"}},
	{[]string{"дедлайн", "срок", "deadline", "calendar", "календар"}, []string{"deadline", "dates", "calendar", "timeline"}},
	{[]string{"стоимост", "оплат", "tuition", "fee", "cost"}, []string{"tuition", "fee", "cost"}},
	{[]string{"тариф", "price", "pricing"}, []string{"price", "pricing", "plan"}},
}

// goalRelevanceTerms returns folded word stems a relevant page title or URL
// shares with the goal: entity words and glued entity names, entity hints,
// topic words and the goal's own content words (generic fields, stop
// words, numbers and short words excluded).
func goalRelevanceTerms(goal string) []string {
	terms := []string{}
	add := func(term string) {
		term = strings.TrimSpace(term)
		if utf8.RuneCountInString(term) >= 3 && !contains(terms, term) {
			terms = append(terms, term)
		}
	}
	for _, entity := range extractGoalEntities(goal) {
		folded := entityFold(entity.Name)
		for _, word := range strings.Fields(folded) {
			if !genericGoalWords[word] {
				add(word)
			}
		}
		if glued := strings.ReplaceAll(folded, " ", ""); utf8.RuneCountInString(glued) >= 4 {
			add(glued)
		}
		add(entityFold(entityHintKeyword(entity.Hint)))
	}
	lower := strings.ToLower(goal)
	for _, topic := range relevanceTopicWords {
		if containsAnyText(lower, topic.stems...) {
			for _, word := range topic.words {
				add(word)
			}
		}
	}
	for _, word := range strings.Fields(entityFold(userURLPattern.ReplaceAllString(goal, " "))) {
		if utf8.RuneCountInString(word) < 4 || genericGoalWords[word] || isGoalStopWord(word) || strings.ContainsFunc(word, unicode.IsDigit) {
			continue
		}
		if runes := []rune(word); len(runes) > 5 {
			word = string(runes[:5])
		}
		add(word)
	}
	return terms
}

// fetchCandidateRelevant reports whether the page title or URL shares a
// term with the goal: a title/URL word starts with a term, or a term of 5+
// letters (a glued programme name) occurs inside the host or path.
func fetchCandidateRelevant(rawURL, title string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	text := title + " " + rawURL
	if parsed, err := url.Parse(rawURL); err == nil {
		text = title + " " + parsed.Hostname() + " " + parsed.Path
	}
	folded := entityFold(text)
	words := strings.Fields(folded)
	compact := strings.ReplaceAll(folded, " ", "")
	for _, term := range terms {
		if utf8.RuneCountInString(term) >= 5 && strings.Contains(compact, term) {
			return true
		}
		for _, word := range words {
			if strings.HasPrefix(word, term) {
				return true
			}
		}
	}
	return false
}

// searchHitTitles maps canonical URLs of recorded search hits to their
// titles.
func searchHitTitles(results map[string]any) map[string]string {
	titles := map[string]string{}
	add := func(rawURL, title string) {
		if canonical, err := provenance.CanonicalURL(rawURL); err == nil && title != "" {
			titles[canonical] = strings.TrimSpace(titles[canonical] + " " + title)
		}
	}
	for _, value := range results {
		switch hits := value.(type) {
		case []research.SearchHit:
			for _, hit := range hits {
				add(hit.URL, hit.Title)
			}
		case []any:
			for _, item := range hits {
				if hit, ok := item.(map[string]any); ok {
					rawURL, _ := hit["url"].(string)
					title, _ := hit["title"].(string)
					add(rawURL, title)
				}
			}
		}
	}
	return titles
}

// officialHost reports a government/education host, which is never gated.
func officialHost(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && officialHostPattern.MatchString(strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www."))
}
