package orchestrator

import (
	"strings"
	"testing"
)

func TestReportLanguageFollowsResearchQuestion(t *testing.T) {
	for question, want := range map[string]string{
		"сделай мне роадмап по биоинформатике, этапы и время":             "ru",
		"Original task:\nкак стать биоинженером, если знаю Python и SQL?": "ru",
		"Research protocol architecture and limitations.":                 "en",
		"Compare PostgreSQL vs MySQL (кратко)":                            "en",
	} {
		if got := reportLanguage(question); got != want {
			t.Fatalf("reportLanguage(%q) = %q, want %q", question, got, want)
		}
	}
	if !strings.Contains(reportOutlinePrompt(4, reportLabelsFor("Как учить биологию?")), "in Russian") || !strings.Contains(reportWriterPrompt(4, reportLabelsFor("Как учить биологию?")), "[1][3]") {
		t.Fatal("Russian question did not produce Russian outline/writer instructions")
	}
}

func TestReportTitleDropsOutlineLabel(t *testing.T) {
	for title, want := range map[string]string{
		"Research Report Outline: Bioinformatics Roadmap": "Bioinformatics Roadmap",
		"План отчёта — Биоинформатика":                    "Биоинформатика",
		"Дорожная карта":                                  "Дорожная карта",
		"Outline":                                         "Fallback",
	} {
		if got := reportTitle(title, "Fallback"); got != want {
			t.Fatalf("reportTitle(%q) = %q, want %q", title, got, want)
		}
	}
}
