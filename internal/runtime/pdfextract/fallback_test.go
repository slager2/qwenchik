package pdfextract

import (
	"context"
	"crypto/md5"
	"crypto/rc4"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPureGoParserEnforcesPageLimitAndExtractsTwentyPages(t *testing.T) {
	result, err := parsePureGoPDF(testPDF(20, "page text"))
	if err != nil {
		t.Fatal(err)
	}
	if result.PageCount != 20 || !strings.Contains(result.Text, "page text") {
		t.Fatalf("unexpected 20-page extraction: %#v", result)
	}

	result, err = parsePureGoPDF(testPDF(21, "page text"))
	if !errors.Is(err, ErrTooManyPages) {
		t.Fatalf("21-page error = %v, want ErrTooManyPages", err)
	}
	if result.Text != "" || result.PageCount != 0 {
		t.Fatalf("21-page input returned partial result: %#v", result)
	}
}

func TestPureGoParserEnforcesTextLimitAndRejectsMalformedOrEmptyPDF(t *testing.T) {
	result, err := parsePureGoPDF(testPDF(1, strings.Repeat("x", MaxTextRunes+1)))
	if !errors.Is(err, ErrTextTooLarge) {
		t.Fatalf("text-limit error = %v, want ErrTextTooLarge", err)
	}
	if result.Text != "" || result.PageCount != 0 {
		t.Fatalf("oversized text returned partial result: %#v", result)
	}

	if _, err := parsePureGoPDF([]byte("not a PDF")); !errors.Is(err, ErrMalformed) {
		t.Fatalf("malformed PDF error = %v, want ErrMalformed", err)
	}
	if _, err := parsePureGoPDF(testPDF(1, " \t ")); !errors.Is(err, ErrNoText) {
		t.Fatalf("empty-text PDF error = %v, want ErrNoText", err)
	}
}

func TestPureGoParserRejectsEncryptedPDF(t *testing.T) {
	if _, err := parsePureGoPDF(testEncryptedPDF(1, "secret")); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("encrypted PDF error = %v, want ErrEncrypted", err)
	}
}

func TestUnavailableDockerFallsBackToBoundedPureGoWorker(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "")
	runner := newFakeRunner()
	runner.imageErr = errors.New("PDF image not found locally")
	result, err := newOptInExtractor(t, fakeImageID, runner).Extract(context.Background(), testPDF(2, "fallback evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if result.PageCount != 2 || !strings.Contains(result.Text, "fallback evidence") {
		t.Fatalf("unexpected pure-Go fallback result: %#v", result)
	}
	if result.Method != methodPureGo {
		t.Fatalf("fallback method = %q, want %q", result.Method, methodPureGo)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("unavailable Docker unexpectedly ran a container: %v", runner.runs)
	}
}

func TestPureGoFallbackWorksWithoutConfiguredDockerImage(t *testing.T) {
	result := newOptInExtractor(t, "", newFakeRunner())
	extracted, err := result.Extract(context.Background(), testPDF(1, "offline text"))
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Method != methodPureGo || extracted.PageCount != 1 || !strings.Contains(extracted.Text, "offline text") {
		t.Fatalf("unexpected no-image fallback result: %#v", extracted)
	}
}

func TestPureGoWorkerDoesNotInheritApplicationSecrets(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "test-secret-must-not-reach-parser")
	t.Setenv("LLM_API_KEY", "another-secret")
	environment := workerEnvironment("0123456789abcdef01234567")
	if len(environment) != 2 {
		t.Fatalf("worker environment contains %d entries, want only mode and one-time token", len(environment))
	}
	for _, item := range environment {
		if strings.Contains(item, "test-secret") || strings.Contains(item, "another-secret") {
			t.Fatalf("application secret leaked into parser worker environment: %q", item)
		}
	}
}

