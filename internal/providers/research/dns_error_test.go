package research

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestDNSFailuresPreserveClassificationAndDoNotImplySSRF(t *testing.T) {
	for _, cause := range []error{&net.DNSError{Err: "private-resolver-detail", Name: "public.example", IsNotFound: true}, errors.New("private-resolver-detail"), nil} {
		policy := DefaultFetchPolicy()
		policy.TempRoot = filepath.Join(t.TempDir(), "fetch-temp")
		fetcher, err := NewSafeFetcherWithFetchPolicy(policy, resolverFunc(func(context.Context, string) ([]net.IPAddr, error) { return nil, cause }), nil)
		if err != nil {
			t.Fatal(err)
		}
		validateErr := fetcher.ValidateURL(context.Background(), "https://public.example/")
		_, dialErr := fetcher.dialContext(context.Background(), "tcp", "public.example:443")
		for _, err := range []error{validateErr, dialErr} {
			if !errors.Is(err, ErrDNSLookupFailed) || errors.Is(err, ErrUnsafeURL) || strings.Contains(err.Error(), "private-resolver-detail") {
				t.Fatalf("DNS error misclassified or unredacted: %v", err)
			}
			if cause != nil && !errors.Is(err, cause) {
				t.Fatalf("DNS cause lost: %v", err)
			}
		}
	}
}

func TestDNSCancellationRemainsCancellation(t *testing.T) {
	policy := DefaultFetchPolicy()
	policy.TempRoot = filepath.Join(t.TempDir(), "fetch-temp")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetcher, err := NewSafeFetcherWithFetchPolicy(policy, resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		cancel()
		return nil, errors.New("lookup interrupted")
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fetcher.ValidateURL(ctx, "https://public.example/"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrDNSLookupFailed) {
		t.Fatalf("DNS cancellation lost: %v", err)
	}
}
