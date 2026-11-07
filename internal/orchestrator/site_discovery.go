package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/policy"
	"github.com/slager2/qwenchik/internal/provenance"
)

const (
	// maxSiteDiscoveriesPerRound is how many official sites one later round
	// maps; maxSiteDiscoveries bounds a whole run.
	maxSiteDiscoveriesPerRound = 2
	maxSiteDiscoveries         = 6
	// siteDiscoveryKeyPrefix keys map_site results in the checkpoint tool
	// results; fetch candidates from them rank before search hits.
	siteDiscoveryKeyPrefix = "discover_"
	// maxDiscoveredPerFetch is how many discovered URLs lead the fetch
	// candidate list of one fetch step.
	maxDiscoveredPerFetch = 3
)

// officialResultSites returns the origins (scheme://host) of likely official
// sources among the recorded search hits, ranked like officialResultDomains.
func officialResultSites(results map[string]any, limit int) []string {
	keys := make([]string, 0, len(results))
	for key := range results {
		if !strings.HasPrefix(key, siteDiscoveryKeyPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	origins := map[string]string{}
	for _, key := range keys {
		for _, rawURL := range searchResultURLsForValue(results[key]) {
			parsed, err := url.Parse(rawURL)
			if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
				continue
			}
			host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
			if _, known := origins[host]; !known {
				origins[host] = parsed.Scheme + "://" + parsed.Host
			}
		}
	}
	sites := []string{}
	for _, host := range officialResultDomains(results, limit) {
		if origin := origins[host]; origin != "" {
			sites = append(sites, origin)
		}
	}
	return sites
}

func siteHost(origin string) string {
	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
}

// siteDiscoveryInstructions are the /map relevance instructions for one
// site: the entity named after the site (if any), English attribute hints of
// the open criteria, the research year and official document words.
func siteDiscoveryInstructions(goal, origin string, open []string, now time.Time) string {
	parts := []string{}
	compactHost := strings.ReplaceAll(strings.ReplaceAll(siteHost(origin), "-", ""), ".", "")
	for _, entity := range goalEntities(goal) {
		if glued := strings.ReplaceAll(entityFold(entity), " ", ""); len(glued) >= 3 && strings.Contains(compactHost, glued) {
			parts = append(parts, entity)
		}
	}
	criteria := goal + "\n" + strings.Join(open, "\n")
	parts = append(parts, entityAttributeHint(criteria))
	year := researchYear(criteria, now)
	if year == 0 {
		year = now.Year()
	}
	parts = append(parts, strconv.Itoa(year), "call for applications guidelines eligibility deadline PDF")
	instructions := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if utf8.RuneCountInString(instructions) > 300 {
		instructions = string([]rune(instructions)[:300])
	}
	return instructions
}

// siteDiscoveryDue reports whether the current fetch step should first map
// official sites: a later round of a bounded-round session that has not run
// discovery yet, with map_site registered, run budget left and enough time
// before the session deadline.
func (w *Worker) siteDiscoveryDue(ctx context.Context, state checkpointState) (int, bool) {
	if !w.tools.Has("map_site") || !roundPlanSession(ctx) || state.ResearchPlans == nil {
		return 0, false
	}
	round := state.ResearchPlans.CurrentRound
	if round < 2 || len(state.MappedSites) >= maxSiteDiscoveries {
		return round, false
	}
	for _, done := range state.SiteDiscoveryRounds {
		if done == round {
			return round, false
		}
	}
	if session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); ok && !session.DeadlineAt.IsZero() {
		descriptor, _ := w.tools.Descriptor("map_site")
		need := time.Duration(max(descriptor.TimeoutSeconds, 60)) * time.Second * (maxSiteDiscoveriesPerRound + 2)
		if time.Until(session.DeadlineAt) < need {
			return round, false
		}
	}
	return round, true
}

