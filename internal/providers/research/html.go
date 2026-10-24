package research

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"golang.org/x/net/html"
)

var (
	ErrExtractionEmpty         = errors.New("empty_after_extraction")
	ErrExtractionChallenge     = errors.New("challenge_detected")
	ErrPDFExtractorUnavailable = errors.New("isolated PDF extraction is unavailable")
	ErrPDFExtractionFailed     = errors.New("PDF extraction failed")
	ErrPDFEncrypted            = errors.New("encrypted PDFs are not supported")
	ErrPDFTooManyPages         = errors.New("PDF exceeds the configured page limit")
	ErrPDFTooLarge             = errors.New("PDF exceeds an extraction size limit")
	ErrPDFNoText               = errors.New("PDF contains no extractable text")
)

type HTMLExtractor struct {
	maxChars     int
	pdfExtractor IsolatedPDFExtractor
	pdfPolicy    pdfextract.Policy
}

type IsolatedPDFExtractor interface {
	Extract(context.Context, []byte) (pdfextract.Result, error)
}

type FilePDFExtractor interface {
	ExtractFile(context.Context, *os.File) (pdfextract.Result, error)
}

type PDFExtractorChecker interface {
	Check(context.Context) error
}

func NewHTMLExtractor(maxChars int) *HTMLExtractor {
	pdfExtractor, _ := pdfextract.New("", nil)
	return NewHTMLExtractorWithPDF(maxChars, pdfExtractor)
}

func NewHTMLExtractorWithPDF(maxChars int, pdfExtractor IsolatedPDFExtractor) *HTMLExtractor {
	extractor, _ := NewHTMLExtractorWithPDFPolicy(maxChars, pdfExtractor, pdfextract.DefaultPolicy())
	return extractor
}

func NewHTMLExtractorWithPDFPolicy(maxChars int, pdfExtractor IsolatedPDFExtractor, policy pdfextract.Policy) (*HTMLExtractor, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if maxChars < 1 {
		maxChars = 100_000
	}
	return &HTMLExtractor{maxChars: maxChars, pdfExtractor: pdfExtractor, pdfPolicy: policy}, nil
}

func (e *HTMLExtractor) PreflightPDF(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || e.pdfExtractor == nil || runtime.GOOS == "windows" {
		return errors.Join(ErrPDFExtractorUnavailable, pdfextract.ErrUnavailable)
	}
	checker, ok := e.pdfExtractor.(PDFExtractorChecker)
	if !ok {
		return nil
	}
	if err := checker.Check(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, pdfextract.ErrUnavailable) && e.pdfPolicy.AllowUnsandboxedFallback {
			if fallback, ok := e.pdfExtractor.(interface{ FallbackAvailable() bool }); ok && fallback.FallbackAvailable() {
				return nil
			}
		}
		return errors.Join(ErrPDFExtractorUnavailable, err)
	}
	return nil
}

func (e *HTMLExtractor) Extract(ctx context.Context, document FetchedDocument) (ExtractedDocument, error) {
	if err := ctx.Err(); err != nil {
		return ExtractedDocument{}, err
	}
	if mediaType, _, err := mime.ParseMediaType(document.ContentType); err == nil {
		document.ContentType = strings.ToLower(mediaType)
	}
	switch document.ContentType {
	case "application/pdf":
		if e.pdfExtractor == nil {
			return ExtractedDocument{}, ErrPDFExtractorUnavailable
		}
		var result pdfextract.Result
		var err error
		if document.File != nil {
			info, statErr := document.File.Stat()
			if statErr != nil {
				return ExtractedDocument{}, errors.Join(ErrPDFExtractionFailed, statErr)
			}
			if !info.Mode().IsRegular() {
				return ExtractedDocument{}, errors.Join(ErrPDFExtractionFailed, pdfextract.ErrMalformed)
			}
			if info.Size() > e.pdfPolicy.MaxInputBytes {
				return ExtractedDocument{}, errors.Join(ErrPDFTooLarge, pdfextract.ErrInputTooLarge)
			}
			delegate, ok := e.pdfExtractor.(FilePDFExtractor)
			if !ok {
				return ExtractedDocument{}, errors.Join(ErrPDFExtractorUnavailable, pdfextract.ErrUnavailable)
			}
			if policyAware, ok := e.pdfExtractor.(interface {
				ExtractFileWithFallback(context.Context, *os.File, bool) (pdfextract.Result, error)
			}); ok {
				result, err = policyAware.ExtractFileWithFallback(ctx, document.File, e.pdfPolicy.AllowUnsandboxedFallback)
			} else {
				result, err = delegate.ExtractFile(ctx, document.File)
			}
		} else {
			if int64(len(document.Body)) > e.pdfPolicy.MaxInputBytes {
				return ExtractedDocument{}, errors.Join(ErrPDFTooLarge, pdfextract.ErrInputTooLarge)
			}
			if policyAware, ok := e.pdfExtractor.(interface {
				ExtractWithFallback(context.Context, []byte, bool) (pdfextract.Result, error)
			}); ok {
				result, err = policyAware.ExtractWithFallback(ctx, document.Body, e.pdfPolicy.AllowUnsandboxedFallback)
			} else {
				result, err = e.pdfExtractor.Extract(ctx, document.Body)
			}
		}
		if errors.Is(err, pdfextract.ErrUnavailable) {
			return ExtractedDocument{}, errors.Join(ErrPDFExtractorUnavailable, err)
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
		if result.PageCount > e.pdfPolicy.MaxPages {
			return ExtractedDocument{}, errors.Join(ErrPDFTooManyPages, pdfextract.ErrTooManyPages)
		}
		if utf8.RuneCountInString(result.Text) > e.pdfPolicy.MaxTextRunes {
			return ExtractedDocument{}, errors.Join(ErrPDFTooLarge, pdfextract.ErrTextTooLarge)
		}
		text := normalizeExtractedText(result.Text, e.pdfPolicy.MaxTextRunes)
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
			return ExtractedDocument{}, ErrExtractionEmpty
		}
		if isChallengeDocument(nil, "", text) {
			return ExtractedDocument{}, ErrExtractionChallenge
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
	if isChallengeDocument(root, title, cleanText) {
		return ExtractedDocument{}, ErrExtractionChallenge
	}
	flags := []string{}
	if cleanText == "" {
		return ExtractedDocument{}, ErrExtractionEmpty
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

func isChallengeDocument(root *html.Node, title, text string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	for _, marker := range []string{"just a moment", "attention required!", "verify you are human", "robot check", "access denied", "security verification"} {
		if strings.Contains(title, marker) {
			return true
		}
	}
	var challengeForm bool
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "form" {
			for _, attr := range n.Attr {
				if attr.Key == "id" || attr.Key == "action" {
					value := strings.ToLower(attr.Val)
					if strings.Contains(value, "challenge") || strings.Contains(value, "captcha") {
						challengeForm = true
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	if challengeForm {
		return true
	}
	if utf8.RuneCountInString(text) <= 1000 {
		lower := strings.ToLower(text)
		for _, marker := range []string{"verify you are human", "checking your browser before accessing", "enable javascript and cookies to continue", "complete the captcha", "подтвердите, что вы не робот"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
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
