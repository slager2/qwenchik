package research

import (
	"context"
	"os"
	"time"
)

const TaintExternal = "untrusted_external_content"

type SearchOptions struct {
	Query            string
	Language         string
	Domains          []string
	PreferredDomains []string
	Recency          string
	MaxResults       int
	SafeSearch       bool
}

type SearchHit struct {
	Provider    string    `json:"provider"`
	Query       string    `json:"query"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Snippet     string    `json:"snippet"`
	Score       float64   `json:"score,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	Taint       string    `json:"taint"`
}

type SearchProvider interface {
	Search(context.Context, SearchOptions) ([]SearchHit, error)
}

type ProviderOutcome struct {
	Provider        string    `json:"provider"`
	State           string    `json:"state"`
	Code            string    `json:"code"`
	Message         string    `json:"message"`
	Retryable       bool      `json:"retryable"`
	HTTPStatus      int       `json:"http_status"`
	LatencyMS       int64     `json:"latency_ms"`
	CheckedAt       time.Time `json:"checked_at"`
	ResultCount     int       `json:"result_count"`
	DistinctDomains int       `json:"distinct_domains"`
	BodySize        int64     `json:"body_size,omitempty"`
	ContentType     string    `json:"content_type,omitempty"`
	ParserVersion   string    `json:"parser_version,omitempty"`
}

type SearchReport struct {
	Hits     []SearchHit       `json:"hits"`
	Outcomes []ProviderOutcome `json:"outcomes"`
}

type SearchHitObservation struct {
	ID               string    `json:"id"`
	Fingerprint      string    `json:"fingerprint"`
	URL              string    `json:"url,omitempty"`
	Origin           string    `json:"origin,omitempty"`
	Title            string    `json:"title,omitempty"`
	Snippet          string    `json:"snippet,omitempty"`
	Provider         string    `json:"provider"`
	QueryFingerprint string    `json:"query_fingerprint"`
	Status           string    `json:"status"`
	ErrorCode        string    `json:"error_code,omitempty"`
	Retryable        bool      `json:"retryable"`
	Time             time.Time `json:"time"`
}

type DiagnosticSearchProvider interface {
	SearchWithDiagnostics(context.Context, SearchOptions) (SearchReport, error)
}

type FetchOptions struct {
	MaxBytes     int64
	TaskID       string
	PDFPreflight func(context.Context) error
}

type FetchFailure struct {
	Err                 error
	FetchedBytes        int64
	Code                string
	Phase               string
	DeclaredBytes       *int64
	EffectiveLimitBytes int64
	MIMEType            string
	LimitKind           string
}

func (e *FetchFailure) Error() string { return e.Err.Error() }
func (e *FetchFailure) Unwrap() error { return e.Err }

type FetchedDocument struct {
	Provider            string
	OriginalURL         string
	FinalURL            string
	ContentType         string
	Language            string
	HTTPStatus          int
	RetrievedAt         time.Time
	ContentSHA256       string
	Body                []byte
	File                *os.File
	SizeBytes           int64
	DeclaredBytes       *int64
	EffectiveLimitBytes int64
	cleanup             func()
}

func (d FetchedDocument) ByteSize() int64 {
	if d.File != nil {
		return d.SizeBytes
	}
	return int64(len(d.Body))
}

func (d FetchedDocument) Cleanup() {
	if d.cleanup != nil {
		d.cleanup()
		return
	}
	if d.File != nil {
		name := d.File.Name()
		_ = d.File.Close()
		_ = os.Remove(name)
	}
}

type Fetcher interface {
	ValidateURL(context.Context, string) error
	Fetch(context.Context, string, FetchOptions) (FetchedDocument, error)
}

type ExtractedDocument struct {
	Title         string
	Text          string
	Method        string
	ContentSHA256 string
	Taint         string
	QualityFlags  []string
	PageCount     int
}

type Extractor interface {
	Extract(context.Context, FetchedDocument) (ExtractedDocument, error)
}
