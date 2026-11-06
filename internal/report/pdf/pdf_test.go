package pdf

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMarkdownToHTMLEscapesRawHTMLAndFiltersURLs(t *testing.T) {
	cases := []struct {
		name, input string
		forbidden   []string
		required    []string
	}{
		{"script block", "<script>alert(1)</script>\n", []string{"<script"}, []string{"&lt;script&gt;alert(1)&lt;/script&gt;"}},
		{"inline html", "text <img src=x onerror=alert(1)> more", []string{"<img"}, []string{"&lt;img src=x onerror=alert(1)&gt;"}},
		{"iframe block", "<iframe src=\"https://evil.example\"></iframe>\n", []string{"<iframe"}, []string{"&lt;iframe"}},
		{"javascript link", "[click](javascript:alert(1))", []string{"javascript:", "<a "}, []string{"click"}},
		{"javascript autolink", "<javascript:alert(1)>", []string{"href=\"javascript", "<a "}, []string{"javascript:alert(1)"}},
		{"data link", "[x](data:text/html;base64,PHNjcmlwdD4=)", []string{"data:text", "<a "}, nil},
		{"file link", "[x](file:///etc/passwd)", []string{"file:", "<a "}, nil},
		{"vbscript link", "[x](VBScript:msgbox)", []string{"<a "}, nil},
		{"image is not loaded", "![logo](https://tracker.example/pixel.png)", []string{"<img"}, []string{"[Изображение: logo] https://tracker.example/pixel.png"}},
		{"attribute syntax ignored", "# Title {onclick=alert(1)}", []string{"onclick=\"", " onclick="}, nil},
		{"quote breakout in title", "[x](https://a.example \"t\\\" onmouseover=\\\"alert(1)\")", []string{"onmouseover=\""}, []string{`href="https://a.example"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := MarkdownToHTML(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(out)
			for _, bad := range tc.forbidden {
				if strings.Contains(lower, strings.ToLower(bad)) {
					t.Fatalf("output contains %q: %s", bad, out)
				}
			}
			for _, want := range tc.required {
				if !strings.Contains(out, want) {
					t.Fatalf("output lacks %q: %s", want, out)
				}
			}
		})
	}
}

func TestMarkdownToHTMLLinks(t *testing.T) {
	out, err := MarkdownToHTML("См. [документацию](https://example.org/a?b=1&c=2), <https://example.com/x> и https://bare.example/path, [раздел](#sources), почта <me@example.org>.")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<a href="https://example.org/a?b=1&amp;c=2">документацию</a><span class="link-url"> (https://example.org/a?b=1&amp;c=2)</span>`,
		`<a href="https://example.com/x">https://example.com/x</a>`,
		`<a href="https://bare.example/path">`,
		`<a href="#sources">раздел</a>`,
		`<a href="mailto:me@example.org">me@example.org</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, `(#sources)`) {
		t.Fatalf("fragment link target printed: %s", out)
	}
}

func TestMarkdownToHTMLTablesAndCode(t *testing.T) {
	out, err := MarkdownToHTML("| a | b |\n|---|---|\n| <b>1</b> | 2 |\n\n```\n<script>x</script>\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<table>") || strings.Contains(out, "<b>") || !strings.Contains(out, "&lt;script&gt;x&lt;/script&gt;") {
		t.Fatalf("unexpected table/code rendering: %s", out)
	}
}

func TestBuildHTMLTitlePageAndEscaping(t *testing.T) {
	doc := Document{
		Title:       "Отчёт </title><script>alert(1)</script>",
		Question:    "Вопрос <b>жирный</b>?",
		GeneratedAt: time.Date(2026, 11, 5, 17, 45, 0, 0, time.UTC),
		RunID:       "run-1",
		Outcome:     "partial",
		Sources:     []Source{{Title: "Источник <i>", URL: "https://example.org/?q=<x>"}, {URL: ""}},
		Markdown:    "# Другой заголовок\n\nТекст.",
	}
	out, err := BuildHTML(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "<script>") || strings.Contains(text, "<b>жирный") || strings.Contains(text, "<i>") || strings.Contains(text, "q=<x>") {
		t.Fatalf("unescaped metadata in document: %s", text)
	}
	for _, want := range []string{"Content-Security-Policy", "script-src 'none'", "Вопрос исследования", "Частичный результат", "https://example.org/?q=&lt;x&gt;", "<h1>Другой заголовок</h1>", "counter(pages)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Count(text, `<li id="source-`) != 1 {
		t.Fatalf("empty source URL must be dropped")
	}
	// A leading heading equal to the title is not printed twice.
	out, err = BuildHTML(Document{Markdown: "# Заголовок\n\n## Раздел\n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "<h1>Заголовок</h1>") || !strings.Contains(string(out), `<h1 class="report-title">Заголовок</h1>`) {
		t.Fatalf("title heading should move to the title page only: %s", out)
	}
}

func TestFooterLabelIsCSSEscaped(t *testing.T) {
	label := string(footerLabel(`a"}</style><script>`))
	if strings.ContainsAny(label[1:len(label)-1], `"<>`) {
		t.Fatalf("footer label not escaped: %s", label)
	}
}

func TestFilename(t *testing.T) {
	cases := map[string]string{
		"Роадмап по биоинформатике": "Роадмап по биоинформатике.pdf",
		"a/b\\c:d*e?f\"g<h>i|j":     "a b c d e f g h i j.pdf",
		"  ..hidden.. ":             "hidden.pdf",
		"":                          "Отчёт исследования.pdf",
		"line\nbreak\ttab\u202eRTL": "line break tabRTL.pdf",
	}
	for input, want := range cases {
		if got := Filename(input); got != want {
			t.Errorf("Filename(%q)=%q want %q", input, got, want)
		}
	}
	if got := Filename(strings.Repeat("я", 300)); len([]rune(got)) > 105 {
		t.Fatalf("filename not truncated: %d runes", len([]rune(got)))
	}
}

func TestParseReportJSON(t *testing.T) {
	report, err := ParseReportJSON([]byte(`{"markdown":"# T","metadata":{"title":"T","research_question":"Q","generated_at":"2026-11-05T17:45:03.66788183Z","final_outcome":"partial"},"evidence_coverage":[{"title":"A","url":"https://a.example"},{"title":"A again","url":"https://a.example"},{"title":"B","url":"https://b.example"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if report.Title != "T" || report.Question != "Q" || report.Outcome != "partial" || report.GeneratedAt.IsZero() || len(report.Sources) != 2 || report.Markdown != "# T" {
		t.Fatalf("unexpected report: %#v", report)
	}
	if _, err := ParseReportJSON([]byte("{")); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestDetectChromeExplicitMissing(t *testing.T) {
	if _, err := DetectChrome("/nonexistent/qwenchik-chrome"); err == nil {
		t.Fatal("expected error for missing explicit renderer")
	}
}

func TestChromeRenderProducesPDF(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renderer smoke test runs on Linux and macOS")
	}
	binary, err := DetectChrome(os.Getenv("PDF_RENDERER_BIN"))
	if err != nil {
		t.Skip("no Chromium-family browser installed")
	}
	html, err := BuildHTML(Document{Title: "Проверка PDF", Question: "Работает ли кириллица?", GeneratedAt: time.Now(), Sources: []Source{{Title: "Пример", URL: "https://example.org"}}, Markdown: "## Раздел\n\nТекст <script>document.title='pwned'</script> и ![img](https://example.org/x.png).\n"})
	if err != nil {
		t.Fatal(err)
	}
	renderer := NewChrome(binary)
	renderer.TempDir = t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, err := renderer.Render(ctx, html)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) || len(data) < 1000 {
		t.Fatalf("not a PDF: %q", data[:min(len(data), 16)])
	}
	entries, _ := os.ReadDir(renderer.TempDir)
	if len(entries) != 0 {
		t.Fatalf("render directory not cleaned up: %v", entries)
	}
}

func TestChromeRenderHonoursCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as a fake renderer")
	}
	dir := t.TempDir()
	script := dir + "/slow-renderer"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	renderer := &Chrome{Binary: script, Timeout: 300 * time.Millisecond, TempDir: t.TempDir()}
	started := time.Now()
	if _, err := renderer.Render(context.Background(), []byte("<html></html>")); err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("renderer was not killed on timeout")
	}
}

func TestChromeRenderRejectsNonPDFOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as a fake renderer")
	}
	dir := t.TempDir()
	script := dir + "/fake-renderer"
	// Writes a non-PDF file to the --print-to-pdf target.
	body := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in --print-to-pdf=*) printf 'not a pdf' > \"${a#--print-to-pdf=}\";; esac; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	renderer := &Chrome{Binary: script, TempDir: t.TempDir()}
	if _, err := renderer.Render(context.Background(), []byte("<html></html>")); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected invalid output error, got %v", err)
	}
}

func TestCitationMarkersBecomeNumberedSourceLinks(t *testing.T) {
	a, b, unknown := "8361383a-1487-4d37-9e8d-bbd5eb59be05", "c310b9bd-f3fc-4806-9a3e-3b1fc4c911eb", "00000000-0000-4000-8000-000000000000"
	out, err := BuildHTML(Document{
		Title:    "Research Report Outline: Bioinformatics Roadmap",
		Sources:  []Source{{Title: "Dropped", URL: ""}, {Title: "A", URL: "https://a.example", EvidenceIDs: []string{a}}, {Title: "B", URL: "https://b.example", EvidenceIDs: []string{b}}},
		Markdown: "# Research Report Outline: Bioinformatics Roadmap\n\nClaim [" + a + "]. Both [" + a + ", " + b + "]. Unknown [" + unknown + "]. Mixed [" + b + "; " + unknown + "]. `[" + a + "]`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{
		`<h1 class="report-title">Bioinformatics Roadmap</h1>`,
		`<li id="source-1">`, `<li id="source-2">`,
		`Claim <a href="#source-1">[1]</a>.`,
		`Both <a href="#source-1">[1]</a><a href="#source-2">[2]</a>.`,
		"Unknown [" + unknown + "].",
		`Mixed <a href="#source-2">[2]</a>[` + unknown + `].`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "Research Report Outline") || strings.Contains(text, "<h1>Bioinformatics") {
		t.Fatalf("outline label or duplicate title kept: %s", text)
	}
	if got := CleanTitle("План отчёта — Тема"); got != "Тема" {
		t.Fatalf("CleanTitle=%q", got)
	}
}
