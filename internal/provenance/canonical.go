package provenance

import (
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
)

var ErrInvalidSourceURL = errors.New("invalid source URL")

func CanonicalURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil {
		return "", ErrInvalidSourceURL
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalidSourceURL
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", ErrInvalidSourceURL
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		parsed.Host = host + ":" + port
	} else {
		parsed.Host = host
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "mc_cid" || lower == "mc_eid" {
			query.Del(key)
		}
	}
	for key := range query {
		sort.Strings(query[key])
	}
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

func BuildEvidence(taskID, query string, fetched research.FetchedDocument, extracted research.ExtractedDocument, rawArtifactID, textArtifactID string) (domain.Evidence, error) {
	canonical, err := CanonicalURL(fetched.FinalURL)
	if err != nil {
		return domain.Evidence{}, err
	}
	if taskID == "" || fetched.ContentSHA256 == "" {
		return domain.Evidence{}, errors.New("evidence requires a task ID and content hash")
	}
	retrievedAt := fetched.RetrievedAt
	if retrievedAt.IsZero() {
		retrievedAt = time.Now().UTC()
	}
	excerpt := strings.TrimSpace(extracted.Text)
	if utf8.RuneCountInString(excerpt) > 1200 {
		runes := []rune(excerpt)
		excerpt = string(runes[:1200])
	}
	quotes := []domain.QuoteSpan{}
	if excerpt != "" {
		quotes = append(quotes, domain.QuoteSpan{Start: 0, End: utf8.RuneCountInString(excerpt), Text: excerpt})
	}
	provider := fetched.Provider
	if provider == "" {
		provider = "direct_http"
	}
	return domain.Evidence{
		TaskID: taskID, Provider: provider, OriginalQuery: query,
		OriginalURL: fetched.OriginalURL, CanonicalURL: canonical,
		Title: extracted.Title, Language: fetched.Language, RetrievedAt: retrievedAt, HTTPStatus: fetched.HTTPStatus,
		ContentSHA256: fetched.ContentSHA256, ExtractionMethod: extracted.Method,
		RawArtifactID: rawArtifactID, TextArtifactID: textArtifactID,
		Excerpt: excerpt, QuoteSpans: quotes, QualityFlags: append([]string(nil), extracted.QualityFlags...),
		Taint: research.TaintExternal,
	}, nil
}
