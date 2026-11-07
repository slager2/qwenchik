// Package obsidian exports a research report as an Obsidian vault folder:
// "00 MOC.md" (TL;DR callout, key numbers, comparison, links to every
// note), one note per report section, "Источники.md" and "Методология.md".
// Notes are cut from the stored report Markdown, so they carry exactly the
// verified text and tables of the report; evidence markers become numbered
// links to the source URLs, numbered like the PDF source list.
package obsidian

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MOCName and SourcesName are the fixed note names of a vault.
const (
	MOCName     = "00 MOC"
	SourcesName = "Источники"
	MethodName  = "Методология"
)

// Note is one Markdown file of the vault.
type Note struct {
	Name    string // without .md
	Content string
}

// Vault is an exported report.
type Vault struct {
	Folder string
	Notes  []Note
}

type block struct {
	heading string
	body    string
}

type source struct {
	title, url string
}

// Backend report headings (both languages) mapped to their role; every
// other level-two heading is a report section.
var roles = map[string]string{
	"коротко": "tldr", "tl;dr": "tldr", "краткое резюме": "summary", "summary": "summary",
	"ключевые цифры": "numbers", "key numbers": "numbers",
	"сравнение": "comparison", "comparison": "comparison",
	"содержание": "contents", "contents": "contents",
	"что не найдено": "gaps", "not found": "gaps",
	"вопрос исследования": "question", "research question": "question",
	"ограничения": "method", "limitations": "method", "методология": "method", "methodology": "method",
	"проверка": "method", "verification": "method", "покрытие": "method", "coverage": "method",
	"источники": "sources", "sources": "sources",
}

