package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/urlcanon"
)

type searchHitObserverKey struct{}

var diagnosticFingerprint = regexp.MustCompile(`^[a-f0-9]{64}$`)
var credentialDiagnosticText = regexp.MustCompile(`(?i)\b(?:access[_-]?token|auth[_-]?token|api[_-]?key|client[_-]?secret|password|passwd|secret|signature|authorization)\s*[:=]`)
var queryCredentialText = regexp.MustCompile(`(?i)(?:\bbearer\b|\bsk-|\btvly-|(?:\b|_)(?:token|access[_ -]?token|auth[_ -]?token|api[_ -]?key|client[_ -]?secret|password|passwd|secret|signature|authorization|credentials?|session(?:[_ -]?id)?)["']?\s*[:=])`)
var queryJWTText = regexp.MustCompile(`(?i)\beyj[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+\b`)

func SafeSearchQuery(query string) string {
	if len(query) > 16384 || !utf8.ValidString(query) {
		return ""
	}
	for _, r := range query {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && !unicode.IsSpace(r) {
			return ""
		}
	}
	view := query
	for pass := 0; pass < 3; pass++ {
		for _, r := range view {
			if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && !unicode.IsSpace(r) {
				return ""
			}
		}
		if queryCredentialText.MatchString(view) || queryJWTText.MatchString(view) {
			return ""
		}
		for _, raw := range httpURLPattern.FindAllString(strings.ToLower(view), -1) {
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() == "" || research.URLHasSensitiveData(u) {
				return ""
			}
		}
		decoded, err := url.QueryUnescape(view)
		if err != nil || decoded == view {
			break
		}
		view = decoded
	}
	query = strings.Join(strings.Fields(query), " ")
	runes := []rune(query)
	if len(runes) > 500 {
		query = string(runes[:500])
	}
	return query
}

func SafeSearchHitObservation(o research.SearchHitObservation) research.SearchHitObservation {
	if !diagnosticFingerprint.MatchString(o.Fingerprint) {
		o.Fingerprint = fingerprint(o.Fingerprint)
	}
	o.ID = o.Fingerprint
	if !diagnosticFingerprint.MatchString(o.QueryFingerprint) {
		o.QueryFingerprint = fingerprint(o.QueryFingerprint)
	}
	switch o.Provider {
	case "searxng", "tavily", "duckduckgo":
	default:
		o.Provider = "search"
	}
	switch o.Status {
	case "discovered", "validated", "rejected":
	default:
		o.Status = "rejected"
	}
	switch o.ErrorCode {
	case "", "url_rejected_ssrf", "url_validation_failed", "search_validation_timeout":
	default:
		o.ErrorCode = "url_validation_failed"
	}
	safeURL, origin, safe := safeDiscoveryURL(o.URL)
	if safe {
		o.URL, o.Origin = safeURL, origin
		o.Title, o.Snippet = safeDiscoveryText(o.Title, 500), safeDiscoveryText(o.Snippet, 400)
	} else {
		o.URL, o.Title, o.Snippet = "", "", ""
		u, err := url.Parse(o.Origin)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
			o.Origin = ""
		} else {
			o.Origin = u.Scheme + "://" + u.Host
		}
	}
	return o
}

func safeDiscoveryText(text string, limit int) string {
	if credentialDiagnosticText.MatchString(text) {
		return ""
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"bearer ", "token=", "secret=", "password=", "api_key=", "apikey=", "authorization:"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	for _, raw := range httpURLPattern.FindAllString(text, -1) {
		if _, _, safe := safeDiscoveryURL(raw); !safe {
			return ""
		}
	}
	return truncateUTF8(text, limit)
}

func WithSearchHitObserver(ctx context.Context, observer func(context.Context, research.SearchHitObservation) error) context.Context {
	return context.WithValue(ctx, searchHitObserverKey{}, observer)
}

func searchCollectionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := 12 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)*3/5)
	}
	return context.WithTimeout(ctx, budget)
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func safeDiscoveryURL(raw string) (string, string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", false
	}
	origin := u.Scheme + "://" + u.Host
	if len(raw) > 2048 || u.User != nil || research.URLHasSensitiveData(u) {
		return "", origin, false
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String(), origin, true
}

