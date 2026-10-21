package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeSearchQueryAllowsOrdinaryQueriesAndBoundsUnicode(t *testing.T) {
	for _, query := range []string{"latest llama.cpp Vulkan support", "Сравнить университеты Алматы и Астаны", "site:docs.example.org API authentication documentation", "password manager documentation", "https://example.org/docs?q=protocol", "TAVILY_API_KEY configuration documentation"} {
		if got := SafeSearchQuery(query); got != query {
			t.Fatalf("safe query changed: %q -> %q", query, got)
		}
	}
	if got := SafeSearchQuery("  Сравнить\n  два\tварианта  "); got != "Сравнить два варианта" {
		t.Fatal(got)
	}
	query := strings.Repeat("界", 700)
	got := SafeSearchQuery(query)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 500 || SafeSearchQuery(got) != got {
		t.Fatalf("invalid Unicode bound %d", utf8.RuneCountInString(got))
	}
}

func TestSafeSearchQuerySuppressesCredentialLikeInputBeforeTruncation(t *testing.T) {
	for _, query := range []string{
		"Bearer private-fixture", "find sk-private-fixture", "tvly-dev-private-fixture", "api_key=private-fixture", "API KEY: private-fixture", "TAVILY_API_KEY = private-fixture", "MY_SECRET: private-fixture", "token = private-fixture", `"token": "private-fixture"`, "password: private-fixture", "secret = private-fixture", "authorization: private-fixture",
		"https://user:private-fixture@example.org/path", "HTTPS://user:private-fixture@example.org/path", "https://example.org/path?access_token=private-fixture", "https://example.org/api_key/private-fixture", "https://example.org/path?redirect=https%3A%2F%2Fu%3Ap%40example.org",
		"api_key%3Dprivate-fixture", "token%253Dprivate-fixture", "to%00ken=private-fixture", "eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyIjoiZml4dHVyZSJ9.signature", "to\u200bken=private-fixture", "query\x00private-fixture", "\xff",
		strings.Repeat("safe ", 130) + "token=private-fixture",
	} {
		if got := SafeSearchQuery(query); got != "" {
			t.Fatalf("credential-like query was published: %q", got)
		}
	}
}
