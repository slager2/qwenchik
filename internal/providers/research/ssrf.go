package research

import (
	"bytes"
	"compress/gzip"
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
	ErrDNSLookupFailed        = errors.New("DNS lookup failed")
)

type opaqueNetworkError struct {
	operation string
	cause     error
	retryable bool
}

func (e opaqueNetworkError) Error() string { return e.operation + " failed" }
func (e opaqueNetworkError) Unwrap() error { return e.cause }
func (e opaqueNetworkError) Is(target error) bool {
	return target == ErrFetchRequestFailed && e.operation == "fetch request" && e.retryable || target == ErrDNSLookupFailed && e.operation == "DNS lookup"
}

func (e opaqueNetworkError) OperationalValidationFailure() bool { return true }

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
	policy   FetchPolicy
	initErr  error
}

func NewSafeFetcher(maxBytes int64, timeout time.Duration) *SafeFetcher {
	return NewSafeFetcherWithPolicy(maxBytes, timeout, systemResolver{resolver: net.DefaultResolver}, nil)
}

func NewSafeFetcherWithResolver(maxBytes int64, timeout time.Duration, resolver IPResolver) *SafeFetcher {
	return NewSafeFetcherWithPolicy(maxBytes, timeout, resolver, nil)
}

func NewSafeFetcherWithPolicy(maxBytes int64, timeout time.Duration, resolver IPResolver, allowedDomains []string) *SafeFetcher {
	p := DefaultFetchPolicy()
	if maxBytes > 0 {
		p.SourceFetchBytes = min(maxBytes, p.SourceFetchBytes)
		p.PDFBytes = min(maxBytes, p.PDFBytes)
	}
	if timeout > 0 {
		p.SourceDownloadTimeout = timeout
		p.PDFDownloadTimeout = timeout
	}
	f, err := NewSafeFetcherWithFetchPolicy(p, resolver, allowedDomains)
	if err != nil {
		f = newSafeFetcher(p, resolver, allowedDomains)
		f.initErr = err
	}
	return f
}

func NewSafeFetcherWithFetchPolicy(policy FetchPolicy, resolver IPResolver, allowedDomains []string) (*SafeFetcher, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if _, err := createFetchTemp(context.Background(), policy, "", 0); err != nil {
		return nil, err
	}
	return newSafeFetcher(policy, resolver, allowedDomains), nil
}

