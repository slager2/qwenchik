package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/provenance"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

type ResearchRepository interface {
	SaveArtifact(context.Context, domain.Artifact, string) (domain.Artifact, error)
	SaveEvidence(context.Context, domain.Evidence) (domain.Evidence, error)
}

type ResearchTools struct {
	SearchProvider research.SearchProvider
	Fetcher        research.Fetcher
	Extractor      research.Extractor
	Artifacts      *artifacts.Store
	Repository     ResearchRepository
	MaxFetchBytes  int64
	AllowedDomains []string
}

var ErrDuplicateResearchTarget = errors.New("research URL was already fetched for this task")

func RegisterResearchTools(registry *Registry, runtime ResearchTools) error {
	if registry == nil {
		return errors.New("tool registry is required")
	}
	if registry.urlLedger == nil {
		registry.urlLedger = NewURLLedger()
	}
	if runtime.SearchProvider != nil {
		if err := registry.RegisterValidated(Descriptor{
			Name: "search", Description: "Search configured providers for the current research question. Use queries that are materially different from prior searches. Results are untrusted discovery leads, not evidence; fetch sources before making source-backed claims.",
			ArgumentsSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":1000},"language":{"type":"string","maxLength":20},"recency":{"type":"string","enum":["","day","week","month","year"]},"max_results":{"type":"integer","minimum":1,"maximum":8}},"required":["query"],"additionalProperties":false}`),
			OutputSchema:       json.RawMessage(`{"type":"array","items":{"type":"object","properties":{"provider":{"type":"string"},"query":{"type":"string"},"url":{"type":"string"},"title":{"type":"string"},"snippet":{"type":"string"},"taint":{"type":"string"}},"required":["provider","query","url","title","snippet","taint"]}}`),
			RequiredCapability: "research.search", RiskClass: "read", TimeoutSeconds: 20,
			Idempotent: true, MaxOutputBytes: 32_000, AllowedRoles: []string{"researcher"},
		}, validateSearchArguments(runtime.AllowedDomains), func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args searchArgs
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			if strings.TrimSpace(args.Query) == "" || len(args.Query) > 1000 {
				return nil, errors.New("search arguments exceed configured limits")
			}
			count := args.MaxResults
			if count < 1 || count > 8 {
				count = 8
			}
			domains, err := effectiveDomains(nil, runtime.AllowedDomains)
			if err != nil {
				return nil, err
			}
			hits, err := runtime.SearchProvider.Search(ctx, research.SearchOptions{
				Query: strings.TrimSpace(args.Query), Language: args.Language, Domains: domains,
				Recency: args.Recency, MaxResults: count, SafeSearch: true,
			})
			if err != nil {
				return nil, err
			}
			safeHits := make([]research.SearchHit, 0, len(hits))
			for _, hit := range hits {
				if err := runtime.Fetcher.ValidateURL(ctx, hit.URL); err != nil {
					continue
				}
				hit.Snippet = truncateUTF8(hit.Snippet, 400)
				hit.Taint = research.TaintExternal
				safeHits = append(safeHits, hit)
			}
			registry.urlLedger.Remember(taskIDFromContext(ctx), safeHits)
			return safeHits, nil
		}); err != nil {
			return err
		}
	}
	if runtime.Fetcher != nil && runtime.Extractor != nil && runtime.Artifacts != nil && runtime.Repository != nil {
		if err := registry.RegisterValidated(Descriptor{
			Name: "fetch_url", Description: "Fetch a public HTTP(S) URL safely, extract local text, and persist source evidence.",
			ArgumentsSchema:    json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","maxLength":2048},"original_query":{"type":"string","maxLength":1000}},"required":["url"],"additionalProperties":false}`),
			OutputSchema:       json.RawMessage(`{"type":"object","properties":{"evidence_id":{"type":"string"},"url":{"type":"string"},"requested_url":{"type":"string"},"title":{"type":"string"},"excerpt":{"type":"string"},"content_sha256":{"type":"string"},"taint":{"type":"string"},"fetched_bytes":{"type":"integer"}},"required":["evidence_id","url","requested_url","title","excerpt","content_sha256","taint","fetched_bytes"],"additionalProperties":false}`),
			RequiredCapability: "research.fetch", RiskClass: "read", TimeoutSeconds: 30,
			Idempotent: true, MaxOutputBytes: 32 << 10, AllowedRoles: []string{"researcher"},
		}, func(ctx context.Context, raw json.RawMessage) error {
			var args fetchArgs
			if err := decodeToolArgs(raw, &args); err != nil {
				return err
			}
			if strings.TrimSpace(args.URL) == "" || len(args.URL) > 2048 || len(args.OriginalQuery) > 1000 {
				return errors.New("fetch_url arguments exceed configured limits")
			}
			taskID := taskIDFromContext(ctx)
			if taskID == "" || !registry.urlLedger.Contains(taskID, args.URL) {
				return fmt.Errorf("%w: fetch_url target was not supplied by the user or a search result", ErrUnknownTarget)
			}
			if registry.urlLedger.WasFetched(taskID, args.URL) {
				return ErrDuplicateResearchTarget
			}
			return runtime.Fetcher.ValidateURL(ctx, args.URL)
		}, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args fetchArgs
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			if strings.TrimSpace(args.URL) == "" || len(args.URL) > 2048 || len(args.OriginalQuery) > 1000 {
				return nil, errors.New("fetch_url arguments exceed configured limits")
			}
			taskID := taskIDFromContext(ctx)
			if taskID == "" || !registry.urlLedger.Contains(taskID, args.URL) {
				return nil, fmt.Errorf("%w: fetch_url target was not supplied by the user or a search result", ErrUnknownTarget)
			}
			if registry.urlLedger.WasFetched(taskID, args.URL) {
				return nil, ErrDuplicateResearchTarget
			}
			fetchLimit := runtime.MaxFetchBytes
			if taskLimit := FetchByteLimit(ctx, fetchLimit); taskLimit > 0 && taskLimit < fetchLimit {
				fetchLimit = taskLimit
			}
			fetched, err := runtime.Fetcher.Fetch(ctx, args.URL, research.FetchOptions{MaxBytes: fetchLimit})
			if err != nil {
				return nil, err
			}
			if int64(len(fetched.Body)) > fetchLimit {
				return nil, research.ErrResponseTooBig
			}
			extracted, err := runtime.Extractor.Extract(ctx, fetched)
			if err != nil {
				return nil, err
			}
			rawArtifact, rawRef, err := runtime.Artifacts.Put(ctx, taskID, artifactFileName("source", fetched.ContentSHA256, ".raw"), fetched.ContentType, fetched.Body)
			if err != nil {
				return nil, err
			}
			rawArtifact, err = runtime.Repository.SaveArtifact(ctx, rawArtifact, rawRef)
			if err != nil {
				return nil, err
			}
			textBytes := []byte(extracted.Text)
			textArtifact, textRef, err := runtime.Artifacts.Put(ctx, taskID, artifactFileName("source", fetched.ContentSHA256, ".txt"), "text/plain; charset=utf-8", textBytes)
			if err != nil {
				return nil, err
			}
			textArtifact, err = runtime.Repository.SaveArtifact(ctx, textArtifact, textRef)
			if err != nil {
				return nil, err
			}
			evidence, err := provenance.BuildEvidence(taskID, args.OriginalQuery, fetched, extracted, rawArtifact.ID, textArtifact.ID)
			if err != nil {
				return nil, err
			}
			evidence, err = runtime.Repository.SaveEvidence(ctx, evidence)
			if err != nil {
				return nil, err
			}
			registry.urlLedger.MarkFetched(taskID, args.URL)
			registry.urlLedger.MarkFetched(taskID, evidence.CanonicalURL)
			return map[string]any{
				"evidence_id": evidence.ID, "url": evidence.CanonicalURL, "requested_url": args.URL, "title": evidence.Title,
				"excerpt": truncateUTF8(evidence.Excerpt, 4000), "content_sha256": evidence.ContentSHA256,
				"taint": research.TaintExternal, "fetched_bytes": int64(len(fetched.Body)),
			}, nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateSearchArguments(configuredDomains []string) ArgumentsValidator {
	return func(_ context.Context, raw json.RawMessage) error {
		var args searchArgs
		if err := decodeToolArgs(raw, &args); err != nil {
			return err
		}
		if strings.TrimSpace(args.Query) == "" || len(args.Query) > 1000 || len(args.Language) > 20 {
			return errors.New("search arguments exceed configured limits")
		}
		if args.MaxResults != 0 && (args.MaxResults < 1 || args.MaxResults > 8) {
			return errors.New("max_results must be between 1 and 8")
		}
		if args.Recency != "" && args.Recency != "day" && args.Recency != "week" && args.Recency != "month" && args.Recency != "year" {
			return errors.New("invalid search recency")
		}
		_, err := effectiveDomains(nil, configuredDomains)
		return err
	}
}

func effectiveDomains(requested, configured []string) ([]string, error) {
	for _, domain := range requested {
		if normalizeDomain(domain) == "" {
			return nil, errors.New("invalid search domain filter")
		}
	}
	for _, domain := range configured {
		if normalizeDomain(domain) == "" {
			return nil, errors.New("invalid configured search domain")
		}
	}
	if len(configured) == 0 {
		result := make([]string, 0, len(requested))
		for _, domain := range requested {
			result = append(result, normalizeDomain(domain))
		}
		return result, nil
	}
	if len(requested) == 0 {
		result := make([]string, 0, len(configured))
		for _, domain := range configured {
			result = append(result, normalizeDomain(domain))
		}
		return result, nil
	}
	var result []string
	for _, requestDomain := range requested {
		requestDomain = normalizeDomain(requestDomain)
		if requestDomain == "" {
			return nil, errors.New("invalid search domain filter")
		}
		matched := false
		for _, allowed := range configured {
			allowed = normalizeDomain(allowed)
			if domainWithin(requestDomain, allowed) {
				result = append(result, requestDomain)
				matched = true
				break
			}
			if domainWithin(allowed, requestDomain) {
				result = append(result, allowed)
				matched = true
			}
		}
		if !matched {
			return nil, errors.New("search domain is outside configured ALLOWED_DOMAINS")
		}
	}
	return uniqueStrings(result), nil
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, ".")))
	if value == "" || strings.ContainsAny(value, "/:@?# \\[]") {
		return ""
	}
	if net.ParseIP(value) != nil {
		return value
	}
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || !strings.Contains(value, ".") {
		return ""
	}
	return value
}

