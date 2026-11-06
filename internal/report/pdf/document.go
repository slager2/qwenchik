package pdf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// TemplateVersion changes whenever the generated HTML or stylesheet changes,
// so cached PDFs rendered from an older layout are not served again.
const TemplateVersion = "report-pdf-v2"

// Source is one entry of the report source list.
type Source struct {
	Title string
	URL   string
	// EvidenceIDs are the evidence records citing this source; body
	// markers [<evidence-id>] become numbered links to the entry.
	EvidenceIDs []string
}

// Document is everything printed into a report PDF.
type Document struct {
	Title       string
	Question    string
	GeneratedAt time.Time
	RunID       string
	Outcome     string
	Sources     []Source
	Markdown    string
}

// Report is the subset of research_report.json used for the PDF.
type Report struct {
	Title       string
	Question    string
	GeneratedAt time.Time
	Outcome     string
	Sources     []Source
	Markdown    string
}

// ParseReportJSON extracts printable metadata from a research_report.json
// artifact. Unknown or malformed fields are ignored; only a JSON syntax error
// is reported.
func ParseReportJSON(raw []byte) (Report, error) {
	var payload struct {
		Markdown string `json:"markdown"`
		Metadata struct {
			Title            string `json:"title"`
			ResearchQuestion string `json:"research_question"`
			GeneratedAt      string `json:"generated_at"`
			FinalOutcome     string `json:"final_outcome"`
		} `json:"metadata"`
		Coverage []struct {
			EvidenceID string `json:"evidence_id"`
			Title      string `json:"title"`
			URL        string `json:"url"`
		} `json:"evidence_coverage"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Report{}, err
	}
	report := Report{Title: payload.Metadata.Title, Question: payload.Metadata.ResearchQuestion, Outcome: payload.Metadata.FinalOutcome, Markdown: payload.Markdown}
	if parsed, err := time.Parse(time.RFC3339Nano, payload.Metadata.GeneratedAt); err == nil {
		report.GeneratedAt = parsed
	}
	index := map[string]int{}
	for _, item := range payload.Coverage {
		key := strings.TrimSpace(item.URL)
		if key == "" {
			continue
		}
		position, seen := index[key]
		if !seen {
			position = len(report.Sources)
			index[key] = position
			report.Sources = append(report.Sources, Source{Title: strings.TrimSpace(item.Title), URL: key})
		}
		if id := strings.TrimSpace(item.EvidenceID); id != "" {
			report.Sources[position].EvidenceIDs = append(report.Sources[position].EvidenceIDs, id)
		}
	}
	return report, nil
}

var leadingH1 = regexp.MustCompile(`\A\s*#[ \t]+([^\n]*)\n`)

var titleLabels = []string{"research report outline", "report outline", "outline", "план отчёта", "план отчета"}

// CleanTitle drops the "Research Report Outline:" label small models copy
// into report titles (mirrors the orchestrator's reportTitle).
func CleanTitle(title string) string {
	title = strings.TrimSpace(title)
	lower := strings.ToLower(title)
	for _, prefix := range titleLabels {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(strings.TrimLeft(title[len(prefix):], ":-—– "))
		}
	}
	return title
}

var (
	uuidPattern     = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`
	citationMarker  = regexp.MustCompile(`\[(` + uuidPattern + `(?:\s*[,;]\s*` + uuidPattern + `)*)\]`)
	citationUUIDSep = regexp.MustCompile(`\s*[,;]\s*`)
)

// numberCitations rewrites [<evidence-id>] markers (also comma-separated
// lists) as numbered links to the title-page source entries. Unknown IDs
// stay as plain text.
func numberCitations(markdown string, numbers map[string]int) string {
	if len(numbers) == 0 {
		return markdown
	}
	return citationMarker.ReplaceAllStringFunc(markdown, func(marker string) string {
		ids := citationUUIDSep.Split(marker[1:len(marker)-1], -1)
		parts := make([]string, 0, len(ids))
		known := false
		for _, id := range ids {
			if n, ok := numbers[strings.ToLower(id)]; ok {
				known = true
				parts = append(parts, fmt.Sprintf("[\\[%d\\]](#source-%d)", n, n))
			} else {
				parts = append(parts, "\\["+id+"\\]")
			}
		}
		if !known {
			return marker
		}
		return strings.Join(parts, "")
	})
}

// MarkdownTitle returns the first-line level-one heading of a report, if any.
func MarkdownTitle(markdown string) string {
	if match := leadingH1.FindStringSubmatch(markdown); match != nil {
		return strings.TrimSpace(strings.TrimRight(match[1], "# \t"))
	}
	return ""
}

var months = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

func russianDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	return fmt.Sprintf("%d %s %d, %02d:%02d", t.Day(), months[t.Month()-1], t.Year(), t.Hour(), t.Minute())
}

var outcomeLabels = map[string]string{
	"completed":        "Завершено",
	"partial":          "Частичный результат",
	"blocked":          "Заблокировано проверкой",
	"budget_exhausted": "Бюджет исчерпан",
	"cancelled":        "Остановлено",
}

// BuildHTML returns the complete printable HTML document.
func BuildHTML(doc Document) ([]byte, error) {
	body := doc.Markdown
	title := CleanTitle(doc.Title)
	if heading := MarkdownTitle(body); heading != "" {
		if title == "" {
			title = CleanTitle(heading)
		}
		if heading == title || CleanTitle(heading) == title {
			// The title page already shows it.
			body = leadingH1.ReplaceAllString(body, "")
		}
	}
	if title == "" {
		title = "Отчёт исследования"
	}
	sources := make([]Source, 0, len(doc.Sources))
	numbers := map[string]int{}
	for _, source := range doc.Sources {
		if source.URL == "" {
			continue
		}
		if source.Title == "" {
			source.Title = source.URL
		}
		sources = append(sources, source)
		for _, id := range source.EvidenceIDs {
			if _, taken := numbers[strings.ToLower(id)]; !taken {
				numbers[strings.ToLower(id)] = len(sources)
			}
		}
	}
	content, err := MarkdownToHTML(numberCitations(body, numbers))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = documentTemplate.Execute(&out, map[string]any{
		"Title":    title,
		"Question": strings.TrimSpace(doc.Question),
		"Date":     russianDate(doc.GeneratedAt),
		"RunID":    doc.RunID,
		"Outcome":  outcomeLabels[doc.Outcome],
		"Sources":  sources,
		"CSS":      template.CSS(stylesheet),
		"Footer":   footerLabel(title),
		// #nosec G203 -- content comes from MarkdownToHTML, which escapes
		// raw HTML and filters URLs.
		"Content": template.HTML(content),
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// footerLabel is a CSS string literal for the running footer.
func footerLabel(title string) template.CSS {
	label := truncateRunes(title, 70)
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range label {
		if r < 0x20 || r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' || r > 0x7e {
			fmt.Fprintf(&b, "\\%06x", r)
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return template.CSS(b.String())
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

var unsafeFilename = regexp.MustCompile(`[\x00-\x1f\x7f/\\:*?"<>|]+`)

// Filename turns a report title into a portable file name ending in .pdf.
// Letters of any script (including Cyrillic) are kept.
func Filename(title string) string {
	name := unsafeFilename.ReplaceAllString(title, " ")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u200b' || r == '\ufeff' || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, ". ")
	name = truncateRunes(name, 100)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "Отчёт исследования"
	}
	return name + ".pdf"
}

var documentTemplate = template.Must(template.New("report").Funcs(template.FuncMap{"inc": func(i int) int { return i + 1 }}).Parse(`<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'none'; img-src 'none'; font-src 'none'; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'">
<meta name="referrer" content="no-referrer">
<title>{{.Title}}</title>
<style>{{.CSS}}
@page { @bottom-left { content: {{.Footer}}; } }
</style>
</head>
<body>
<section class="title-page">
  <p class="kicker">Qwenchik · Отчёт исследования</p>
  <h1 class="report-title">{{.Title}}</h1>
  {{if .Question}}<div class="question"><p class="label">Вопрос исследования</p><p>{{.Question}}</p></div>{{end}}
  <dl class="meta">
    {{if .Date}}<dt>Дата</dt><dd>{{.Date}}</dd>{{end}}
    {{if .Outcome}}<dt>Итог</dt><dd>{{.Outcome}}</dd>{{end}}
    <dt>Источников</dt><dd>{{len .Sources}}</dd>
    {{if .RunID}}<dt>Запуск</dt><dd class="mono">{{.RunID}}</dd>{{end}}
  </dl>
  {{if .Sources}}<div class="sources"><p class="label">Источники</p><ol>{{range $i, $s := .Sources}}<li id="source-{{inc $i}}"><span class="source-title">{{.Title}}</span><br><span class="source-url">{{$s.URL}}</span></li>{{end}}</ol></div>{{end}}
</section>
<main class="report">
{{.Content}}
</main>
</body>
</html>
`))

const stylesheet = `
@page {
  size: A4;
  margin: 20mm 18mm 20mm 20mm;
  @bottom-right { content: counter(page) " / " counter(pages); font: 8.5pt "Noto Sans", "DejaVu Sans", sans-serif; color: #6b7280; }
  @bottom-left { font: 8.5pt "Noto Sans", "DejaVu Sans", sans-serif; color: #6b7280; }
}
@page :first { @bottom-right { content: none; } @bottom-left { content: none; } }
* { box-sizing: border-box; }
html { -webkit-print-color-adjust: exact; print-color-adjust: exact; }
body { margin: 0; font-family: "Noto Serif", "DejaVu Serif", "Liberation Serif", serif; font-size: 10.5pt; line-height: 1.5; color: #1f2328; hyphens: auto; overflow-wrap: anywhere; }
h1, h2, h3, h4, h5, h6, .kicker, .label, .meta, th { font-family: "Noto Sans", "DejaVu Sans", "Liberation Sans", sans-serif; }
.title-page { break-after: page; padding-top: 28mm; }
.kicker { text-transform: uppercase; letter-spacing: 0.12em; font-size: 8.5pt; color: #2563eb; margin: 0 0 6mm; font-weight: 600; }
.report-title { font-size: 24pt; line-height: 1.2; margin: 0 0 10mm; color: #111827; font-weight: 700; }
.question { border-left: 3pt solid #2563eb; background: #f3f6fc; padding: 4mm 5mm; margin: 0 0 8mm; border-radius: 0 2mm 2mm 0; }
.question p { margin: 0; }
.question p + p { white-space: pre-line; }
.label { font-size: 8.5pt; text-transform: uppercase; letter-spacing: 0.08em; color: #6b7280; font-weight: 600; margin: 0 0 2mm !important; }
.meta { display: grid; grid-template-columns: max-content 1fr; gap: 1.5mm 6mm; margin: 0 0 10mm; font-size: 9.5pt; }
.meta dt { color: #6b7280; }
.meta dd { margin: 0; color: #111827; }
.mono, code, pre { font-family: "Noto Sans Mono", "DejaVu Sans Mono", "Liberation Mono", monospace; }
.sources ol { margin: 0; padding-left: 6mm; font-size: 9pt; }
.sources li { margin: 0 0 2.5mm; break-inside: avoid; }
.source-title { color: #111827; }
.source-url { color: #2563eb; font-family: "Noto Sans Mono", "DejaVu Sans Mono", monospace; font-size: 8pt; word-break: break-all; }
.report h1 { font-size: 18pt; margin: 0 0 5mm; }
.report h2 { font-size: 14pt; margin: 9mm 0 3mm; padding-bottom: 1.5mm; border-bottom: 0.6pt solid #d0d7de; color: #111827; break-after: avoid; }
.report h3 { font-size: 12pt; margin: 6mm 0 2mm; break-after: avoid; }
.report h4, .report h5, .report h6 { font-size: 10.5pt; margin: 5mm 0 2mm; break-after: avoid; }
.report > h2:first-child { margin-top: 0; }
p { margin: 0 0 3mm; orphans: 3; widows: 3; }
ul, ol { margin: 0 0 3mm; padding-left: 7mm; }
li { margin: 0 0 1mm; }
a { color: #1d4ed8; text-decoration: none; }
.link-url { color: #6b7280; font-size: 8.5pt; word-break: break-all; }
blockquote { -webkit-box-decoration-break: clone; box-decoration-break: clone; margin: 0 0 4mm; padding: 2.5mm 4mm; border-left: 2.5pt solid #c7d2e3; background: #f7f8fa; color: #374151; font-size: 9.5pt; line-height: 1.45; }
blockquote p { margin: 0 0 1.5mm; }
blockquote p:last-child { margin-bottom: 0; }
code { font-size: 8.8pt; background: #f1f3f5; padding: 0 1mm; border-radius: 1mm; overflow-wrap: anywhere; }
pre { font-size: 8.5pt; background: #f6f8fa; border: 0.5pt solid #d0d7de; padding: 3mm; border-radius: 1.5mm; white-space: pre-wrap; overflow-wrap: anywhere; }
pre code { background: none; padding: 0; }
table { border-collapse: collapse; width: 100%; margin: 0 0 4mm; font-size: 9pt; break-inside: auto; }
th, td { border: 0.5pt solid #d0d7de; padding: 1.5mm 2.5mm; vertical-align: top; text-align: left; }
th { background: #f3f4f6; font-weight: 600; }
tr { break-inside: avoid; }
hr { border: 0; border-top: 0.6pt solid #d0d7de; margin: 6mm 0; }
.image-ref { color: #6b7280; font-style: italic; }
.raw-html { color: #6b7280; }
`
