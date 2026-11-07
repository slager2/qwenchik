package orchestrator

import (
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

// Values and quotes are from run 2d18e5e5 (Türkiye Burslari section).
func TestReportDisplayValueIsShortCompleteAndRussian(t *testing.T) {
	cases := []struct {
		value, quote, want string
		ok                 bool
	}{
		{"tuition, accommodation, health insurance, a one-year Turkish course, a return flight and a monthly stipend of 6,500 TL (", "It covers tuition, accommodation, health insurance, a one-year Turkish course, a return flight and a monthly stipend of 6,500 TL (bachelor's)", "", false},
		{"None. You apply online for free", "Application fee | None. You apply online for free", "", false},
		{"Usually 10 January – 20 February", "General call | Usually 10 January – 20 February", "Обычно 10 января – 20 февраля", true},
		{"15 to 30 September 2026", "Open·Next: Undergraduate 2027 cycle: 15 to 30 September 2026.", "15 – 30 сентября 2026", true},
		{"January – April", "Open·Next: January – April", "Январь – апрель", true},
		{"70% (bachelor&#39;s)", "Minimum grades | 70% (bachelor's), 75% (master's and PhD)", "", false},
		{"6,500 TL", "Monthly stipend | Bachelor's 6,500 TL; master's 9,500 TL", "6,500 TL", true},
		{"до 21 года для бакалавриата", "Age limits | Under 21 (bachelor's), under 30 (master's)", "до 21 года для бакалавриата", true},
		{"до 22 лет", "Age limits | Under 21 (bachelor's)", "", false},
		{"Не указано", "The genuinely open schemes are the ones with no such conditions.", "", false},
		{"No minimum language score is stated for the standard scholarship. Funded one-year Turkish course; many programmes in", "No minimum language score is stated for the standard scholarship. Funded one-year Turkish course; many programmes in Turkish", "", false},
		{"без IELTS (допускаются MOI, Duolingo, тесты на месте)", "accept applications without IELTS. Alternatives include the Medium of Instruction (MOI) letter, Duolingo English Test", "без IELTS (допускаются MOI, Duolingo, тесты на месте)", true},
	}
	for _, c := range cases {
		got, ok := reportDisplayValue(domain.ReportFact{Value: c.value, Quote: c.quote}, true)
		if ok != c.ok || got != c.want {
			t.Fatalf("reportDisplayValue(%q) = %q, %v; want %q, %v", c.value, got, ok, c.want, c.ok)
		}
	}
	// English reports keep English values.
	if got, ok := reportDisplayValue(domain.ReportFact{Value: "None. You apply online for free", Quote: "Application fee | None. You apply online for free"}, false); !ok || got != "None. You apply online for free" {
		t.Fatalf("english value = %q, %v", got, ok)
	}
}

func TestReportValueCutInQuote(t *testing.T) {
	if !reportValueCutInQuote("many programmes in Turk", "many programmes in Turkish") {
		t.Fatal("cut word not detected")
	}
	if reportValueCutInQuote("70", "1970 and 70 more") {
		t.Fatal("a later word-bounded match is complete")
	}
}

func TestReportLimitationItemsHideExecutionCriteria(t *testing.T) {
	lines := reportLimitationItems([]string{
		`Fetch criterion "continue_2_fetch".0 is marked met without citing its fetched evidence.`,
		"No official source has been fetched yet for Stipendium Hungaricum.",
		"No official source has been fetched yet for GKS.",
		"The accepted evidence names no year after 2025; current (2026) official dates and deadlines are still missing.",
	}, true)
	want := []string{
		"Официальные сайты не загружены для: Stipendium Hungaricum, GKS; сведения о них взяты со сторонних сайтов.",
		"Источники не называют годов позже 2025: актуальные (2026) официальные даты и дедлайны не найдены.",
		"Не пройдено служебных проверок хода исследования: 1 (подробности — в панели исследования).",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines=%q", lines)
	}
	for index := range want {
		if lines[index] != want[index] {
			t.Fatalf("line %d = %q, want %q", index, lines[index], want[index])
		}
	}
}
