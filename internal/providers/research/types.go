package research

import (
	"context"
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

type DiagnosticSearchProvider interface {
	SearchWithDiagnostics(context.Context, SearchOptions) (SearchReport, error)
}

type FetchOptions struct {
	MaxBytes int64
}

type FetchFailure struct {
	Err          error
	FetchedBytes int64
}

func (e *FetchFailure) Error() string { return e.Err.Error() }
func (e *FetchFailure) Unwrap() error { return e.Err }

type FetchedDocument struct {
	Provider      string
	OriginalURL   string
	FinalURL      string
	ContentType   string
	Language      string
	HTTPStatus    int
	RetrievedAt   time.Time
	ContentSHA256 string
	Body          []byte
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