func observeSearchHit(ctx context.Context, hit research.SearchHit, query, status, code string, retryable bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	idURL, err := urlcanon.Canonical(hit.URL)
	if err != nil {
		idURL = hit.URL
	}
	id := fingerprint(idURL)
	o := research.SearchHitObservation{ID: id, Fingerprint: id, QueryFingerprint: fingerprint(query), Status: status, ErrorCode: code, Retryable: retryable, Time: time.Now().UTC()}
	switch hit.Provider {
	case "searxng", "tavily", "duckduckgo":
		o.Provider = hit.Provider
	default:
		o.Provider = "search"
	}
	safeURL, origin, safe := safeDiscoveryURL(hit.URL)
	o.Origin = origin
	if safe {
		o.URL, o.Title, o.Snippet = safeURL, truncateUTF8(hit.Title, 500), truncateUTF8(hit.Snippet, 400)
	}
	if observer, ok := ctx.Value(searchHitObserverKey{}).(func(context.Context, research.SearchHitObservation) error); ok && observer != nil {
		if err := observer(ctx, SafeSearchHitObservation(o)); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func validateSearchHits(ctx context.Context, fetcher research.Fetcher, hits []research.SearchHit, outcomes []research.ProviderOutcome, query string) ([]research.SearchHit, error) {
	if len(hits) == 0 {
		return []research.SearchHit{}, nil
	}
	if fetcher == nil {
		return nil, errors.New("search URL policy requires a fetcher")
	}
	unique := make([]research.SearchHit, 0, len(hits))
	seen := map[string]bool{}
	for _, hit := range hits {
		key, err := urlcanon.Canonical(hit.URL)
		if err != nil {
			key = hit.URL
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, hit)
		if err := observeSearchHit(ctx, hit, query, "discovered", "", false); err != nil {
			return nil, err
		}
	}
	budget := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)*4/5)
	}
	validationCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type validationResult struct {
		index int
		err   error
	}
	jobs := make(chan int, len(unique))
	results := make(chan validationResult, len(unique))
	for i := range unique {
		jobs <- i
	}
	close(jobs)
	for worker := 0; worker < min(4, len(unique)); worker++ {
		go func() {
			for i := range jobs {
				hitCtx, hitCancel := context.WithTimeout(validationCtx, time.Second)
				var err error
				if hitCtx.Err() != nil {
					err = hitCtx.Err()
				} else if _, _, safe := safeDiscoveryURL(unique[i].URL); !safe {
					err = research.ErrUnsafeURL
				} else {
					err = fetcher.ValidateURL(hitCtx, unique[i].URL)
					if hitCtx.Err() != nil {
						err = hitCtx.Err()
					}
				}
				hitCancel()
				results <- validationResult{i, err}
			}
		}()
	}
	errorsByIndex := make([]error, len(unique))
	received := make([]bool, len(unique))
	for remaining := len(unique); remaining > 0; {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-results:
			errorsByIndex[result.index] = result.err
			received[result.index] = true
			remaining--
		case <-validationCtx.Done():
			for draining := true; draining; {
				select {
				case result := <-results:
					errorsByIndex[result.index] = result.err
					received[result.index] = true
				default:
					draining = false
				}
			}
			for i := range unique {
				if !received[i] {
					errorsByIndex[i] = validationCtx.Err()
				}
			}
			remaining = 0
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	safeHits := make([]research.SearchHit, 0, len(unique))
	allPolicy, timedOut := true, false
	for i, hit := range unique {
		err := errorsByIndex[i]
		status, code, retryable := "validated", "", false
		if err != nil {
			status, code = "rejected", "url_rejected_ssrf"
			if !errors.Is(err, research.ErrUnsafeURL) {
				allPolicy = false
				code, retryable = "url_validation_failed", true
			}
			if errors.Is(err, context.DeadlineExceeded) {
				timedOut = true
				code, retryable = "search_validation_timeout", true
			}
		} else {
			safeURL, _, _ := safeDiscoveryURL(hit.URL)
			hit.URL, hit.Title, hit.Snippet, hit.Taint = safeURL, safeDiscoveryText(hit.Title, 500), safeDiscoveryText(hit.Snippet, 400), research.TaintExternal
			hit.Query = query
			switch hit.Provider {
			case "searxng", "tavily", "duckduckgo":
			default:
				hit.Provider = "search"
			}
			safeHits = append(safeHits, hit)
		}
		if err := observeSearchHit(ctx, hit, query, status, code, retryable); err != nil {
			return nil, err
		}
	}
	if len(safeHits) > 0 {
		return safeHits, nil
	}
	code := "search_no_usable_hits"
	cause := errors.Join(errorsByIndex...)
	if allPolicy {
		code = "search_filtered_by_policy"
	} else if timedOut {
		code, cause = "search_validation_timeout", context.DeadlineExceeded
	}
	return nil, &research.SearchFailure{Code: code, Outcomes: outcomes, Cause: cause}
}