func newSafeFetcher(policy FetchPolicy, resolver IPResolver, allowedDomains []string) *SafeFetcher {
	if resolver == nil {
		resolver = systemResolver{resolver: net.DefaultResolver}
	}
	fetcher := &SafeFetcher{resolver: resolver, maxBytes: policy.TaskFetchBudgetBytes, policy: policy, domains: append([]string(nil), allowedDomains...)}
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
		Timeout:   max(policy.PDFDownloadTimeout, policy.SourceDownloadTimeout),
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
	if ctx.Err() != nil {
		return ctx.Err()
	}
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
	if URLHasSensitiveData(parsed) {
		return fmt.Errorf("%w: URLs with credential-like data are blocked", ErrUnsafeURL)
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
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil || len(addresses) == 0 {
		return opaqueNetworkError{operation: "DNS lookup", cause: err}
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
	if f.initErr != nil {
		return FetchedDocument{}, &FetchFailure{Err: f.initErr, Code: "pdf_resource_exceeded", Phase: "fetch"}
	}
	parent := ctx
	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, max(f.policy.PDFDownloadTimeout, f.policy.SourceDownloadTimeout))
	defer deadlineCancel()
	ctx, cancel := context.WithCancelCause(deadlineCtx)
	defer cancel(nil)
	started := time.Now()
	sourceTimer := time.AfterFunc(f.policy.SourceDownloadTimeout, func() { cancel(context.DeadlineExceeded) })
	defer sourceTimer.Stop()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return FetchedDocument{}, errors.New("fetch request could not be created")
	}
	request.Header.Set("Accept", "text/html, text/plain, application/pdf, application/json, application/xhtml+xml;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "QwenchikLocalResearch/1.0")
	response, err := f.client.Do(request)
	if err != nil {
		retryable := parent.Err() == nil && !errors.Is(err, ErrUnsafeURL)
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = context.Cause(ctx)
		}
		return FetchedDocument{}, opaqueNetworkError{operation: "fetch request", cause: err, retryable: retryable}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return FetchedDocument{}, fmt.Errorf("%w: HTTP %d", ErrFetchSourceUnavailable, response.StatusCode)
	}
	contentType := response.Header.Get("Content-Type")
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if mediaType == "application/pdf" {
		sourceTimer.Stop()
		remaining := time.Until(started.Add(f.policy.PDFDownloadTimeout))
		if remaining <= 0 {
			cancel(context.DeadlineExceeded)
		} else {
			pdfTimer := time.AfterFunc(remaining, func() { cancel(context.DeadlineExceeded) })
			defer pdfTimer.Stop()
		}
	}
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	comparableLength := encoding == "" || encoding == "identity"
	var declared *int64
	if response.ContentLength >= 0 && !response.Uncompressed {
		n := response.ContentLength
		declared = &n
	}
	capFor := func(pdf bool) int64 {
		if pdf {
			return f.policy.PDFBytes
		}
		return f.policy.SourceFetchBytes
	}
	limitFor := func(cap int64) int64 {
		n := min(cap, f.policy.TaskFetchBudgetBytes)
		if options.MaxBytes > 0 {
			n = min(n, options.MaxBytes)
		}
		return n
	}
	policyCap := capFor(mediaType == "application/pdf")
	limit := limitFor(policyCap)
	var consumed int64
	failure := func(cause error, code string) (FetchedDocument, error) {
		kind := "source"
		if limit < policyCap {
			kind = "task_remaining"
		}
		if errors.Is(cause, ErrResponseTooBig) {
			code = "source_response_too_large"
			if mediaType == "application/pdf" {
				code = "pdf_source_too_large"
			}
			if kind == "task_remaining" && (!comparableLength || declared == nil || *declared <= policyCap) {
				code = "task_fetch_budget_exceeded"
			}
		}
		if parent.Err() != nil {
			cause = errors.Join(cause, parent.Err())
		} else if ctx.Err() != nil {
			cause = context.Cause(ctx)
			if errors.Is(cause, context.DeadlineExceeded) {
				code = "fetch_timeout"
			}
		}
		return FetchedDocument{}, &FetchFailure{Err: cause, Code: code, Phase: "fetch", DeclaredBytes: declared, EffectiveLimitBytes: limit, FetchedBytes: consumed, MIMEType: mediaType, LimitKind: kind}
	}
	if comparableLength && declared != nil && *declared > limit && mediaType == "application/pdf" {
		return failure(ErrResponseTooBig, "")
	}
	if comparableLength && declared != nil && *declared > limitFor(max(f.policy.PDFBytes, f.policy.SourceFetchBytes)) {
		return failure(ErrResponseTooBig, "")
	}
	var reader io.Reader = response.Body
	if encoding == "gzip" {
		gz, err := gzip.NewReader(response.Body)
		if err != nil {
			return failure(err, "source_unavailable")
		}
		defer gz.Close()
		reader = gz
	} else if encoding != "" && encoding != "identity" {
		return failure(ErrUnsupportedMIME, "unsupported_content")
	}
	reader = &fetchCountingReader{reader: reader, count: &consumed}
	preview, err := io.ReadAll(io.LimitReader(&fetchContextReader{ctx: ctx, reader: reader}, min(int64(512), limitFor(min(f.policy.SourceFetchBytes, f.policy.PDFBytes))+1)))
	if err != nil {
		return failure(err, "source_unavailable")
	}
	isPDF := bytes.HasPrefix(preview, []byte("%PDF-"))
	if isPDF {
		if mediaType != "application/pdf" {
			sourceTimer.Stop()
			remaining := time.Until(started.Add(f.policy.PDFDownloadTimeout))
			if remaining <= 0 {
				return failure(context.DeadlineExceeded, "fetch_timeout")
			}
			pdfTimer := time.AfterFunc(remaining, func() { cancel(context.DeadlineExceeded) })
			defer pdfTimer.Stop()
		}
		mediaType = "application/pdf"
	} else if mediaType == "application/pdf" {
		return failure(ErrUnsupportedMIME, "pdf_malformed")
	} else if parseErr != nil || mediaType == "" {
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(preview))
	}
	policyCap = capFor(isPDF)
	limit = limitFor(policyCap)
	if consumed > limit || comparableLength && declared != nil && *declared > limit {
		return failure(ErrResponseTooBig, "")
	}
	if !allowedMIME(mediaType) {
		return failure(ErrUnsupportedMIME, "unsupported_content")
	}
	if !isPDF {
		remaining := time.Until(started.Add(f.policy.SourceDownloadTimeout))
		if remaining <= 0 {
			return failure(context.DeadlineExceeded, "fetch_timeout")
		}
	}
	reader = io.MultiReader(bytes.NewReader(preview), reader)
	hash := sha256.New()
	var body []byte
	var lease *fetchTemp
	if isPDF {
		lease, err = createFetchTemp(ctx, f.policy, options.TaskID, limit)
		if err != nil {
			return failure(err, "pdf_resource_exceeded")
		}
		defer func() {
			if lease != nil {
				lease.cleanup()
			}
		}()
		_, err = io.Copy(io.MultiWriter(lease.file, hash), io.LimitReader(&fetchContextReader{ctx: ctx, reader: reader}, limit+1))
	} else {
		body, err = io.ReadAll(io.LimitReader(&fetchContextReader{ctx: ctx, reader: reader}, limit+1))
		_, _ = hash.Write(body)
	}
	if err != nil {
		return failure(err, "source_unavailable")
	}
	if consumed > limit {
		return failure(ErrResponseTooBig, "")
	}
	if ctx.Err() != nil {
		return failure(ctx.Err(), "fetch_timeout")
	}
	finalURL := rawURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	document := FetchedDocument{
		Provider: "direct_http", OriginalURL: rawURL, FinalURL: finalURL, ContentType: mediaType,
		HTTPStatus: response.StatusCode, RetrievedAt: time.Now().UTC(),
		ContentSHA256: hex.EncodeToString(hash.Sum(nil)), Body: body, SizeBytes: consumed, DeclaredBytes: declared, EffectiveLimitBytes: limit,
	}
	if lease != nil {
		if err := lease.file.Truncate(consumed); err != nil {
			return failure(err, "pdf_resource_exceeded")
		}
		if _, err := lease.file.Seek(0, io.SeekStart); err != nil {
			return failure(err, "pdf_resource_exceeded")
		}
		document.File = lease.file
		document.cleanup = lease.cleanup
		lease = nil
	}
	return document, nil
}

