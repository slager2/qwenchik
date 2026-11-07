package orchestrator

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

// maxFreshnessContinuations bounds how many continuation rounds the
// freshness and official-coverage rule may request in one run.
const maxFreshnessContinuations = 3

// freshnessCoverageGaps checks evidence of a date-sensitive request before
// the run may stop: the newest year named by the accepted evidence must not
// be older than the previous year, and every named entity of the goal needs
// at least one official source (government/education/programme domain)
// that mentions it. It returns the gaps and keyword follow-up queries
// ("<entity> <year> official") that can close them. accepted limits the
// freshness check to evidence the verifier cited; empty means all evidence.
func freshnessCoverageGaps(goal string, evidence []domain.Evidence, accepted map[string]bool, now time.Time) (gaps, queries []string) {
	year := researchYear(goal, now)
	if year == 0 {
		return nil, nil
	}
	usable := []domain.Evidence{}
	for _, item := range evidence {
		if item.HTTPStatus >= 200 && item.HTTPStatus < 300 && len(item.QualityFlags) == 0 && strings.TrimSpace(item.Excerpt) != "" {
			usable = append(usable, item)
		}
	}
	// Only real named entities (programmes, universities, products) need an
	// official source; generic fields such as Computer Science do not.
	entities := extractGoalEntities(goal)
	addQuery := func(subject string) {
		query := conciseSearchQuery(subject + " " + strconv.Itoa(year) + " official")
		if query != "" && !contains(queries, query) {
			queries = append(queries, query)
		}
	}
	newest := 0
	for _, item := range usable {
		if len(accepted) > 0 && !accepted[item.ID] {
			continue
		}
		for _, named := range yearsIn(item.Title + " " + item.Excerpt) {
			if named <= now.Year()+2 {
				newest = max(newest, named)
			}
		}
	}
	if newest > 0 && newest < now.Year()-1 {
		gaps = append(gaps, fmt.Sprintf("The accepted evidence names no year after %d; current (%d) official dates and deadlines are still missing.", newest, year))
		if len(entities) == 0 {
			addQuery(goalKeywordQuery(goal, 6))
		}
		for _, entity := range entities {
			addQuery(entityQueryStem(entity, goal))
		}
	}
	for _, goalEntity := range entities {
		entity := goalEntity.Name
		words := strings.Fields(entityFold(entity))
		known := officialEntityDomains(entity)
		official := false
		for _, item := range usable {
			if knownOfficialHost(item.CanonicalURL, known) {
				official = true
				break
			}
			if officialSourceScore(item.CanonicalURL, entity) < 1 {
				continue
			}
			text := " " + entityFold(item.Title+" "+item.Excerpt+" "+item.CanonicalURL) + " "
			mentions := strings.Contains(text, strings.ReplaceAll(entityFold(entity), " ", "")) // programme-named host
			if !mentions {
				mentions = len(words) > 0
				for _, word := range words {
					mentions = mentions && strings.Contains(text, " "+word)
				}
			}
			official = official || mentions
		}
		if !official {
			gaps = append(gaps, truncateRunes("No official source has been fetched yet for "+entity+".", 240))
			addQuery(entityQueryStem(goalEntity, goal))
		}
	}
	return gaps, queries
}

// knownOfficialHost reports whether the URL is on one of the domains or
// their subdomains.
func knownOfficialHost(rawURL string, domains []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	for _, suffix := range domains {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// freshnessRuleApplies reports whether the freshness and official-coverage
// rule may hold back this round's verdict: only profiles that explore
// several rounds (MinRounds > 0: deep, long and overnight), only while
// another round is allowed, and at most maxFreshnessContinuations times.
// Quick and standard runs keep their verdicts.
func freshnessRuleApplies(ctx context.Context, state checkpointState) (holdVerdict, ok bool) {
	session, found := ctx.Value(researchExecutionKey{}).(domain.ResearchSession)
	if !found || !session.Policy.AdaptiveEnabled || session.Policy.Version != 2 || session.Budget.MinRounds <= 0 {
		return false, false
	}
	round := state.Completion.Continuations + 1
	if round >= session.Budget.MaxRounds || state.Completion.Continuations >= maxFreshnessContinuations {
		return false, false
	}
	// Rounds before MinRounds explore anyway; there the rule only steers
	// the follow-up searches.
	return !domain.Exploring(session, round), true
}
