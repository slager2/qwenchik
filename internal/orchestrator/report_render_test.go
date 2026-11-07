package orchestrator

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

// Fixture data follows the facts of the 9B live run
// 20231000-bf3b-47df-8a5b-00c4ba398846 (IT bachelor grants for Kazakhstan).
const (
	evSH  = "3c757d00-1032-43dc-8b10-547adc82b579"
	evTB  = "8e37407c-9eec-4764-a66e-0680229bba13"
	evGKS = "1be322ed-9841-423c-ba3a-04965fc464fb"
	evKZ  = "5c766154-6377-4f72-bd5d-e45213895e2b"
)

func sampleReportFixture() (*domain.ResearchReportState, []citationEvidence, string) {
	goal := "Исследуй, куда гражданин Казахстана может поступить на бакалавриат по IT за границей на полный грант: Stipendium Hungaricum, Türkiye Burslari, GKS."
	catalogue := []citationEvidence{
		{EvidenceID: evSH, URL: "https://stipendiumhungaricum.hu/apply/", Title: "Stipendium Hungaricum — Application 2026/2027", Excerpt: "Stipendium Hungaricum offers tuition-free education, a monthly stipend, accommodation and medical insurance. The application deadline for the 2026/2027 academic year is 15 January 2026, 14:00 CET."},
		{EvidenceID: evTB, URL: "https://www.turkiyeburslari.gov.tr/en/page/prospective-students/scholarship-coverage", Title: "Türkiye Scholarships — coverage", Excerpt: "Undergraduate students receive a monthly stipend of 4,500 TL. Applications for 2026 are accepted from 10 January to 20 February 2026. Around 5,000 scholarships are awarded every year. Undergraduate applicants must be under 21 years old."},
		{EvidenceID: evGKS, URL: "https://www.studyinkorea.go.kr/gks/2027-undergraduate.pdf", Title: "GKS 2027 Undergraduate Guidelines", PDF: true, Excerpt: "Global Korea Scholarship provides a settlement grant of 200,000 KRW and a monthly allowance of 1,000,000 KRW. Approximately 286 undergraduate places are available in the 2027 cycle."},
		{EvidenceID: evKZ, URL: "https://www.gov.kz/memleket/entities/edu/press/news/details/sh-2025", Title: "Stipendium Hungaricum для Казахстана 2025-2026", Excerpt: "В рамках программы Stipendium Hungaricum на 2025-2026 учебный год выделено 110 мест для бакалавриата."},
	}
	review := func(claim, id, quote string) json.RawMessage {
		raw, _ := json.Marshal(reportCitationReview{Claims: []citationClaim{{Claim: claim, EvidenceID: id, Quote: quote, Kind: "supported"}}, CriteriaCovered: []string{}})
		return raw
	}
	report := &domain.ResearchReportState{
		Version: 1, Title: "Полные гранты на IT-бакалавриат для граждан Казахстана", Outcome: "partial", PrivacyMode: domain.PrivacyExternalSearch, GeneratedAt: "2026-11-07T10:00:00Z",
		Entities: []string{"Stipendium Hungaricum", "Türkiye Burslari", "GKS", "MEXT"},
		TLDR: []domain.ReportBullet{
			{Text: "Самый ранний дедлайн у Stipendium Hungaricum — 15 января 2026.", EvidenceIDs: []string{evSH}},
			{Text: "Больше всего мест у Türkiye Burslari: около 5 000 стипендий в год.", EvidenceIDs: []string{evTB}},
		},
		TLDRStatus: "model",
		Sections: []domain.ReportSection{
			{ID: "section_1", Title: "Stipendium Hungaricum", EvidenceIDs: []string{evSH, evKZ}, Status: "supported", Body: "Программа покрывает обучение, проживание и страховку и платит ежемесячную стипендию. [" + evSH + "]", Review: review("Программа покрывает обучение, проживание и страховку и платит ежемесячную стипендию.", evSH, "tuition-free education, a monthly stipend, accommodation and medical insurance"), FactsStatus: "extracted", Facts: []domain.ReportFact{
				{Entity: "Stipendium Hungaricum", Attribute: "дедлайн", Value: "15 января 2026, 14:00 CET", Year: "2026/2027", EvidenceID: evSH, Quote: "The application deadline for the 2026/2027 academic year is 15 January 2026, 14:00 CET.", Check: "critic"},
				{Entity: "Stipendium Hungaricum", Attribute: "мест", Value: "110 (бакалавриат, Казахстан)", Year: "2025-2026", EvidenceID: evKZ, Quote: "на 2025-2026 учебный год выделено 110 мест для бакалавриата", Check: "critic"},
			}},
			{ID: "section_2", Title: "Türkiye Burslari", EvidenceIDs: []string{evTB}, Status: "supported", Body: "- Студенты бакалавриата получают ежемесячную стипендию 4 500 лир. [" + evTB + "]", Review: review("Студенты бакалавриата получают ежемесячную стипендию 4 500 лир.", evTB, "Undergraduate students receive a monthly stipend of 4,500 TL."), FactsStatus: "extracted", Facts: []domain.ReportFact{
				{Entity: "Türkiye Burslari", Attribute: "дедлайн", Value: "10 января – 20 февраля 2026", Year: "2026", EvidenceID: evTB, Quote: "Applications for 2026 are accepted from 10 January to 20 February 2026.", Check: "critic"},
				{Entity: "Türkiye Burslari", Attribute: "стипендия в месяц", Value: "4 500 TL", EvidenceID: evTB, Quote: "a monthly stipend of 4,500 TL", Check: "critic"},
				{Entity: "Türkiye Burslari", Attribute: "мест", Value: "около 5 000 в год", EvidenceID: evTB, Quote: "Around 5,000 scholarships are awarded every year.", Check: "critic"},
				{Entity: "Türkiye Burslari", Attribute: "возраст", Value: "до 21 года", EvidenceID: evTB, Quote: "Undergraduate applicants must be under 21 years old.", Check: "critic"},
			}},
			{ID: "section_3", Title: "GKS (Корея)", EvidenceIDs: []string{evGKS}, Status: "supported", Body: "- Студенты бакалавриата получают ежемесячную стипендию 4 500 лир. [" + evTB + "]\n- Студенты GKS получают разовое пособие 200 000 KRW при заселении. [" + evGKS + "]", Review: review("Студенты GKS получают разовое пособие 200 000 KRW при заселении.", evGKS, "a settlement grant of 200,000 KRW"), Limitation: "unsupported_claims_removed", FactsStatus: "extracted", Facts: []domain.ReportFact{
				{Entity: "GKS", Attribute: "стипендия в месяц", Value: "1 000 000 KRW", EvidenceID: evGKS, Quote: "a monthly allowance of 1,000,000 KRW", Check: "critic"},
				{Entity: "GKS", Attribute: "пособие на заселение", Value: "200 000 KRW", EvidenceID: evGKS, Quote: "a settlement grant of 200,000 KRW", Check: "critic"},
				{Entity: "GKS", Attribute: "мест", Value: "286", Year: "2027", EvidenceID: evGKS, Quote: "Approximately 286 undergraduate places are available in the 2027 cycle.", Check: "critic"},
			}},
		},
	}
	return report, catalogue, goal
}

