package urlcanon

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

var ErrInvalidURL = errors.New("invalid HTTP(S) URL")

// Canonical normalizes a public HTTP(S) URL for source deduplication.
func Canonical(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil {
		return "", ErrInvalidURL
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalidURL
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", ErrInvalidURL
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