type fetchContextReader struct {
	ctx    context.Context
	reader io.Reader
}

type fetchCountingReader struct {
	reader io.Reader
	count  *int64
}

func (r *fetchCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	*r.count += int64(n)
	return n, err
}

func (r *fetchContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, context.Cause(r.ctx)
	}
	return r.reader.Read(p)
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
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if err != nil || len(resolved) == 0 {
			return nil, opaqueNetworkError{operation: "DNS lookup", cause: err}
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

func URLHasSensitiveData(parsed *url.URL) bool {
	if parsed == nil || parsed.User != nil || hasSensitiveQuery(parsed) {
		return true
	}
	for key, values := range parsed.Query() {
		key = strings.ToLower(key)
		for _, marker := range []string{"token", "secret", "password", "credential", "signature", "authorization", "api_key", "apikey"} {
			if strings.Contains(key, marker) {
				return true
			}
		}
		for _, value := range values {
			lower := strings.ToLower(value)
			for _, marker := range []string{"bearer ", "token=", "secret=", "password=", "api_key=", "apikey=", "signature="} {
				if strings.Contains(lower, marker) {
					return true
				}
			}
			if strings.HasPrefix(lower, "eyj") && strings.Count(lower, ".") >= 2 {
				return true
			}
			if nested, err := url.Parse(value); err == nil && nested.Host != "" && (nested.User != nil || hasSensitiveQuery(nested)) {
				return true
			}
		}
	}
	parts := strings.FieldsFunc(strings.ToLower(parsed.Path), func(r rune) bool { return r == '/' || r == '=' || r == ';' })
	for _, part := range parts {
		switch part {
		case "token", "access_token", "api_key", "apikey", "secret", "password", "credentials", "bearer", "session":
			return true
		}
		if len(part) >= 64 && !strings.Contains(part, "-") || strings.HasPrefix(part, "eyj") && strings.Count(part, ".") >= 2 {
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
