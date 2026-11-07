package pdf

import (
	"strings"
	"testing"
)

func TestBuildHTMLNumbersCitationsInsideTables(t *testing.T) {
	id := "3c757d00-1032-43dc-8b10-547adc82b579"
	markdown := "## Сравнение\n\n| Объект | дедлайн |\n|---|---|\n| **SH** | 15 января 2026 [" + id + "] |\n\n| № | Источник |\n|---|---|\n| [" + id + "] | [SH](https://stipendiumhungaricum.hu/apply/) |\n"
	page, err := BuildHTML(Document{Title: "T", Markdown: markdown, Sources: []Source{{Title: "SH", URL: "https://stipendiumhungaricum.hu/apply/", EvidenceIDs: []string{id}}}})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, id) || strings.Count(html, `href="#source-1"`) != 2 || !strings.Contains(html, "<table>") || !strings.Contains(html, "<th>дедлайн</th>") {
		t.Fatalf("table citations were not numbered: %s", html)
	}
	if !strings.Contains(html, "thead { display: table-header-group; }") {
		t.Fatal("table headers do not repeat across pages")
	}
}