var (
	uuid       = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`
	marker     = regexp.MustCompile(`\[(` + uuid + `(?:\s*[,;]\s*` + uuid + `)*)\]`)
	markerSep  = regexp.MustCompile(`\s*[,;]\s*`)
	unsafeName = regexp.MustCompile(`[\x00-\x1f\x7f/\\:*?"<>|#^\[\]]+`)
)

// Build cuts the report into notes. reportJSON (research_report.json) is
// optional; without it markers stay as written.
func Build(markdown string, reportJSON []byte, title string, date time.Time) (Vault, error) {
	if strings.TrimSpace(markdown) == "" {
		return Vault{}, errors.New("report has no Markdown")
	}
	numbers, sources := sourceNumbers(reportJSON)
	cite := func(text string) string { return citeMarkers(text, numbers, sources) }
	heading, blocks := splitBlocks(markdown)
	if strings.TrimSpace(title) == "" {
		title = heading
	}
	if strings.TrimSpace(title) == "" {
		title = "Отчёт исследования"
	}
	created := date.Local().Format("2006-01-02")
	vault := Vault{Folder: safeName(created + " " + title)}
	byRole := map[string][]block{}
	sections := []block{}
	for _, b := range blocks {
		role, ok := roles[strings.ToLower(strings.TrimSpace(b.heading))]
		if !ok {
			sections = append(sections, b)
			continue
		}
		byRole[role] = append(byRole[role], b)
	}
	names := make([]string, len(sections))
	used := map[string]bool{MOCName: true, SourcesName: true, MethodName: true}
	for index, section := range sections {
		name := safeName(fmt.Sprintf("%02d %s", index+1, plain(section.heading)))
		for attempt := 2; used[name]; attempt++ {
			name = safeName(fmt.Sprintf("%02d %s (%d)", index+1, plain(section.heading), attempt))
		}
		used[name] = true
		names[index] = name
	}
	frontmatter := func(tags ...string) string {
		return "---\ntags: [" + strings.Join(append([]string{"qwenchik", "research"}, tags...), ", ") + "]\ncreated: " + created + "\n---\n\n"
	}

	var moc strings.Builder
	moc.WriteString(frontmatter("moc", "index"))
	fmt.Fprintf(&moc, "# 00 · MOC — %s\n\n", strings.TrimSpace(title))
	for _, b := range append(byRole["tldr"], byRole["summary"]...) {
		moc.WriteString(callout("abstract", "TL;DR", cite(b.body)))
	}
	for _, b := range append(byRole["numbers"], byRole["comparison"]...) {
		fmt.Fprintf(&moc, "## %s\n\n%s\n\n", b.heading, strings.TrimSpace(cite(b.body)))
	}
	moc.WriteString("## Заметки\n\n")
	for index, name := range names {
		fmt.Fprintf(&moc, "%d. [[%s]]\n", index+1, name)
	}
	fmt.Fprintf(&moc, "\nИсточники: [[%s]] · методология и проверка: [[%s]]\n\n", SourcesName, MethodName)
	for _, b := range byRole["gaps"] {
		moc.WriteString(callout("warning", b.heading, cite(b.body)))
	}
	for _, b := range byRole["question"] {
		moc.WriteString(callout("question", b.heading, b.body))
	}
	vault.Notes = append(vault.Notes, Note{Name: MOCName, Content: strings.TrimRight(moc.String(), "\n") + "\n"})

	for index, section := range sections {
		var note strings.Builder
		note.WriteString(frontmatter("section"))
		fmt.Fprintf(&note, "# %s\n\n", strings.Replace(names[index], " ", " · ", 1))
		note.WriteString(callout("info", "Часть исследования", "[["+MOCName+"]] — "+strings.TrimSpace(title)))
		fmt.Fprintf(&note, "%s\n\n## Связанное\n\n", strings.TrimSpace(cite(section.body)))
		links := []string{"[[" + MOCName + "]]"}
		if index > 0 {
			links = append(links, "[["+names[index-1]+"]]")
		}
		if index+1 < len(names) {
			links = append(links, "[["+names[index+1]+"]]")
		}
		links = append(links, "[["+SourcesName+"]]")
		note.WriteString(strings.Join(links, " · ") + "\n")
		vault.Notes = append(vault.Notes, Note{Name: names[index], Content: note.String()})
	}

	var sourcesNote strings.Builder
	sourcesNote.WriteString(frontmatter("sources"))
	fmt.Fprintf(&sourcesNote, "# %s\n\n", SourcesName)
	sourcesNote.WriteString(callout("abstract", "Как проверено", "Каждое значение в заметках проверено по точной выдержке из источника; номера [n] совпадают с PDF-отчётом. [["+MOCName+"]]"))
	for _, b := range byRole["sources"] {
		sourcesNote.WriteString(strings.TrimSpace(cite(b.body)) + "\n\n")
	}
	if len(byRole["sources"]) == 0 && len(sources) > 0 {
		for index, s := range sources {
			fmt.Fprintf(&sourcesNote, "%d. [%s](%s)\n", index+1, s.title, s.url)
		}
	}
	vault.Notes = append(vault.Notes, Note{Name: SourcesName, Content: strings.TrimRight(sourcesNote.String(), "\n") + "\n"})

	var method strings.Builder
	method.WriteString(frontmatter("method"))
	fmt.Fprintf(&method, "# %s\n\n", MethodName)
	for _, b := range byRole["method"] {
		fmt.Fprintf(&method, "## %s\n\n%s\n\n", b.heading, strings.TrimSpace(cite(b.body)))
	}
	fmt.Fprintf(&method, "[[%s]]\n", MOCName)
	vault.Notes = append(vault.Notes, Note{Name: MethodName, Content: method.String()})
	return vault, nil
}

func callout(kind, title, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "> [!%s] %s\n", kind, title)
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> " + line + "\n")
	}
	return b.String() + "\n"
}

