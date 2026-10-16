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

type FetchOptions struct {
	MaxBytes int64
}

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