func TestRenderedReportHasTablesAndHonestGaps(t *testing.T) {
	report, catalogue, goal := sampleReportFixture()
	for _, section := range report.Sections {
		if err := validateReportFacts(section.Facts, catalogue); err != nil {
			t.Fatalf("fixture fact does not verify in %s: %v", section.ID, err)
		}
	}
	markdown := renderSectionedReport(report, goal, "run-1", catalogue, nil, nil)
	if path := os.Getenv("QWENCHIK_REPORT_SAMPLE"); path != "" {
		_ = os.WriteFile(path, []byte(markdown), 0o644)
	}
	order := []string{"## Коротко", "## Ключевые цифры", "## Сравнение", "## Содержание", "## Stipendium Hungaricum", "## Что не найдено", "## Источники", "## Проверка"}
	position := -1
	for _, heading := range order {
		next := strings.Index(markdown, heading)
		if next <= position {
			t.Fatalf("heading %q missing or out of order:\n%s", heading, markdown)
		}
		position = next
	}
	for _, want := range []string{
		"| Объект | мест | дедлайн | стипендия в месяц |",
		"| **Stipendium Hungaricum** | 110 (бакалавриат, Казахстан) (2025-2026) [" + evKZ + "] | 15 января 2026, 14:00 CET (2026/2027) [" + evSH + "] | — |",
		"| **Türkiye Burslari** | около 5 000 в год [" + evTB + "] | 10 января – 20 февраля 2026 [" + evTB + "] | 4 500 TL [" + evTB + "] |",
		"**Stipendium Hungaricum**: стипендия в месяц — не найдено в проверенных источниках.",
		"**MEXT**: подтверждённых сведений не найдено.",
		"| [" + evGKS + "] | [GKS 2027 Undergraduate Guidelines](https://www.studyinkorea.go.kr/gks/2027-undergraduate.pdf) | studyinkorea.go.kr | да | PDF | 2027 |",
		"| GKS (Корея) | подтверждено источниками | часть утверждений не подтвердилась выдержками и удалена; фактов проверено: 3 |",
		"| Türkiye Burslari | возраст | до 21 года | [" + evTB + "] |",
		"- Самый ранний дедлайн у Stipendium Hungaricum — 15 января 2026. [" + evSH + "]",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("rendered report lacks %q:\n%s", want, markdown)
		}
	}
	// A claim repeated by a later section is rendered once.
	if strings.Count(markdown, "ежемесячную стипендию 4 500 лир") != 1 {
		t.Fatalf("duplicated claim was not removed:\n%s", markdown)
	}
	for _, noise := range []string{"Статус секции", "Ограничение секции", "Анализ основан на"} {
		if strings.Contains(markdown, noise) {
			t.Fatalf("noisy line %q in report", noise)
		}
	}
}

