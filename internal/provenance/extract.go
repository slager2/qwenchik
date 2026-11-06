package provenance

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
)

const (
	// ExtractEvidenceProvider, ExtractEvidenceMethod and
	// ExtractPDFEvidenceMethod mark evidence whose text agentSearcher's
	// /extract endpoint read from the page (HTML) or from the PDF text
	// layer. Unlike agentsearcher_passages this is the whole cleaned page
	// text, a normal fetched source.
	ExtractEvidenceProvider  = "agentsearcher"
	ExtractEvidenceMethod    = "agentsearcher_extract"
	ExtractPDFEvidenceMethod = "agentsearcher_extract_pdf"
	// ExtractExcerptRunes is the stored excerpt bound for extracted pages:
	// room for the best two or three ranked chunks (about 600 characters
	// each) with their surrounding context, still one contiguous span.
	ExtractExcerptRunes = 6000
	// DefaultExcerptRunes is the excerpt bound of directly fetched and
	// passage evidence.
	DefaultExcerptRunes = 4000
)

// IsExtractEvidence reports whether evidence text came from agentSearcher's
// /extract endpoint.
func IsExtractEvidence(evidence domain.Evidence) bool {
	return evidence.ExtractionMethod == ExtractEvidenceMethod || evidence.ExtractionMethod == ExtractPDFEvidenceMethod
}

// IsPDFEvidence reports whether evidence text is a PDF text layer, read by
// agentSearcher or by the local sandboxed PDF extractor.
func IsPDFEvidence(evidence domain.Evidence) bool {
	method := strings.ToLower(evidence.ExtractionMethod)
	return evidence.ExtractionMethod == ExtractPDFEvidenceMethod || strings.Contains(method, "pdf")
}

// ExtractEvidenceText is the stored text artifact of an agentSearcher
// extraction. It is the page text itself; only when none of the ranked
// chunks occur in it (the service cuts long pages at MAX_RAW_CHARS, so the
// best chunks may lie past the cut) are the chunks appended after a
// separator line, so the excerpt can still be a verbatim span of the stored
// text.
func ExtractEvidenceText(text string, chunks []string) string {
	if len(chunks) == 0 {
		return text
	}
	if _, _, _, ok := SelectPassageExcerptWithSpan(text, chunks, ExtractExcerptRunes); ok {
		return text
	}
	joined := JoinPassages(chunks)
	if joined == "" {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n[...]\n\n" + joined
}

// ExtractEvidenceInput describes one agentSearcher extraction to turn into
// evidence.
type ExtractEvidenceInput struct {
	TaskID, Query, URL, Title string
	// Text is the stored text artifact (ExtractEvidenceText).
	Text string
	// Chunks are agentSearcher's query-ranked chunks of the page; Passages
	// are the search passages of the same URL, used when no chunk matches.
	Chunks, Passages []string
	PDF              bool
	// ContentSHA256 is the hash of the stored raw extract record.
	ContentSHA256                 string
	RetrievedAt                   time.Time
	RawArtifactID, TextArtifactID string
}

// BuildExtractEvidence builds evidence from text agentSearcher extracted from
// a page. The excerpt is one verbatim contiguous span of input.Text, located
// by the ranked chunks, then by the search passages, then by the lexical
// query selector, at most ExtractExcerptRunes long. HTTPStatus 200 records
// that agentSearcher retrieved the page.
func BuildExtractEvidence(input ExtractEvidenceInput) (domain.Evidence, error) {
	canonical, err := CanonicalURL(input.URL)
	if err != nil {
		return domain.Evidence{}, err
	}
	if input.TaskID == "" || input.ContentSHA256 == "" || strings.TrimSpace(input.Text) == "" {
		return domain.Evidence{}, errors.New("extract evidence requires a task ID, content hash and text")
	}
	retrievedAt := input.RetrievedAt
	if retrievedAt.IsZero() {
		retrievedAt = time.Now().UTC()
	}
	excerpt, start, end, matched := SelectPassageExcerptWithSpan(input.Text, input.Chunks, ExtractExcerptRunes)
	if !matched {
		excerpt, start, end, matched = SelectPassageExcerptWithSpan(input.Text, input.Passages, ExtractExcerptRunes)
	}
	if !matched {
		excerpt, start, end = SelectExcerptWithSpan(input.Text, input.Query, ExtractExcerptRunes)
	}
	quotes := []domain.QuoteSpan{}
	if excerpt != "" {
		quotes = append(quotes, domain.QuoteSpan{Start: start, End: end, Text: excerpt, CoordinateSpace: "extracted_text_runes"})
	}
	method := ExtractEvidenceMethod
	if input.PDF {
		method = ExtractPDFEvidenceMethod
	}
	return domain.Evidence{
		TaskID: input.TaskID, Provider: ExtractEvidenceProvider, OriginalQuery: input.Query,
		OriginalURL: input.URL, CanonicalURL: canonical, Title: extractTitle(input), RetrievedAt: retrievedAt, HTTPStatus: 200,
		ContentSHA256: input.ContentSHA256, ExtractionMethod: method,
		RawArtifactID: input.RawArtifactID, TextArtifactID: input.TextArtifactID,
		Excerpt: excerpt, QuoteSpans: quotes, QualityFlags: []string{}, Taint: research.TaintExternal,
	}, nil
}

// extractTitle is the search result title when known, else the first text
// line of an HTML page or the PDF file name.
func extractTitle(input ExtractEvidenceInput) string {
	if title := strings.TrimSpace(input.Title); title != "" {
		return boundedTitle(title)
	}
	if !input.PDF {
		for _, line := range strings.Split(input.Text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return boundedTitle(line)
			}
		}
	}
	if parsed, err := url.Parse(input.URL); err == nil {
		segments := strings.Split(strings.TrimRight(parsed.Path, "/"), "/")
		if name := segments[len(segments)-1]; name != "" {
			return boundedTitle(name)
		}
		return parsed.Hostname()
	}
	return ""
}

func boundedTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(title) > 200 {
		title = string([]rune(title)[:200])
	}
	return title
}
