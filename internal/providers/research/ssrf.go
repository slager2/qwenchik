package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnsafeURL              = errors.New("URL is not allowed by outbound network policy")
	ErrUnsupportedMIME        = errors.New("unsupported content type")
	ErrResponseTooBig         = errors.New("response exceeds configured byte limit")
	ErrFetchSourceUnavailable = errors.New("fetched source returned a non-success HTTP status")
	ErrFetchRequestFailed     = errors.New("fetch request failed")
)

type opaqueNetworkError struct {
	operation string
	cause     error
	retryable bool
}

func (e opaqueNetworkError) Error() string { return e.operation + " failed" }
func (e opaqueNetworkError) Unwrap() error { return e.cause }
func (e opaqueNetworkError) Is(target error) bool {
	return target == ErrFetchRequestFailed && e.operation == "fetch request" && e.retryable
}

type IPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type systemResolver struct{ resolver *net.Resolver }

func (r systemResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return r.resolver.LookupIPAddr(ctx, host)
}

type SafeFetcher struct {
	resolver IPResolver
	client   *http.Client
	maxBytes int64
	domains  []string
}

type replayBody struct {
	io.Reader
	io.Closer
}

func NewSafeFetcher(maxBytes int64, timeout time.Duration) *SafeFetcher {
	return NewSafeFetcherWithPolicy(maxBytes, timeout, systemResolver{resolver: net.DefaultResolver}, nil)
}

func NewSafeFetcherWithResolver(maxBytes int64, timeout time.Duration, resolver IPResolver) *SafeFetcher {
	return NewSafeFetcherWithPolicy(maxBytes, timeout, resolver, nil)
}

func NewSafeFetcherWithPolicy(maxBytes int64, timeout time.Duration, resolver IPResolver, allowedDomains []string) *SafeFetcher {
	if maxBytes < 1 {
		maxBytes = 5_000_000
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if resolver == nil {
		resolver = systemResolver{resolver: net.DefaultResolver}
	}
	fetcher := &SafeFetcher{resolver: resolver, maxBytes: maxBytes, domains: append([]string(nil), allowedDomains...)}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           fetcher.dialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          10,
	}
	fetcher.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return fetcher.ValidateURL(request.Context(), request.URL.String())
		},
	}
	return fetcher
}

func (f *SafeFetcher) ValidateURL(ctx context.Context, rawURL string) error {
	if len(rawURL) > 4096 {
		return fmt.Errorf("%w: URL exceeds 4096 characters", ErrUnsafeURL)
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%w: malformed URL", ErrUnsafeURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: only http and https are supported", ErrUnsafeURL)
	}
	host := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(parsed.Hostname()), "."), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("%w: local hostnames are blocked", ErrUnsafeURL)
	}
	if len(f.domains) > 0 && !matchesDomainAllowlist(host, f.domains) {
		return fmt.Errorf("%w: host is outside the configured domain allowlist", ErrUnsafeURL)
	}
	if hasSensitiveQuery(parsed) {
		return fmt.Errorf("%w: URLs with credential-like query parameters are blocked", ErrUnsafeURL)
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("%w: invalid port", ErrUnsafeURL)
		}
		if (parsed.Scheme == "http" && portNumber != 80) || (parsed.Scheme == "https" && portNumber != 443) {
			return fmt.Errorf("%w: only standard HTTP(S) ports are allowed", ErrUnsafeURL)
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return fmt.Errorf("%w: non-public IP address", ErrUnsafeURL)
		}
		return nil
	}
	addresses, err := f.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("%w: hostname did not resolve", ErrUnsafeURL)
	}
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return fmt.Errorf("%w: hostname resolves to a non-public address", ErrUnsafeURL)
		}
	}
	return nil
}

func (f *SafeFetcher) Fetch(ctx context.Context, rawURL string, options FetchOptions) (FetchedDocument, error) {
	if err := f.ValidateURL(ctx, rawURL); err != nil {
		return FetchedDocument{}, err
	}
	limit := f.maxBytes
	if options.MaxBytes > 0 && options.MaxBytes < limit {
		limit = options.MaxBytes
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return FetchedDocument{}, errors.New("fetch request could not be created")
	}
	request.Header.Set("Accept", "text/html, text/plain, application/pdf, application/json, application/xhtml+xml;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "QwenchikLocalResearch/1.0")
	response, err := f.client.Do(request)
	if err != nil {
		retryable := ctx.Err() == nil && !errors.Is(err, ErrUnsafeURL)
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(err, ctxErr)
		}
		return FetchedDocument{}, opaqueNetworkError{operation: "fetch request", cause: err, retryable: retryable}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return FetchedDocument{}, fmt.Errorf("%w: HTTP %d", ErrFetchSourceUnavailable, response.StatusCode)
	}
	if response.ContentLength > limit {
		return FetchedDocument{}, ErrResponseTooBig
	}
	contentType := response.Header.Get("Content-Type")
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || mediaType == "" {
		preview, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(preview))
		originalBody := response.Body
		response.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(preview), originalBody), Closer: originalBody}
	}
	if !allowedMIME(mediaType) {
		return FetchedDocument{}, fmt.Errorf("%w: %s", ErrUnsupportedMIME, mediaType)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return FetchedDocument{}, fmt.Errorf("read fetched document: %w", err)
	}
	if int64(len(body)) > limit {
		return FetchedDocument{}, ErrResponseTooBig
	}
	hash := sha256.Sum256(body)
	finalURL := rawURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return FetchedDocument{
		Provider: "direct_http", OriginalURL: rawURL, FinalURL: finalURL, ContentType: mediaType,
		HTTPStatus: response.StatusCode, RetrievedAt: time.Now().UTC(),
		ContentSHA256: hex.EncodeToString(hash[:]), Body: body,
	}, nil
}

func (f *SafeFetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid dial address: %w", ErrUnsafeURL)
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := f.resolver.LookupIPAddr(ctx, host)
		if err != nil || len(resolved) == 0 {
			return nil, fmt.Errorf("resolve outbound host: %w", ErrUnsafeURL)
		}
		for _, item := range resolved {
			ips = append(ips, item.IP)
		}
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("DNS rebinding or non-public target blocked: %w", ErrUnsafeURL)
		}
	}
	var lastErr error
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func allowedMIME(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "text/html", "application/xhtml+xml", "text/plain", "application/json", "application/pdf":
		return true
	default:
		return false
	}
}

func hasSensitiveQuery(parsed *url.URL) bool {
	for key := range parsed.Query() {
		switch strings.ToLower(key) {
		case "token", "access_token", "auth_token", "api_key", "apikey", "key", "auth", "authorization", "password", "passwd", "secret", "signature", "session", "sid", "credential", "client_secret", "x-amz-credential", "x-amz-signature":
			return true
		}
	}
	return false
}

func isPublicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	deniedPrefixes := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
	}
	for _, prefix := range deniedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
