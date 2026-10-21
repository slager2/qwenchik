package research

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func TestValidateURLPreservesDNSCancellationAndTimeout(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		fetcher := NewSafeFetcherWithResolver(1024, time.Second, resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return nil, cause
		}))
		err := fetcher.ValidateURL(context.Background(), "https://example.org/source")
		if !errors.Is(err, cause) || errors.Is(err, ErrUnsafeURL) {
			t.Fatalf("DNS cause=%v returned=%v", cause, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewSafeFetcher(1024, time.Second).ValidateURL(ctx, "https://8.8.8.8/source"); !errors.Is(err, context.Canceled) {
		t.Fatalf("literal IP validation ignored parent cancellation: %v", err)
	}
}

func (f resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type bodyReaderFunc func([]byte) (int, error)

func (f bodyReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestFetchAccountsForPartialBodyReadFailures(t *testing.T) {
	for _, contentType := range []string{"text/plain", ""} {
		for _, test := range []struct {
			name   string
			cause  error
			cancel bool
		}{
			{name: "unexpected EOF", cause: io.ErrUnexpectedEOF},
			{name: "timeout", cause: context.DeadlineExceeded},
			{name: "canceled read", cause: io.ErrClosedPipe, cancel: true},
		} {
			t.Run(contentType+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fetcher := NewSafeFetcher(1024, time.Second)
				body := "partial response"
				fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
					reader := bodyReaderFunc(func(p []byte) (int, error) {
						if test.cancel {
							cancel()
						}
						return copy(p, body), test.cause
					})
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}},
						ContentLength: -1, Body: io.NopCloser(reader), Request: request,
					}, nil
				})
				document, err := fetcher.Fetch(ctx, "https://93.184.216.34/article", FetchOptions{})
				if !reflect.DeepEqual(document, FetchedDocument{}) {
					t.Fatalf("failed read returned partial document: %#v", document)
				}
				var failure *FetchFailure
				if !errors.As(err, &failure) || failure.FetchedBytes != int64(len(body)) {
					t.Fatalf("read error = %v, want FetchFailure with %d bytes", err, len(body))
				}
				if !errors.Is(err, test.cause) {
					t.Fatalf("read error = %v, want underlying cause %v", err, test.cause)
				}
				if test.cancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("read error = %v, want context cancellation", err)
				}
				if test.cause == context.DeadlineExceeded {
					var timeout net.Error
					if !errors.As(err, &timeout) || !timeout.Timeout() {
						t.Fatalf("read error = %v, want net.Error timeout", err)
					}
				}
			})
		}
	}
}

func TestFetchAccountsForOversizeBody(t *testing.T) {
	for _, test := range []struct {
		name          string
		contentType   string
		limit         int64
		contentLength int64
	}{
		{name: "streamed text", contentType: "text/plain", limit: 32, contentLength: -1},
		{name: "streamed sniffed text", contentType: "", limit: 32, contentLength: -1},
		{name: "declared text", contentType: "text/plain", limit: 32, contentLength: 33},
		{name: "streamed PDF", contentType: "application/pdf", limit: 128, contentLength: -1},
		{name: "declared PDF", contentType: "application/pdf", limit: 128, contentLength: 129},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fetcher := NewSafeFetcher(20<<20, time.Second)
			payload := strings.Repeat("x", int(test.limit+2))
			if test.contentType == "application/pdf" {
				payload = "%PDF-" + payload[5:]
			}
			body := strings.NewReader(payload)
			fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{test.contentType}},
					ContentLength: test.contentLength, Body: io.NopCloser(body), Request: request,
				}, nil
			})
			options := FetchOptions{MaxBytes: test.limit}
			document, err := fetcher.Fetch(context.Background(), "https://93.184.216.34/article", options)
			if !reflect.DeepEqual(document, FetchedDocument{}) || !errors.Is(err, ErrResponseTooBig) {
				t.Fatalf("oversize fetch = %#v, %v, want empty document and ErrResponseTooBig", document, err)
			}
			consumed := test.limit + 2 - int64(body.Len())
			var failure *FetchFailure
			if test.contentLength < 0 {
				if consumed != test.limit+1 || !errors.As(err, &failure) || failure.FetchedBytes != consumed {
					t.Fatalf("oversize error = %v, consumed %d, want FetchFailure with %d bytes", err, consumed, test.limit+1)
				}
			} else if consumed != 0 || (errors.As(err, &failure) && failure.FetchedBytes != 0) {
				t.Fatalf("ContentLength rejection consumed or charged bytes: consumed=%d error=%v", consumed, err)
			}
		})
	}
}

func TestFetchChargesSniffedBytesWhenMIMERejectsDocument(t *testing.T) {
	for _, test := range []struct {
		name          string
		body          string
		contentLength int64
		cause         error
	}{
		{name: "unsupported image", body: "GIF89aimage", contentLength: -1, cause: ErrUnsupportedMIME},
		{name: "declared oversized PDF", body: "%PDF-1.7", contentLength: (64 << 20) + 1, cause: ErrResponseTooBig},
	} {
		t.Run(test.name, func(t *testing.T) {
			fetcher := NewSafeFetcher(20<<20, time.Second)
			fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: test.contentLength,
					Body: io.NopCloser(strings.NewReader(test.body)), Request: request,
				}, nil
			})
			document, err := fetcher.Fetch(context.Background(), "https://93.184.216.34/document", FetchOptions{})
			var failure *FetchFailure
			wantBytes := int64(len(test.body))
			if test.contentLength > 0 {
				wantBytes = 0
			}
			if !reflect.DeepEqual(document, FetchedDocument{}) || !errors.Is(err, test.cause) || !errors.As(err, &failure) || failure.FetchedBytes != wantBytes {
				t.Fatalf("sniffed rejection returned document=%#v error=%v; want %v charging %d bytes", document, err, test.cause, len(test.body))
			}
		})
	}
}

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

func TestPDFFetchHasPolicyCeilingEvenWhenTaskBudgetIsLarger(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	fetcher := NewSafeFetcherWithResolver(128<<20, time.Second, resolver)
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/pdf"}},
			ContentLength: (64 << 20) + 1,
			Body:          io.NopCloser(strings.NewReader("%PDF-1.7")), Request: request,
		}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), "https://public.example/report.pdf", FetchOptions{MaxBytes: 128 << 20}); !errors.Is(err, ErrResponseTooBig) {
		t.Fatalf("oversized PDF fetch error = %v, want ErrResponseTooBig", err)
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