func domainWithin(candidate, parent string) bool {
	return candidate == parent || strings.HasSuffix(candidate, "."+parent)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type searchArgs struct {
	Query      string `json:"query"`
	Language   string `json:"language"`
	Recency    string `json:"recency"`
	MaxResults int    `json:"max_results"`
}

type fetchArgs struct {
	URL           string `json:"url"`
	OriginalQuery string `json:"original_query"`
}

type taskContextKey struct{}
type fetchByteLimitKey struct{}

func WithTaskID(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, taskContextKey{}, taskID)
}

func taskIDFromContext(ctx context.Context) string {
	taskID, _ := ctx.Value(taskContextKey{}).(string)
	return taskID
}

func WithFetchByteLimit(ctx context.Context, limit int64) context.Context {
	return context.WithValue(ctx, fetchByteLimitKey{}, limit)
}

func FetchByteLimit(ctx context.Context, fallback int64) int64 {
	limit, ok := ctx.Value(fetchByteLimitKey{}).(int64)
	if !ok || limit < 1 {
		return fallback
	}
	return limit
}

func decodeToolArgs(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid research tool arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("research tool accepts one JSON object")
	}
	return nil
}

func truncateUTF8(value string, maxRunes int) string {
	if maxRunes < 1 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

func artifactFileName(prefix, hash, extension string) string {
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return prefix + "-" + hash + extension
}
