package research

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"golang.org/x/net/html"
)

var (
	ErrPDFExtractorUnavailable = errors.New("isolated PDF extraction is unavailable")
	ErrPDFExtractionFailed     = errors.New("PDF extraction failed")
	ErrPDFEncrypted            = errors.New("encrypted PDFs are not supported")
	ErrPDFTooManyPages         = errors.New("PDF exceeds the 20 page limit")
	ErrPDFTooLarge             = errors.New("PDF exceeds an extraction size limit")
	ErrPDFNoText               = errors.New("PDF contains no extractable text")
)

type HTMLExtractor struct {
	maxChars     int
	pdfExtractor IsolatedPDFExtractor
}

type IsolatedPDFExtractor interface {
	Extract(context.Context, []byte) (pdfextract.Result, error)
}

func NewHTMLExtractor(maxChars int) *HTMLExtractor {
	pdfExtractor, _ := pdfextract.New("", nil)
	return NewHTMLExtractorWithPDF(maxChars, pdfExtractor)
}

func NewHTMLExtractorWithPDF(maxChars int, pdfExtractor IsolatedPDFExtractor) *HTMLExtractor {
	if maxChars < 1 {
		maxChars = 100_000
	}
	return &HTMLExtractor{maxChars: maxChars, pdfExtractor: pdfExtractor}
}

func (e *HTMLExtractor) Extract(ctx context.Context, document FetchedDocument) (ExtractedDocument, error) {
	if err := ctx.Err(); err != nil {
		return ExtractedDocument{}, err
	}
	switch document.ContentType {
	case "application/pdf":
		if e.pdfExtractor == nil {
			return ExtractedDocument{}, ErrPDFExtractorUnavailable
		}
		result, err := e.pdfExtractor.Extract(ctx, document.Body)
		if errors.Is(err, pdfextract.ErrUnavailable) {
			return ExtractedDocument{}, ErrPDFExtractorUnavailable
		}
		if err != nil {
			switch {
			case errors.Is(err, pdfextract.ErrNoText):
				return ExtractedDocument{}, errors.Join(ErrPDFNoText, err)
			case errors.Is(err, pdfextract.ErrEncrypted):
				return ExtractedDocument{}, errors.Join(ErrPDFEncrypted, err)
			case errors.Is(err, pdfextract.ErrTooManyPages):
				return ExtractedDocument{}, errors.Join(ErrPDFTooManyPages, err)
			case errors.Is(err, pdfextract.ErrInputTooLarge), errors.Is(err, pdfextract.ErrTextTooLarge), errors.Is(err, pdfextract.ErrOutputExceeded):
				return ExtractedDocument{}, errors.Join(ErrPDFTooLarge, err)
			default:
				return ExtractedDocument{}, errors.Join(ErrPDFExtractionFailed, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return ExtractedDocument{}, err
		}
		if result.PageCount < 1 {
			return ExtractedDocument{}, errors.Join(ErrPDFExtractionFailed, pdfextract.ErrMalformed)
		}
		if result.PageCount > pdfextract.MaxPages {
			return ExtractedDocument{}, errors.Join(ErrPDFTooManyPages, pdfextract.ErrTooManyPages)
		}
		if utf8.RuneCountInString(result.Text) > pdfextract.MaxTextRunes {
			return ExtractedDocument{}, errors.Join(ErrPDFTooLarge, pdfextract.ErrTextTooLarge)
		}
		text := normalizeExtractedText(result.Text, pdfextract.MaxTextRunes)
		if text == "" {
			return ExtractedDocument{}, ErrPDFNoText
		}
		flags := []string{}
		method := result.Method
		if method == "" {
			method = "isolated_poppler_pdfinfo_pdftotext"
		}
		return ExtractedDocument{
			Text: text, Method: method, ContentSHA256: document.ContentSHA256,
			Taint: TaintExternal, QualityFlags: flags, PageCount: result.PageCount,
		}, nil
	case "text/plain", "application/json":
		text := normalizeExtractedText(string(document.Body), e.maxChars)
		flags := []string{}
		if text == "" {
			flags = append(flags, "empty_after_extraction")
		}
		return ExtractedDocument{Text: text, Method: "local_text_extractor", ContentSHA256: document.ContentSHA256, Taint: TaintExternal, QualityFlags: flags}, nil
	case "text/html", "application/xhtml+xml":
	default:
		return ExtractedDocument{}, fmt.Errorf("HTML extractor does not support %q", document.ContentType)
	}
	root, err := html.Parse(strings.NewReader(string(document.Body)))
	if err != nil {
		return ExtractedDocument{}, fmt.Errorf("parse HTML: %w", err)
	}
	title, cleanText := extractSmartHTML(root, e.maxChars)
	flags := []string{}
	if cleanText == "" {
		flags = append(flags, "empty_after_extraction")
	}
	return ExtractedDocument{
		Title:         trimRunes(title, 500),
		Text:          cleanText,
		Method:        "local_html_parser",
		ContentSHA256: document.ContentSHA256,
		Taint:         TaintExternal,
		QualityFlags:  flags,
	}, nil
}

func isDiscardedTag(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "svg", "canvas", "iframe", "form", "nav", "header", "footer", "aside", "template", "head":
		return true
	default:
		return false
	}
}

func isBlockTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "br", "dd", "div", "dl", "dt", "h1", "h2", "h3", "h4", "h5", "h6", "li", "main", "ol", "p", "pre", "section", "table", "td", "th", "tr", "ul":
		return true
	default:
		return false
	}
}

func normalizeExtractedText(value string, maxChars int) string {
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	clean := make([]string, 0, len(lines))
	remaining := maxChars
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || remaining <= 0 {
			continue
		}
		line = trimRunes(line, remaining)
		remaining -= utf8.RuneCountInString(line)
		clean = append(clean, line)
	}
	return strings.TrimSpace(strings.Join(clean, "\n"))
}

func trimRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return strings.TrimSpace(value)
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit]))
}
