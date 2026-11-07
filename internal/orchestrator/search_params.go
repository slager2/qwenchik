package orchestrator

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/provenance"
)

// researchNow is the clock of date-sensitive research decisions (search
// recency, freshness of evidence); tests replace it.
var researchNow = func() time.Time { return time.Now().UTC() }

var yearPattern = regexp.MustCompile(`\b(20[0-9]{2})\b`)

// dateSensitiveStems mark questions about current, dated facts: a word
// starting with a stem matches; dateSensitivePhrases match word sequences.
var (
	dateSensitiveStems   = []string{"deadline", "calendar", "schedule", "дедлайн", "срок", "календар", "актуальн", "текущ"}
	dateSensitiveWords   = []string{"due", "intake", "timeline", "latest", "current", "upcoming", "дата", "даты", "датах"}
	dateSensitivePhrases = []string{"application period", "this year", "next year", "приём заявок", "прием заявок", "в этом году", "в следующем году"}
)

// yearsIn returns the four-digit years 2000-2099 named in text.
func yearsIn(text string) []int {
	years := []int{}
	for _, match := range yearPattern.FindAllStringSubmatch(text, -1) {
		if year, err := strconv.Atoi(match[1]); err == nil {
			years = append(years, year)
		}
	}
	return years
}

// dateSensitiveText reports whether text asks for current dated facts: it
// names this or a later year, or a deadline/date term.
func dateSensitiveText(text string, now time.Time) bool {
	for _, year := range yearsIn(text) {
		if year >= now.Year() {
			return true
		}
	}
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for _, word := range words {
		if contains(dateSensitiveWords, word) {
			return true
		}
		for _, stem := range dateSensitiveStems {
			if strings.HasPrefix(word, stem) {
				return true
			}
		}
	}
	joined := " " + strings.Join(words, " ") + " "
	for _, phrase := range dateSensitivePhrases {
		if strings.Contains(joined, " "+phrase+" ") {
			return true
		}
	}
	return false
}

// searchLanguage is the agentSearcher language (region) of a query: ru for
// Cyrillic queries, en otherwise.
func searchLanguage(query string) string {
	if strings.ContainsFunc(query, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) {
		return "ru"
	}
	return "en"
}

// maxPreferredDomains bounds preferred_domains of a follow-up search.
const maxPreferredDomains = 8

// officialResultDomains returns the hosts of likely official sources among
// the recorded search hits (government/education/programme-named domains,
// officialSourceScore >= 1), best score first, then by first appearance.
func officialResultDomains(results map[string]any, limit int) []string {
	keys := make([]string, 0, len(results))
	for key := range results {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	type candidate struct {
		host         string
		score, order int
	}
	byHost := map[string]*candidate{}
	order := 0
	for _, key := range keys {
		for _, rawURL := range searchResultURLsForValue(results[key]) {
			query := ""
			if hits := searchResultHitsForURL(results[key], canonicalOrRaw(rawURL)); len(hits) > 0 {
				query = hits[0][0]
			}
			score := officialSourceScore(rawURL, query)
			if score < 1 {
				continue
			}
			parsed, err := url.Parse(rawURL)
			if err != nil || parsed.Hostname() == "" {
				continue
			}
			host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
			if existing := byHost[host]; existing != nil {
				existing.score = max(existing.score, score)
				continue
			}
			byHost[host] = &candidate{host: host, score: score, order: order}
			order++
		}
	}
	ranked := make([]*candidate, 0, len(byHost))
	for _, item := range byHost {
		ranked = append(ranked, item)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].order < ranked[j].order
	})
	domains := []string{}
	for _, item := range ranked {
		if len(domains) == limit {
			break
		}
		domains = append(domains, item.host)
	}
	return domains
}

// followUpSearchStep reports whether a search step belongs to a later
// research round (continuation or exploration).
func followUpSearchStep(step domain.PlanStep) bool {
	return strings.HasPrefix(step.ID, "continue_")
}

// researchSearchArguments are the backend-built arguments of a search step:
// language from the query script, recency "year" for date-sensitive facets,
// and as preferred (not restricted) domains the known official sites of
// programmes the query names plus, in follow-up rounds, the official
// domains seen so far.
func researchSearchArguments(step domain.PlanStep, query string, state checkpointState, now time.Time) map[string]any {
	arguments := map[string]any{"query": query, "max_results": researchSearchResults, "language": searchLanguage(query)}
	if dateSensitiveText(query, now) {
		arguments["recency"] = "year"
	}
	// Known official sites of programmes the query names lead.
	domains := officialEntityDomains(query)
	if followUpSearchStep(step) {
		for _, host := range officialResultDomains(state.ToolResults, maxPreferredDomains) {
			if !contains(domains, host) {
				domains = append(domains, host)
			}
		}
	}
	if len(domains) > maxPreferredDomains {
		domains = domains[:maxPreferredDomains]
	}
	if len(domains) > 0 {
		arguments["preferred_domains"] = domains
	}
	return arguments
}

func canonicalOrRaw(rawURL string) string {
	if canonical, err := provenance.CanonicalURL(rawURL); err == nil {
		return canonical
	}
	return rawURL
}