func TestReportTLDRKeepsOnlyGroundedBullets(t *testing.T) {
	statements := []reportStatement{{Text: "Türkiye Burslari — стипендия в месяц: 4 500 TL", EvidenceIDs: []string{"tb"}}, {Text: "GKS — мест: 286 (2027)", EvidenceIDs: []string{"gks"}}}
	var answer reportTLDRAnswer
	_ = json.Unmarshal([]byte(`{"bullets":[
		{"text":"Türkiye Burslari платит 4 500 TL в месяц, GKS даёт 286 мест в 2027.","statements":[1,2]},
		{"text":"GKS даёт 300 мест.","statements":[2]},
		{"text":"Ссылка https://example.com","statements":[1]},
		{"text":"Выдуманный номер утверждения","statements":[7]}
	]}`), &answer)
	bullets := verifyReportTLDR(answer, statements)
	if len(bullets) != 1 || strings.Join(bullets[0].EvidenceIDs, ",") != "tb,gks" {
		t.Fatalf("bullets=%+v", bullets)
	}
	report, _, _ := sampleReportFixture()
	fallback := fallbackReportTLDR(report.Sections)
	if len(fallback) != 3 || fallback[2].EvidenceIDs[0] != evGKS {
		t.Fatalf("fallback TL;DR=%+v", fallback)
	}
}

func TestReportOfficialHosts(t *testing.T) {
	for host, want := range map[string]bool{"turkiyeburslari.gov.tr": true, "studyinkorea.go.kr": true, "mit.edu": true, "www.ox.ac.uk": true, "daad.de": true, "medium.com": false, "reddit.com": false} {
		if reportOfficialHost(host) != want {
			t.Fatalf("official(%s) != %v", host, want)
		}
	}
}
