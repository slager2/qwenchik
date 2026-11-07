package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/slager2/qwenchik/internal/providers/research"
)

// SiteMapper discovers URLs of one site (agentSearcher /map). The caller
// validates the root URL; every returned URL is validated again here.
type SiteMapper interface {
	Map(ctx context.Context, rawURL, instructions string) ([]string, error)
	Budget() time.Duration
}

// SiteMapProvider marks map_site results, so search-hit consumers know the
// URL came from site discovery, not from a search engine.
const SiteMapProvider = "agentsearcher_map"

// MaxSiteMapResults bounds the discovered URLs one map_site call returns.
const MaxSiteMapResults = 6

// siteMapNow is the clock of the year preference; tests replace it.
var siteMapNow = func() time.Time { return time.Now().UTC() }

type siteMapArgs struct {
	URL          string `json:"url"`
	Instructions string `json:"instructions"`
}

// siteMapHints are path words of the pages research usually needs on an
// official site.
var siteMapHints = []string{"call", "apply", "application", "admission", "deadline", "guideline", "guide", "notice", "announcement", "eligib", "requirement", "scholarship", "calendar", "faq", "programme", "program", "document", "download", "brochure", "regulation", "tuition", "fee"}

// siteMapStopWords carry no relevance in instructions.
var siteMapStopWords = map[string]bool{"for": true, "the": true, "and": true, "with": true, "from": true}

// numberedCopy matches the "-2", "-3" suffix of re-posted pages
// (final-call-...-2/), which count as the same path.
var numberedCopy = regexp.MustCompile(`-[0-9]{1,2}$`)

var siteMapYear = regexp.MustCompile(`(?:^|[^0-9])(20[0-9]{2})(?:[^0-9]|$)`)

func siteMapWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// rankDiscoveredURLs keeps the discovered URLs whose path matches the
// research criteria: instruction words and common official-page words in the
// path, PDFs, and the current or next year score; paths whose newest year is
// before the previous one are dropped. The site root is dropped (search already
// found it) and at most two URLs share one path. Ties keep discovery order.
func rankDiscoveredURLs(urls []string, instructions string, now time.Time, limit int) []string {
	wanted := map[string]bool{}
	for _, word := range siteMapWords(instructions) {
		if len([]rune(word)) >= 3 && !siteMapStopWords[word] {
			wanted[word] = true
		}
	}
	type scored struct {
		url          string
		score, order int
		path         string
	}
	candidates := []scored{}
	for index, raw := range urls {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		path := strings.TrimRight(parsed.Path, "/")
		if path == "" && parsed.RawQuery == "" {
			continue
		}
		text := strings.ToLower(path + " " + parsed.RawQuery)
		score := 0
		for _, word := range siteMapWords(text) {
			if wanted[word] {
				score += 2
			}
			for _, hint := range siteMapHints {
				if strings.HasPrefix(word, hint) {
					score++
					break
				}
			}
		}
		if strings.HasSuffix(strings.ToLower(path), ".pdf") {
			score += 2
		}
		newest := 0
		for _, match := range siteMapYear.FindAllStringSubmatch(text, -1) {
			if year, err := strconv.Atoi(match[1]); err == nil && year <= now.Year()+2 {
				newest = max(newest, year)
			}
		}
		if newest > 0 && newest < now.Year()-1 {
			// A page about an outdated call or intake.
			continue
		}
		if newest >= now.Year() {
			// Freshness outweighs one more matching path word.
			score += 5
		}
		if score >= 2 {
			candidates = append(candidates, scored{url: raw, score: score, order: index, path: parsed.Host + numberedCopy.ReplaceAllString(path, "")})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	perPath := map[string]int{}
	result := []string{}
	for _, item := range candidates {
		if len(result) == limit {
			break
		}
		if perPath[item.path] >= 2 {
			continue
		}
		perPath[item.path]++
		result = append(result, item.url)
	}
	return result
}

// registerSiteMapTool registers map_site: agentSearcher /map discovery of
// one official site's key pages. The root URL and every discovered URL pass
// the fetcher's URL policy (SSRF); results are remembered as fetchable
// targets like search hits and returned as search hits of provider
// agentsearcher_map whose query is the instructions.
func registerSiteMapTool(registry *Registry, runtime ResearchTools) error {
	timeout := int((runtime.SiteMapper.Budget() + 5*time.Second + time.Second - 1) / time.Second)
	return registry.RegisterValidated(Descriptor{
		Name: "map_site", Description: "Discover the key pages and PDFs of one official site (agentSearcher /map) for the research criteria. Results are untrusted discovery leads, not evidence.",
		ArgumentsSchema:    json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","maxLength":2048},"instructions":{"type":"string","maxLength":300}},"required":["url"],"additionalProperties":false}`),
		OutputSchema:       json.RawMessage(`{"type":"array","items":{"type":"object","properties":{"provider":{"type":"string"},"query":{"type":"string"},"url":{"type":"string"},"title":{"type":"string"},"snippet":{"type":"string"},"taint":{"type":"string"}},"required":["provider","query","url","title","snippet","taint"]}}`),
		RequiredCapability: "research.search", RiskClass: "read", TimeoutSeconds: timeout,
		Idempotent: true, MaxOutputBytes: 32 << 10, AllowedRoles: []string{"researcher"},
	}, func(ctx context.Context, raw json.RawMessage) error {
		var args siteMapArgs
		if err := decodeToolArgs(raw, &args); err != nil {
			return err
		}
		if strings.TrimSpace(args.URL) == "" || len(args.URL) > 2048 || len([]rune(args.Instructions)) > 300 {
			return errors.New("map_site arguments exceed configured limits")
		}
		return runtime.Fetcher.ValidateURL(ctx, args.URL)
	}, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args siteMapArgs
		if err := decodeToolArgs(raw, &args); err != nil {
			return nil, err
		}
		if err := runtime.Fetcher.ValidateURL(ctx, args.URL); err != nil {
			return nil, err
		}
		discovered, err := runtime.SiteMapper.Map(ctx, args.URL, args.Instructions)
		if err != nil {
			return nil, fmt.Errorf("map_site: %w", err)
		}
		instructions := strings.Join(strings.Fields(args.Instructions), " ")
		hits := []research.SearchHit{}
		validatedHosts := map[string]error{}
		for _, candidate := range rankDiscoveredURLs(discovered, instructions, siteMapNow(), MaxSiteMapResults) {
			parsed, err := url.Parse(candidate)
			if err != nil {
				continue
			}
			host := strings.ToLower(parsed.Hostname())
			validation, checked := validatedHosts[host]
			if !checked {
				validation = runtime.Fetcher.ValidateURL(ctx, candidate)
				validatedHosts[host] = validation
			}
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			if validation != nil {
				continue
			}
			hits = append(hits, research.SearchHit{Provider: SiteMapProvider, Query: instructions, URL: candidate, Title: siteMapTitle(parsed), Taint: research.TaintExternal})
		}
		registry.urlLedger.Remember(taskIDFromContext(ctx), hits)
		return hits, nil
	})
}

// siteMapTitle is a readable title from the URL path, for the fetch query
// choice and the candidate list.
func siteMapTitle(parsed *url.URL) string {
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := segments[len(segments)-1]
	if last == "" {
		return parsed.Hostname()
	}
	return truncateUTF8(strings.Join(strings.FieldsFunc(last, func(r rune) bool { return r == '-' || r == '_' || r == '.' }), " "), 200)
}
