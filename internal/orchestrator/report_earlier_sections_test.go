package orchestrator

import (
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestReportEarlierSectionsSummarizesVerifiedBodies(t *testing.T) {
	sections := []domain.ReportSection{
		{Title: "Основы", Status: "supported", Body: "Нужна база по биологии. [0b9a3f7e-1111-4222-8333-444455556666]\n\n- Python важен для анализа данных. [1][2]"},
		{Title: "Цитаты", Status: "fallback_excerpts", Body: "> raw excerpt [x]"},
	}
	got := reportEarlierSections(sections)
	if len(got) != 1 || got[0]["title"] != "Основы" {
		t.Fatalf("earlier sections = %+v", got)
	}
	said := got[0]["already_said"].(string)
	if strings.Contains(said, "[") || !strings.Contains(said, "Нужна база по биологии.") || !strings.Contains(said, "Python важен") {
		t.Fatalf("already_said = %q", said)
	}
	if reportEarlierSections(nil) == nil || len(reportEarlierSections(nil)) != 0 {
		t.Fatal("empty input must give an empty list")
	}
	long := domain.ReportSection{Title: "Long", Status: "supported", Body: strings.Repeat("слово ", 2000)}
	if n := len([]rune(reportEarlierSections([]domain.ReportSection{long})[0]["already_said"].(string))); n > 1200 {
		t.Fatalf("summary not bounded: %d runes", n)
	}
}