func TestRootfulDockerFallsBackToPureGoParser(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "")
	runner := newFakeRunner()
	runner.securityOptions = `["name=seccomp,profile=builtin"]`
	result, err := newOptInExtractor(t, fakeImageID, runner).Extract(context.Background(), testPDF(1, "rootless fallback"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Method != methodPureGo || !strings.Contains(result.Text, "rootless fallback") {
		t.Fatalf("rootful Docker did not use pure-Go fallback: %#v", result)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("rootful Docker unexpectedly ran a container: %v", runner.runs)
	}
}

func newOptInExtractor(t *testing.T, image string, runner Runner) *Extractor {
	t.Helper()
	policy := DefaultPolicy()
	policy.AllowUnsandboxedFallback = true
	extractor, err := NewWithPolicy(image, runner, policy)
	if err != nil {
		t.Fatal(err)
	}
	return extractor
}

func TestPDFOutputStripsControlsAndRejectsEmptyText(t *testing.T) {
	result, err := parseOutput([]byte(outputPrefix + "1\nleft\x00\x01right\x7f\u0085\rnext\tword"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "leftright\nnext\tword" {
		t.Fatalf("control-stripped text = %q", result.Text)
	}
	if _, err := parseOutput([]byte(outputPrefix + "1\n\x00\x01 \t")); !errors.Is(err, ErrNoText) {
		t.Fatalf("empty output error = %v, want ErrNoText", err)
	}
}

func testPDF(pageCount int, text string) []byte {
	return buildTestPDF(pageCount, text, false)
}

func testEncryptedPDF(pageCount int, text string) []byte {
	return buildTestPDF(pageCount, text, true)
}

func buildTestPDF(pageCount int, text string, encrypted bool) []byte {
	objects := make(map[int]string, 2+2*pageCount+1)
	pageRefs := make([]string, 0, pageCount)
	fontObject := 3 + pageCount*2
	for pageIndex := 0; pageIndex < pageCount; pageIndex++ {
		pageObject := 3 + pageIndex*2
		contentObject := pageObject + 1
		pageRefs = append(pageRefs, fmt.Sprintf("%d 0 R", pageObject))
		objects[pageObject] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObject, contentObject)
		content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", escapePDFString(text))
		objects[contentObject] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	}
	objects[1] = "<< /Type /Catalog /Pages 2 0 R >>"
	objects[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(pageRefs, " "), pageCount)
	objects[fontObject] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"

	objectCount := fontObject
	trailerExtra := ""
	if encrypted {
		encryptObject := fontObject + 1
		owner, user, id := testEncryptionValues()
		objects[encryptObject] = fmt.Sprintf("<< /Filter /Standard /V 1 /R 2 /Length 40 /O <%s> /U <%s> /P -4 >>", hex.EncodeToString(owner), hex.EncodeToString(user))
		objectCount = encryptObject
		trailerExtra = fmt.Sprintf(" /Encrypt %d 0 R /ID [<%s> <%s>]", encryptObject, hex.EncodeToString(id), hex.EncodeToString(id))
	}
	var document strings.Builder
	document.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, objectCount+1)
	for objectNumber := 1; objectNumber <= objectCount; objectNumber++ {
		offsets[objectNumber] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", objectNumber, objects[objectNumber])
	}
	xrefOffset := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", objectCount+1)
	for objectNumber := 1; objectNumber <= objectCount; objectNumber++ {
		fmt.Fprintf(&document, "%010d 00000 n \n", offsets[objectNumber])
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", objectCount+1, trailerExtra, xrefOffset)
	return []byte(document.String())
}

func testEncryptionValues() (owner, user, id []byte) {
	owner = make([]byte, 32)
	id = []byte("test-id")
	passwordPadding := []byte{
		0x28, 0xbf, 0x4e, 0x5e, 0x4e, 0x75, 0x8a, 0x41, 0x64, 0x00, 0x4e, 0x56, 0xff, 0xfa, 0x01, 0x08,
		0x2e, 0x2e, 0x00, 0xb6, 0xd0, 0x68, 0x3e, 0x80, 0x2f, 0x0c, 0xa9, 0xfe, 0x64, 0x53, 0x69, 0x7a,
	}
	hash := md5.New()
	_, _ = hash.Write(passwordPadding)
	_, _ = hash.Write(owner)
	_, _ = hash.Write([]byte{0xfc, 0xff, 0xff, 0xff})
	_, _ = hash.Write(id)
	key := hash.Sum(nil)[:5]
	cipher, _ := rc4.NewCipher(key)
	user = append([]byte(nil), passwordPadding...)
	cipher.XORKeyStream(user, user)
	return owner, user, id
}

func escapePDFString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `(`, `\(`)
	return strings.ReplaceAll(value, `)`, `\)`)
}
