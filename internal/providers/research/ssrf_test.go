package research

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestValidateURLRejectsLocalAndPrivateTargets(t *testing.T) {
	resolver := resolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		addresses := map[string]string{
			"private.example":   "10.2.3.4",
			"metadata.example":  "169.254.169.254",
			"v6private.example": "fd00::1",
			"public.example":    "93.184.216.34",
			"docs.example.com":  "93.184.216.34",
		}
		ip := net.ParseIP(addresses[host])
		if ip == nil {
			return nil, errors.New("unknown test hostname")
		}
		return []net.IPAddr{{IP: ip}}, nil
	})
	fetcher := NewSafeFetcherWithResolver(1024, time.Second, resolver)
	blocked := []string{
		"http://127.0.0.1/",
		"http://localhost/",
		"http://private.example/",
		"http://metadata.example/latest/meta-data/",
		"http://v6private.example/",
		"file:///etc/passwd",
		"http://user:password@public.example/",
		"http://public.example/?access_token=secret",
		"http://public.example:8081/",
	}
	for _, rawURL := range blocked {
		if err := fetcher.ValidateURL(context.Background(), rawURL); !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("ValidateURL(%q) error = %v, want ErrUnsafeURL", rawURL, err)
		}
	}
	if err := fetcher.ValidateURL(context.Background(), "https://public.example/path"); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
	allowlisted := NewSafeFetcherWithPolicy(1024, time.Second, resolver, []string{"example.com"})
	if err := allowlisted.ValidateURL(context.Background(), "https://docs.example.com/path"); err != nil {
		t.Fatalf("configured domain rejected: %v", err)
	}
	if err := allowlisted.ValidateURL(context.Background(), "https://other.example/path"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("host outside configured allowlist error = %v", err)
	}
}

func TestDialContextBlocksDNSRebindingOnConnection(t *testing.T) {
	calls := 0
	resolver := resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		address := "93.184.216.34"
		if calls > 1 {
			address = "127.0.0.1"
		}
		return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
	})
	fetcher := NewSafeFetcherWithResolver(1024, time.Second, resolver)
	if err := fetcher.ValidateURL(context.Background(), "https://rebind.example/"); err != nil {
		t.Fatal(err)
	}
	_, err := fetcher.dialContext(context.Background(), "tcp", "rebind.example:443")
	if !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("rebinding dial error = %v, want ErrUnsafeURL", err)
	}
}

func TestFetchBoundsBodyAndBlocksPrivateRedirect(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	fetcher := NewSafeFetcherWithResolver(1024, time.Second, resolver)
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader("<html>safe</html>")), Request: request}, nil
	})
	document, err := fetcher.Fetch(context.Background(), "https://public.example/article", FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if document.ContentSHA256 == "" || string(document.Body) != "<html>safe</html>" || document.FinalURL != "https://public.example/article" {
		t.Fatalf("unexpected fetched document: %#v", document)
	}
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<html><body>sniffed</body></html>")), Request: request}, nil
	})
	sniffed, err := fetcher.Fetch(context.Background(), "https://public.example/no-content-type", FetchOptions{})
	if err != nil || sniffed.ContentType != "text/html" || !strings.Contains(string(sniffed.Body), "sniffed") {
		t.Fatalf("MIME sniff failed: document=%#v err=%v", sniffed, err)
	}
	if _, err := fetcher.Fetch(context.Background(), "https://public.example/article", FetchOptions{MaxBytes: 4}); !errors.Is(err, ErrResponseTooBig) {
		t.Fatalf("oversize response error = %v, want ErrResponseTooBig", err)
	}
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), "https://public.example/redirect", FetchOptions{}); !errors.Is(err, ErrUnsafeURL) || errors.Is(err, ErrFetchRequestFailed) {
		t.Fatalf("private redirect error = %v, want ErrUnsafeURL without retry marker", err)
	}
}

func TestFetchRequestFailureIsRetryableButDoesNotHidePolicyOrCancellation(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	fetcher := NewSafeFetcherWithResolver(1024, time.Second, resolver)
	dialError := errors.New("connection refused")
	fetcher.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, dialError })
	_, err := fetcher.Fetch(context.Background(), "https://public.example/article", FetchOptions{})
	if !errors.Is(err, ErrFetchRequestFailed) || !errors.Is(err, dialError) || err.Error() != "fetch request failed" {
		t.Fatalf("request error = %v, want opaque retryable network error preserving cause", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fetcher.Fetch(ctx, "https://public.example/article", FetchOptions{})
	if errors.Is(err, ErrFetchRequestFailed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want cancellation without retry marker", err)
	}

	fetcher.client.Timeout = 10 * time.Millisecond
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	_, err = fetcher.Fetch(context.Background(), "https://public.example/article", FetchOptions{})
	if !errors.Is(err, ErrFetchRequestFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetcher's bounded request timeout error = %v, want retry marker preserving timeout cause", err)
	}
}

func TestCanonicalURLAndSearchDomainFilter(t *testing.T) {
	if !matchesDomainAllowlist("docs.example.com", []string{"example.com"}) || matchesDomainAllowlist("notexample.com", []string{"example.com"}) {
		t.Fatal("domain allowlist matching failed")
	}
}
