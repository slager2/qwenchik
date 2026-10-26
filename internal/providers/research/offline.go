package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// OfflineCorpus has no network client, resolver or fallback. URLs identify
// imported documents; they are never opened, even when supplied directly.
type OfflineCorpus struct {
	documents []offlineDocument
	hash      string
}
type offlineDocument struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

func LoadOfflineCorpus(path string) (*OfflineCorpus, error) {
	f, err := openOfflineCorpus(path)
	if err != nil {
		return nil, errors.New("local corpus cannot be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("corpus must be a regular file of at most 16 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return nil, errors.New("corpus exceeds read limit")
	}
	var payload struct {
		Documents []offlineDocument `json:"documents"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&payload); err != nil {
		return nil, errors.New("invalid corpus JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("corpus contains trailing data")
	}
	if len(payload.Documents) < 1 || len(payload.Documents) > 128 {
		return nil, errors.New("corpus requires 1..128 documents")
	}
	seen := map[string]bool{}
	for _, doc := range payload.Documents {
		u, err := url.Parse(doc.URL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || len(doc.URL) > 2048 || seen[doc.URL] {
			return nil, errors.New("invalid or duplicate corpus URL")
		}
		if !utf8.ValidString(doc.Text) || strings.TrimSpace(doc.Text) == "" || len(doc.Text) > 256<<10 || len(doc.Title) > 1000 {
			return nil, errors.New("invalid corpus text/title limits")
		}
		seen[doc.URL] = true
	}
	hash := sha256.Sum256(raw)
	return &OfflineCorpus{documents: payload.Documents, hash: hex.EncodeToString(hash[:])}, nil
}
func (c *OfflineCorpus) SHA256() string { return c.hash }

// Imported text can retain an original .pdf URL without requiring live PDF
// extraction. This extractor intentionally exposes no network/PDF preflight.
func (c *OfflineCorpus) Extract(ctx context.Context, document FetchedDocument) (ExtractedDocument, error) {
	if err := c.ValidateURL(ctx, document.OriginalURL); err != nil {
		return ExtractedDocument{}, err
	}
	if document.Provider != "offline_corpus" || document.ContentType != "text/plain" {
		return ExtractedDocument{}, errors.New("offline extraction requires imported text")
	}
	result, err := NewHTMLExtractor(100000).Extract(ctx, document)
	if err != nil {
		return result, err
	}
	result.Method = "offline_imported_text"
	for _, doc := range c.documents {
		if doc.URL == document.OriginalURL {
			result.Title = doc.Title
			break
		}
	}
	return result, nil
}
func (c *OfflineCorpus) ValidateURL(ctx context.Context, raw string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, doc := range c.documents {
		if doc.URL == raw {
			return nil
		}
	}
	return errors.New("offline policy: URL is absent from the pinned corpus")
}
func (c *OfflineCorpus) Fetch(ctx context.Context, raw string, options FetchOptions) (FetchedDocument, error) {
	if err := c.ValidateURL(ctx, raw); err != nil {
		return FetchedDocument{}, err
	}
	for _, doc := range c.documents {
		if doc.URL == raw {
			if options.MaxBytes < 1 || int64(len(doc.Text)) > options.MaxBytes {
				return FetchedDocument{}, &FetchFailure{Err: errors.New("offline source exceeds fetch allowance"), Code: "source_too_large", Phase: "preflight"}
			}
			hash := sha256.Sum256([]byte(doc.Text))
			return FetchedDocument{Provider: "offline_corpus", OriginalURL: raw, FinalURL: raw, ContentType: "text/plain", HTTPStatus: 200, RetrievedAt: time.Now().UTC(), ContentSHA256: hex.EncodeToString(hash[:]), Body: []byte(doc.Text)}, nil
		}
	}
	return FetchedDocument{}, errors.New("offline source unavailable")
}
func (c *OfflineCorpus) Search(ctx context.Context, options SearchOptions) ([]SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tokens := strings.FieldsFunc(strings.ToLower(options.Query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	hits := []SearchHit{}
	for _, doc := range c.documents {
		u, _ := url.Parse(doc.URL)
		allowed := len(options.Domains) == 0
		for _, domain := range options.Domains {
			allowed = allowed || u.Hostname() == domain || strings.HasSuffix(u.Hostname(), "."+domain)
		}
		if !allowed {
			continue
		}
		score := 0
		text := strings.ToLower(doc.Title + " " + doc.Text)
		seen := map[string]bool{}
		for _, token := range tokens {
			if len([]rune(token)) >= 3 && !seen[token] && strings.Contains(text, token) {
				score++
				seen[token] = true
			}
		}
		if score == 0 {
			continue
		}
		snippet := []rune(doc.Text)
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		hits = append(hits, SearchHit{Provider: "offline_corpus", Query: options.Query, URL: doc.URL, Title: doc.Title, Snippet: string(snippet), Score: float64(score), Taint: TaintExternal})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	limit := options.MaxResults
	if limit < 1 || limit > 8 {
		limit = 8
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
func (c *OfflineCorpus) SearchWithDiagnostics(ctx context.Context, options SearchOptions) (SearchReport, error) {
	hits, err := c.Search(ctx, options)
	code := "search_ok"
	if len(hits) == 0 {
		code = "search_no_results"
	}
	return SearchReport{Hits: hits, Outcomes: []ProviderOutcome{{Provider: "offline_corpus", State: "available", Code: code, Message: "Pinned local corpus; no network requests", ResultCount: len(hits), CheckedAt: time.Now().UTC()}}}, err
}
