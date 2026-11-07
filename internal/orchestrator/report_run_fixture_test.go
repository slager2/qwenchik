package orchestrator

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

// testdata/report_run_2d18e5e5.json is the stored report state and source
// catalogue of live run 2d18e5e5-c2b6-4b29-9c13-d55dfacb397a (the first
// live run of the structured report).
type reportRunFixture struct {
	RunID         string                     `json:"run_id"`
	Goal          string                     `json:"goal"`
	Missing       []string                   `json:"missing"`
	Supplementary []string                   `json:"supplementary"`
	Report        domain.ResearchReportState `json:"report"`
	Catalogue     []citationEvidence         `json:"catalogue"`
}

func loadReportRunFixture(t *testing.T) reportRunFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/report_run_2d18e5e5.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture reportRunFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func renderReportRunFixture(t *testing.T) string {
	t.Helper()
	fixture := loadReportRunFixture(t)
	markdown := renderSectionedReport(&fixture.Report, fixture.Goal, fixture.RunID, fixture.Catalogue, fixture.Missing, fixture.Supplementary)
	if path := os.Getenv("QWENCHIK_REPORT_RUN_SAMPLE"); path != "" {
		_ = os.WriteFile(path, []byte(markdown), 0o644)
	}
	return markdown
}

// reportSection returns the Markdown of the "## title" section.
func reportSection(markdown, title string) string {
	start := strings.Index(markdown, "\n## "+title+"\n")
	if start < 0 {
		return ""
	}
	rest := markdown[start+len(title)+5:]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestReportRunFixtureFactsStayWithTheirEntity(t *testing.T) {
	markdown := renderReportRunFixture(t)
	hungary := reportSection(markdown, "Stipendium Hungaricum")
	if hungary == "" || strings.Contains(hungary, "| ") || strings.Contains(hungary, "6,500 TL") || strings.Contains(hungary, "**Факты**") {
		t.Fatalf("Stipendium Hungaricum shows another entity's facts:\n%s", hungary)
	}
	turkiye := reportSection(markdown, "Türkiye Burslari")
	for _, attribute := range []string{"| покрытие |", "| дедлайн приёма |", "| сбор за подачу |", "| требования к среднему баллу |", "| стипендия в месяц |"} {
		if strings.Count(turkiye, attribute) != 1 {
			t.Fatalf("Türkiye Burslari shows %q %d times:\n%s", attribute, strings.Count(turkiye, attribute), turkiye)
		}
	}
	for _, want := range []string{"| покрытие | Университет, обучение, проживание, страховка, год турецкого языка, перелёт |", "| сбор за подачу | Нет |", "| стипендия в месяц | 6,500 TL |"} {
		if !strings.Contains(turkiye, want) {
			t.Fatalf("Türkiye Burslari lacks %q:\n%s", want, turkiye)
		}
	}
}

func TestReportRunFixtureValuesAreReadable(t *testing.T) {
	markdown := renderReportRunFixture(t)
	for _, bad := range []string{"&\\#", "&\\\\#", "&#39;", "&#34;", "6,500 TL ( |", "None. You apply online", "Usually 10 January", "Under 21", "bachelor's", "Awards open to you", "Дословные выдержки", "continue\\_2\\_fetch", "Fetch criterion", "No official source", "goal.0"} {
		if strings.Contains(markdown, bad) {
			t.Fatalf("rendered report contains %q:\n%s", bad, markdown)
		}
	}
	for _, want := range []string{
		"- Официальные сайты не загружены для: Computer Science, Software Engineering, Data Science, Stipendium Hungaricum, Türkiye Burslari, GKS; сведения о них взяты со сторонних сайтов.",
		"- Не пройдено служебных проверок хода исследования: 1 (подробности — в панели исследования).",
		"- Не все пункты вопроса раскрыты подтверждёнными разделами (5); пробелы перечислены в разделе «Что не найдено».",
		"Bachelor's ...](https://www.thelightofengineering.com/",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("rendered report lacks %q:\n%s", want, markdown)
		}
	}
}

func TestReportRunFixtureFallbackSectionsAreShort(t *testing.T) {
	markdown := renderReportRunFixture(t)
	for _, title := range []string{"Stipendium Hungaricum", "MEXT", "DAAD"} {
		section := reportSection(markdown, title)
		if section == "" || !strings.Contains(section, "Связного текста не получилось подтвердить") && !strings.Contains(section, "Подтверждённых сведений по этому разделу") {
			t.Fatalf("fallback section %s:\n%s", title, section)
		}
		if quotes := strings.Count(section, "\n> "); quotes > 2 {
			t.Fatalf("%s shows %d excerpts:\n%s", title, quotes, section)
		}
		for _, line := range strings.Split(section, "\n") {
			if strings.HasPrefix(line, "> ") && len([]rune(line)) > 560 {
				t.Fatalf("%s excerpt too long (%d runes): %s", title, len([]rune(line)), line)
			}
		}
	}
}

func TestReportRunFixtureGapsNameEntityAttributes(t *testing.T) {
	markdown := renderReportRunFixture(t)
	gaps := reportSection(markdown, "Что не найдено")
	for _, want := range []string{"- **Stipendium Hungaricum**: подтверждённых сведений не найдено.", "- **Türkiye Burslari** — места и конкурс: не найдено в проверенных источниках.", "- **GKS** — сборы и расходы, места и конкурс, средний балл, язык (IELTS/TOEFL): не найдено", "**MEXT**: подтверждённых сведений не найдено."} {
		if !strings.Contains(gaps, want) {
			t.Fatalf("gaps lack %q:\n%s", want, gaps)
		}
	}
	keyNumbers := reportSection(markdown, "Ключевые цифры")
	if strings.Count(keyNumbers, "| Türkiye Burslari |") > reportKeyNumbersPerEntity {
		t.Fatalf("key numbers take more than %d rows of one entity:\n%s", reportKeyNumbersPerEntity, keyNumbers)
	}
}