// discoverOfficialSites maps up to maxSiteDiscoveriesPerRound official sites
// seen in search hits (agentSearcher /map via map_site) and records the
// best matching discovered pages as tool results that rank first among
// fetch candidates. A failed discovery is recorded and skipped; it never
// fails the run. Each mapped site is checkpointed before the next.
func (w *Worker) discoverOfficialSites(ctx context.Context, taskID, goal string, stage *domain.Stage, state *checkpointState) error {
	round, due := w.siteDiscoveryDue(ctx, *state)
	if !due {
		return nil
	}
	open := append([]string{}, state.Completion.Missing...)
	open = append(open, followUpQuestionTexts(*state)...)
	started := 0
	for _, origin := range officialResultSites(state.ToolResults, maxSiteDiscoveries+len(state.MappedSites)) {
		host := siteHost(origin)
		if host == "" || contains(state.MappedSites, host) {
			continue
		}
		if started == maxSiteDiscoveriesPerRound || len(state.MappedSites) >= maxSiteDiscoveries {
			break
		}
		started++
		instructions := siteDiscoveryInstructions(goal, origin, open, researchNow())
		arguments, err := json.Marshal(map[string]any{"url": origin + "/", "instructions": instructions})
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s%d_%d", siteDiscoveryKeyPrefix, round, len(state.MappedSites)+1)
		result, discoveryErr := w.runSiteDiscovery(ctx, taskID, stage, key, arguments)
		if discoveryErr != nil && (ctx.Err() != nil || errors.Is(discoveryErr, context.Canceled) || errors.Is(discoveryErr, ErrInterrupted) || errors.Is(discoveryErr, ErrRecoveryRequired)) {
			return discoveryErr
		}
		state.MappedSites = append(state.MappedSites, host)
		payload := map[string]any{"round": round, "site": host, "result_key": key}
		if discoveryErr != nil {
			payload["error"] = truncateRunes(discoveryErr.Error(), 120)
		} else {
			state.ToolResults[key] = result
			payload["discovered_urls"] = len(searchResultURLsForValue(result))
		}
		if _, err := w.repo.SaveCheckpoint(ctx, taskID, 1, *state); err != nil {
			return err
		}
		if _, err := w.repo.RecordEvent(ctx, taskID, "site.discovery", stage, "user", payload); err != nil {
			return err
		}
	}
	state.SiteDiscoveryRounds = append(state.SiteDiscoveryRounds, round)
	_, err := w.repo.SaveCheckpoint(ctx, taskID, 1, *state)
	return err
}

func (w *Worker) runSiteDiscovery(ctx context.Context, taskID string, stage *domain.Stage, key string, arguments json.RawMessage) (any, error) {
	descriptor, ok := w.tools.Descriptor("map_site")
	if !ok {
		return nil, errors.New("map_site is not registered")
	}
	normalized, err := normalizeToolArguments(arguments, descriptor)
	if err != nil {
		return nil, err
	}
	_, decision, err := w.tools.Authorize(ctx, "researcher", "map_site", normalized)
	if err != nil {
		return nil, err
	}
	if decision.Action != policy.Allow {
		return nil, errors.New("map_site is not allowed by policy")
	}
	return w.executeObservedTool(ctx, taskID, stage, "researcher", "map_site", normalized, "", key)
}

// discoveredFetchURLs returns unfetched URLs of site discovery results,
// round-robin across the mapped sites, at most limit.
func discoveredFetchURLs(results map[string]any, fetched map[string]struct{}, limit int) []string {
	keys := []string{}
	for key := range results {
		if strings.HasPrefix(key, siteDiscoveryKeyPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lists := [][]string{}
	for _, key := range keys {
		urls := []string{}
		for _, rawURL := range searchResultURLsForValue(results[key]) {
			canonical, err := provenance.CanonicalURL(rawURL)
			if err != nil {
				continue
			}
			if _, done := fetched[canonical]; !done {
				urls = append(urls, canonical)
			}
		}
		lists = append(lists, urls)
	}
	selected := []string{}
	for position := 0; len(selected) < limit; position++ {
		added := false
		for _, urls := range lists {
			if position < len(urls) && len(selected) < limit {
				if !contains(selected, urls[position]) {
					selected = append(selected, urls[position])
				}
				added = true
			}
		}
		if !added {
			break
		}
	}
	return selected
}
