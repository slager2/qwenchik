package research

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDuckDuckGoSearchParsesOrganicResultsAndUsesBoundedBrowserRequest(t *testing.T) {
	var requests []*http.Request
	fetcher := testSafeFetcher(t, duckDuckGoMaxBody, time.Second, resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}), nil)
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(request.Context()))
		body := duckDuckGoFixture(10)
		header := http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
		if len(requests) == 1 {
			header.Set("Set-Cookie", "session=fixture; Path=/html/")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: request}, nil
	})
	provider, err := NewDuckDuckGoWithFetcher(fetcher)
	if err != nil {
		t.Fatal(err)
	}
	options := SearchOptions{Query: " local search ", Language: "us-en", Recency: "week", MaxResults: 20, SafeSearch: true, Domains: []string{"docs.example.com"}}
	hits, err := provider.Search(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != duckDuckGoMaxHits {
		t.Fatalf("got %d hits, want cap %d: %#v", len(hits), duckDuckGoMaxHits, hits)
	}
	first, err := url.Parse(requests[0].URL.String())
	if err != nil {
		t.Fatal(err)
	}
	if first.Host != duckDuckGoHost || first.Path != "/html/" || first.Query().Get("q") != "local search" || first.Query().Get("kp") != "1" || first.Query().Get("kl") != "us-en" || first.Query().Get("df") != "w" {
		t.Fatalf("unexpected fixed-endpoint request: %s", requests[0].URL)
	}
	if !strings.Contains(requests[0].Header.Get("User-Agent"), "Mozilla/5.0") || requests[0].Header.Get("Accept-Language") != "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7" || !strings.Contains(requests[0].Header.Get("Accept"), "text/html") {
		t.Fatalf("browser-like request headers are missing: %#v", requests[0].Header)
	}
	if hits[0].URL != "https://docs.example.com/guide?from=search&x=1" || hits[0].Title != "Official & useful guide" || hits[0].Snippet != "A useful summary." || hits[0].Provider != "duckduckgo" || hits[0].Taint != TaintExternal {
		t.Fatalf("unexpected normalized DDG hit: %#v", hits[0])
	}
	for _, hit := range hits {
		if !strings.HasPrefix(hit.URL, "https://docs.example.com/") || hit.Taint != TaintExternal {
			t.Errorf("domain filter or external taint missing: %#v", hit)
		}
	}

	if _, err := provider.Search(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[1].Header.Get("Cookie") != "session=fixture" {
		t.Fatalf("cookie jar did not retain the fixture cookie: requests=%d cookie=%q", len(requests), requests[1].Header.Get("Cookie"))
	}
}

func TestDuckDuckGoSearchRejectsRedirectsAndOversizedResponses(t *testing.T) {
	t.Run("does not follow redirect", func(t *testing.T) {
		calls := 0
		provider := newFixtureDuckDuckGo(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"http://127.0.0.1/private"}},
				Body:       io.NopCloser(strings.NewReader("")), Request: request,
			}, nil
		}))
		_, err := provider.Search(context.Background(), SearchOptions{Query: "safe query", MaxResults: 1})
		if !errors.Is(err, ErrSearchUnavailable) || calls != 1 {
			t.Fatalf("redirect result err=%v calls=%d, want unavailable without following", err, calls)
		}
	})

	t.Run("bounds response bytes", func(t *testing.T) {
		provider := newFixtureDuckDuckGo(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := strings.Repeat("x", duckDuckGoMaxBody+1)
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/html"}},
				ContentLength: -1, Body: io.NopCloser(strings.NewReader(body)), Request: request,
			}, nil
		}))
		_, err := provider.Search(context.Background(), SearchOptions{Query: "safe query", MaxResults: 1})
		if !errors.Is(err, ErrSearchResponseTooBig) {
			t.Fatalf("oversized response error = %v, want ErrSearchResponseTooBig", err)
		}
	})
}

func TestNormalizeDuckDuckGoResultURLUnwrapsAndValidatesTargets(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "unwrap click URL", input: "/l/?uddg=https%3A%2F%2Fdocs.example.com%2Fguide%3Ffrom%3Dsearch%26x%3D1&rut=tracking", want: "https://docs.example.com/guide?from=search&x=1"},
		{name: "normalize direct URL", input: "HTTPS://Docs.Example.COM/guide#section", want: "https://docs.example.com/guide"},
		{name: "reject non-web target", input: "/l/?uddg=file%3A%2F%2F%2Fetc%2Fpasswd", wantErr: true},
		{name: "reject URL credentials", input: "https://user:pass@example.com/", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeDuckDuckGoResultURL(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalizeDuckDuckGoResultURL(%q) unexpectedly succeeded", test.input)
				}
				return
			}
			if err != nil || got.String() != test.want {
				t.Fatalf("normalizeDuckDuckGoResultURL(%q) = %v, %v; want %q", test.input, got, err, test.want)
			}
		})
	}
}

func TestDuckDuckGoValidatesFixedHostAndSearchOptions(t *testing.T) {
	fetcher := testSafeFetcher(t, duckDuckGoMaxBody, time.Second, resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}), []string{duckDuckGoHost})
	provider, err := NewDuckDuckGoWithFetcher(fetcher)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []SearchOptions{
		{Query: "  ", MaxResults: 1},
		{Query: strings.Repeat("q", duckDuckGoMaxQuery+1), MaxResults: 1},
		{Query: "query", Language: "en/../private", MaxResults: 1},
		{Query: "query", Recency: "forever", MaxResults: 1},
	} {
		if _, err := provider.Search(context.Background(), options); err == nil {
			t.Errorf("Search(%#v) unexpectedly succeeded", options)
		}
	}
	if err := provider.fetcher.ValidateURL(context.Background(), duckDuckGoEndpoint); err != nil {
		t.Fatalf("fixed DuckDuckGo host was rejected by SafeFetcher: %v", err)
	}
	if err := provider.fetcher.ValidateURL(context.Background(), "https://other.example/"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("host outside DuckDuckGo allowlist error = %v, want ErrUnsafeURL", err)
	}
}

func newFixtureDuckDuckGo(t *testing.T, transport http.RoundTripper) *DuckDuckGo {
	t.Helper()
	fetcher := testSafeFetcher(t, duckDuckGoMaxBody, time.Second, resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}), nil)
	fetcher.client.Transport = transport
	provider, err := NewDuckDuckGoWithFetcher(fetcher)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func duckDuckGoFixture(count int) string {
	var body strings.Builder
	body.WriteString(`<html><body>`)
	body.WriteString(`<div class="result sponsored"><h2><a class="result__a" href="https://ads.example.com/">Sponsored result</a></h2></div>`)
	body.WriteString(`<div class="result"><h2><a class="result__a" href="file:///etc/passwd">Unsafe result</a></h2></div>`)
	body.WriteString(`<div class="result"><h2><a class="result__a" href="https://outside.example.com/">Wrong domain</a></h2></div>`)
	for i := 0; i < count; i++ {
		if i == 0 {
			body.WriteString(`<div class="result results_links results_links_deep web-result"><h2 class="result__title"><a class="result__a" href="/l/?uddg=https%3A%2F%2Fdocs.example.com%2Fguide%3Ffrom%3Dsearch%26x%3D1&amp;rut=tracking">Official &amp; useful guide</a></h2><a class="result__snippet">A useful <b>summary</b>.</a></div>`)
			continue
		}
		fmt.Fprintf(&body, `<div class="result"><h2><a class="result__a" href="https://docs.example.com/page%d">Guide %d</a></h2></div>`, i, i)
	}
	body.WriteString(`</body></html>`)
	return body.String()
}