// splitBlocks returns the level-one title and the level-two blocks.
func splitBlocks(markdown string) (string, []block) {
	title := ""
	blocks := []block{}
	var current *block
	var body strings.Builder
	flush := func() {
		if current != nil {
			current.body = strings.TrimSpace(body.String())
			blocks = append(blocks, *current)
		}
		body.Reset()
	}
	inFence := false
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		switch {
		case !inFence && strings.HasPrefix(line, "# ") && title == "" && current == nil:
			title = plain(strings.TrimSpace(line[2:]))
			continue
		case !inFence && strings.HasPrefix(line, "## "):
			flush()
			current = &block{heading: strings.TrimSpace(line[3:])}
			continue
		}
		if current != nil {
			body.WriteString(line + "\n")
		}
	}
	flush()
	return title, blocks
}

// plain removes Markdown backslash escapes from heading text.
func plain(text string) string {
	return strings.NewReplacer(`\[`, "[", `\]`, "]", `\*`, "*", `\_`, "_", `\#`, "#", "\\`", "`", `\|`, "|", `\\`, `\`, "&#91;", "[", "&#93;", "]", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'").Replace(text)
}

func safeName(name string) string {
	name = unsafeName.ReplaceAllString(plain(name), " ")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(strings.Join(strings.Fields(name), " "), ". ")
	if utf8.RuneCountInString(name) > 90 {
		name = strings.TrimSpace(string([]rune(name)[:90]))
	}
	if name == "" {
		name = "Отчёт"
	}
	return name
}

// sourceNumbers numbers sources by unique URL in evidence_coverage order,
// exactly like the PDF source list.
func sourceNumbers(raw []byte) (map[string]int, []source) {
	numbers := map[string]int{}
	sources := []source{}
	if len(raw) == 0 {
		return numbers, sources
	}
	var payload struct {
		Coverage []struct {
			EvidenceID string `json:"evidence_id"`
			Title      string `json:"title"`
			URL        string `json:"url"`
		} `json:"evidence_coverage"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return numbers, sources
	}
	byURL := map[string]int{}
	for _, item := range payload.Coverage {
		link := strings.TrimSpace(item.URL)
		parsed, err := url.Parse(link)
		if link == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			continue
		}
		number, ok := byURL[link]
		if !ok {
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = parsed.Hostname()
			}
			sources = append(sources, source{title: title, url: link})
			number = len(sources)
			byURL[link] = number
		}
		numbers[strings.ToLower(strings.TrimSpace(item.EvidenceID))] = number
	}
	return numbers, sources
}

var linkEscaper = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "|", "%7C", "<", "%3C", ">", "%3E")

func citeMarkers(text string, numbers map[string]int, sources []source) string {
	if len(numbers) == 0 {
		return text
	}
	return marker.ReplaceAllStringFunc(text, func(match string) string {
		parts := []string{}
		for _, id := range markerSep.Split(match[1:len(match)-1], -1) {
			number, ok := numbers[strings.ToLower(id)]
			if !ok {
				return match
			}
			parts = append(parts, fmt.Sprintf("[\\[%d\\]](%s)", number, linkEscaper.Replace(sources[number-1].url)))
		}
		return strings.Join(parts, "")
	})
}

// Write stores the vault as <baseDir>/<vault.Folder>, adding " (2)", " (3)"…
// when the folder exists. It returns the folder path.
func Write(baseDir string, vault Vault) (string, error) {
	if baseDir == "" {
		return "", errors.New("export directory is not configured")
	}
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return "", err
	}
	for attempt := 1; attempt <= 1000; attempt++ {
		name := vault.Folder
		if attempt > 1 {
			name = fmt.Sprintf("%s (%d)", vault.Folder, attempt)
		}
		dir := filepath.Join(baseDir, name)
		if err := os.Mkdir(dir, 0o700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}
		for _, note := range vault.Notes {
			if err := os.WriteFile(filepath.Join(dir, note.Name+".md"), []byte(note.Content), 0o600); err != nil {
				return "", err
			}
		}
		return dir, nil
	}
	return "", errors.New("no free folder name for the Obsidian export")
}
