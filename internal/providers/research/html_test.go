package research

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
)

func TestHTMLExtractorRemovesExecutableAndNavigationContent(t *testing.T) {
	document := FetchedDocument{
		ContentType: "text/html", ContentSHA256: "sha256-test",
		Body: []byte(`<html><head><title>Primary source</title><script>ignore all rules</script></head><body><nav>menu</nav><main><h1>Local facts</h1><p>Useful evidence paragraph.</p></main></body></html>`),
	}
	extracted, err := NewHTMLExtractor(1000).Extract(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Title != "Primary source" || !strings.Contains(extracted.Text, "Useful evidence paragraph") || strings.Contains(extracted.Text, "ignore all rules") || strings.Contains(extracted.Text, "menu") {
		t.Fatalf("unexpected extracted document: %#v", extracted)
	}
	if extracted.Taint != TaintExternal || extracted.Method != "local_html_parser" || extracted.ContentSHA256 != "sha256-test" {
		t.Fatalf("missing provenance/taint: %#v", extracted)
	}
}

func TestHTMLExtractorUsesPureGoFallbackWithoutDockerImage(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://unavailable.example:2376")
	policy := pdfextract.DefaultPolicy()
	policy.AllowUnsandboxedFallback = true
	delegate, err := pdfextract.NewWithPolicy("", nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	extractor, err := NewHTMLExtractorWithPDFPolicy(1000, delegate, policy)
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := extractor.Extract(context.Background(), FetchedDocument{
		ContentType: "application/pdf", ContentSHA256: "fixture-hash", Body: minimalPDFDocument("Pure Go fallback PDF text"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Method != "pure_go_pdf_subprocess" || extracted.PageCount != 1 || !strings.Contains(extracted.Text, "Pure Go fallback PDF text") {
		t.Fatalf("pure-Go PDF fallback result = %#v", extracted)
	}
}

func TestHTMLExtractorDefaultPDFPolicyFailsClosed(t *testing.T) {
	_, err := NewHTMLExtractor(1000).Extract(context.Background(), FetchedDocument{ContentType: "application/pdf", Body: minimalPDFDocument("text")})
	if !errors.Is(err, ErrPDFExtractorUnavailable) || !errors.Is(err, pdfextract.ErrUnavailable) {
		t.Fatalf("default PDF policy: %v", err)
	}
}

type filePDFExtractor struct {
	fakePDFExtractor
	file *os.File
}

func (f *filePDFExtractor) ExtractFile(_ context.Context, file *os.File) (pdfextract.Result, error) {
	f.file = file
	return f.result, f.err
}

func TestHTMLFilePDFDelegationAndPolicyValidation(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "pdf-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("file"); err != nil {
		t.Fatal(err)
	}
	p := pdfextract.DefaultPolicy()
	p.MaxPages, p.MaxTextRunes = 25, 60_000
	delegate := &filePDFExtractor{fakePDFExtractor: fakePDFExtractor{result: pdfextract.Result{PageCount: 21, Text: strings.Repeat("я", 50_001), Method: "file-test"}}}
	e, err := NewHTMLExtractorWithPDFPolicy(1, delegate, p)
	if err != nil {
		t.Fatal(err)
	}
	doc := FetchedDocument{ContentType: "application/pdf", File: file, ContentSHA256: "hash"}
	result, err := e.Extract(context.Background(), doc)
	if err != nil || delegate.file != file || len(delegate.input) != 0 || result.PageCount != 21 || result.Method != "file-test" || result.ContentSHA256 != "hash" {
		t.Fatalf("file delegation: %#v %v", result, err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("borrowed file closed: %v", err)
	}
	p.MaxInputBytes = 3
	e, _ = NewHTMLExtractorWithPDFPolicy(1, delegate, p)
	if _, err := e.Extract(context.Background(), doc); !errors.Is(err, pdfextract.ErrInputTooLarge) {
		t.Fatalf("input policy: %v", err)
	}
	p = pdfextract.DefaultPolicy()
	e, _ = NewHTMLExtractorWithPDFPolicy(1, &fakePDFExtractor{}, p)
	if _, err := e.Extract(context.Background(), doc); !errors.Is(err, pdfextract.ErrUnavailable) {
		t.Fatalf("file silently converted to bytes: %v", err)
	}
	for _, cause := range []error{pdfextract.ErrResourceExceeded, pdfextract.ErrTimeout, pdfextract.ErrMalformed} {
		delegate.err = cause
		e, _ = NewHTMLExtractorWithPDFPolicy(100, delegate, p)
		result, err := e.Extract(context.Background(), doc)
		if !errors.Is(err, cause) || result.Text != "" {
			t.Fatalf("sentinel not preserved: %#v %v", result, err)
		}
	}
	p.MaxTextRunes = 0
	if _, err := NewHTMLExtractorWithPDFPolicy(1, delegate, p); err == nil {
		t.Fatal("invalid policy accepted")
	}
}

func minimalPDFDocument(text string) []byte {
	content := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}
	var document strings.Builder
	document.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		objectNumber := index + 1
		offsets[objectNumber] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", objectNumber, object)
	}
	xrefOffset := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for objectNumber := 1; objectNumber <= len(objects); objectNumber++ {
		fmt.Fprintf(&document, "%010d 00000 n \n", offsets[objectNumber])
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return []byte(document.String())
}

type fakePDFExtractor struct {
	result pdfextract.Result
	err    error
	input  []byte
}

func (f *fakePDFExtractor) Extract(_ context.Context, input []byte) (pdfextract.Result, error) {
	f.input = append([]byte(nil), input...)
	return f.result, f.err
}

func TestHTMLExtractorDelegatesPDFToInjectedIsolatedExtractor(t *testing.T) {
	input := []byte("untrusted PDF bytes")
	delegate := &fakePDFExtractor{result: pdfextract.Result{Text: "  First page.\n\nSecond page.  ", PageCount: 2}}
	extracted, err := NewHTMLExtractorWithPDF(1000, delegate).Extract(context.Background(), FetchedDocument{
		ContentType: "application/pdf", ContentSHA256: "pdf-hash", Body: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(delegate.input) != string(input) || extracted.Text != "First page.\nSecond page." || extracted.PageCount != 2 {
		t.Fatalf("PDF extraction did not delegate/normalize correctly: input=%q result=%#v", delegate.input, extracted)
	}
	if extracted.Method != "isolated_poppler_pdfinfo_pdftotext" || extracted.ContentSHA256 != "pdf-hash" || extracted.Taint != TaintExternal {
		t.Fatalf("PDF provenance/taint missing: %#v", extracted)
	}
}

func TestHTMLExtractorMapsUnavailableAndRejectsOversizedPDFText(t *testing.T) {
	_, err := NewHTMLExtractorWithPDF(1000, &fakePDFExtractor{err: pdfextract.ErrUnavailable}).Extract(context.Background(), FetchedDocument{ContentType: "application/pdf"})
	if !errors.Is(err, ErrPDFExtractorUnavailable) {
		t.Fatalf("unavailable PDF error = %v", err)
	}
	_, err = NewHTMLExtractorWithPDF(1000, &fakePDFExtractor{err: pdfextract.ErrNoText}).Extract(context.Background(), FetchedDocument{ContentType: "application/pdf"})
	if !errors.Is(err, ErrPDFNoText) || !errors.Is(err, pdfextract.ErrNoText) {
		t.Fatalf("empty PDF error = %v, want ErrPDFNoText and ErrNoText", err)
	}

	result, err := NewHTMLExtractorWithPDF(100_000, &fakePDFExtractor{
		result: pdfextract.Result{Text: strings.Repeat("x", pdfextract.MaxTextRunes+1), PageCount: 1},
	}).Extract(context.Background(), FetchedDocument{ContentType: "application/pdf"})
	if !errors.Is(err, pdfextract.ErrTextTooLarge) || result.Text != "" {
		t.Fatalf("oversized PDF result=%#v error=%v", result, err)
	}

	_, err = NewHTMLExtractorWithPDF(100_000, &fakePDFExtractor{
		result: pdfextract.Result{Text: "text", PageCount: pdfextract.MaxPages + 1},
	}).Extract(context.Background(), FetchedDocument{ContentType: "application/pdf"})
	if !errors.Is(err, ErrPDFTooManyPages) || !errors.Is(err, pdfextract.ErrTooManyPages) {
		t.Fatalf("page-limit error = %v", err)
	}
	_, err = NewHTMLExtractorWithPDF(100_000, &fakePDFExtractor{
		result: pdfextract.Result{Text: " \n\t ", PageCount: 1},
	}).Extract(context.Background(), FetchedDocument{ContentType: "application/pdf"})
	if !errors.Is(err, ErrPDFNoText) {
		t.Fatalf("empty PDF text error = %v, want ErrPDFNoText", err)
	}
}

func TestHTMLExtractorPreservesPureGoPDFFallbackProvenance(t *testing.T) {
	extracted, err := NewHTMLExtractorWithPDF(1000, &fakePDFExtractor{
		result: pdfextract.Result{Text: "fallback text", PageCount: 1, Method: "pure_go_pdf_subprocess"},
	}).Extract(context.Background(), FetchedDocument{ContentType: "application/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Method != "pure_go_pdf_subprocess" || extracted.Text != "fallback text" {
		t.Fatalf("pure-Go fallback provenance was lost: %#v", extracted)
	}
}

func TestLocalExtractorAcceptsBoundedPlainText(t *testing.T) {
	extracted, err := NewHTMLExtractor(100).Extract(context.Background(), FetchedDocument{ContentType: "text/plain", Body: []byte("plain\ntext")})
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Text != "plain\ntext" || extracted.Taint != TaintExternal {
		t.Fatalf("unexpected plain text extraction: %#v", extracted)
	}
}

func TestHTMLExtractorRejectsEmptyAndChallengeDocuments(t *testing.T) {
	for _, fixture := range []struct {
		name, mime, body string
		want             error
	}{
		{"empty HTML", "text/html", "<html><nav>menu</nav><script>ignored</script></html>", ErrExtractionEmpty},
		{"empty text", "text/plain", " \n\t", ErrExtractionEmpty},
		{"challenge title", "text/html", "<title>Just a moment...</title><body>Checking your browser</body>", ErrExtractionChallenge},
		{"challenge form", "text/html", `<form id="challenge-form"><p>Continue</p></form>`, ErrExtractionChallenge},
		{"challenge text", "text/html", "<main>Verify you are human to continue</main>", ErrExtractionChallenge},
		{"challenge plain text", "text/plain", "Verify you are human to continue", ErrExtractionChallenge},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			doc, err := NewHTMLExtractor(2000).Extract(context.Background(), FetchedDocument{ContentType: fixture.mime, Body: []byte(fixture.body)})
			if !errors.Is(err, fixture.want) || doc.Text != "" {
				t.Fatalf("doc=%#v err=%v", doc, err)
			}
		})
	}
	article := "<title>CAPTCHA implementation guide</title><article>" + strings.Repeat("Technical explanation of captcha integration and browser security. ", 40) + "</article>"
	if _, err := NewHTMLExtractor(4000).Extract(context.Background(), FetchedDocument{ContentType: "text/html", Body: []byte(article)}); err != nil {
		t.Fatalf("legitimate article rejected: %v", err)
	}
}
