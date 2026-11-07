package orchestrator

import (
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func reportFactSources() map[string]citationEvidence {
	return reportSourceMap([]citationEvidence{
		{EvidenceID: "sh", Title: "Stipendium Hungaricum — Call for Applications 2026/2027", Excerpt: "Stipendium Hungaricum provides a monthly stipend of HUF 43,700 for bachelor students. The application deadline is 15 January 2026, 14:00 CET."},
		{EvidenceID: "tb", Title: "Türkiye Scholarships", Excerpt: "Undergraduate students receive 4 500 TL per month. Applications: 10 January – 20 February."},
	})
}

func TestReportFactNumbersMustComeFromTheQuote(t *testing.T) {
	sources := reportFactSources()
	for _, test := range []struct {
		name  string
		fact  domain.ReportFact
		ok    bool
		year  string
		named bool
	}{
		{"exact thousands separator differs", domain.ReportFact{Entity: "Stipendium Hungaricum", Attribute: "стипендия в месяц", Value: "43 700 HUF", Year: "2026", EvidenceID: "sh", Quote: "a monthly stipend of HUF 43,700 for bachelor students"}, true, "", true},
		{"year stated in the quote", domain.ReportFact{Entity: "Stipendium Hungaricum", Attribute: "дедлайн", Value: "15 января 2026, 14:00", Year: "2026", EvidenceID: "sh", Quote: "The application deadline is 15 January 2026, 14:00 CET."}, true, "2026", true},
		{"invented number", domain.ReportFact{Entity: "Stipendium Hungaricum", Attribute: "стипендия в месяц", Value: "50 000 HUF", EvidenceID: "sh", Quote: "a monthly stipend of HUF 43,700 for bachelor students"}, false, "", false},
		{"invented quote", domain.ReportFact{Entity: "Stipendium Hungaricum", Attribute: "IELTS", Value: "6.5", EvidenceID: "sh", Quote: "IELTS 6.5 is required for all applicants"}, false, "", false},
		{"unknown source", domain.ReportFact{Entity: "GKS", Attribute: "мест", Value: "286", EvidenceID: "zz", Quote: "about 286 places"}, false, "", false},
		{"text value, entity named by the title", domain.ReportFact{Entity: "Türkiye Burslari", Attribute: "приём заявок", Value: "с 10 января по 20 февраля", EvidenceID: "tb", Quote: "Applications: 10 January – 20 February."}, true, "", false},
		{"markup in value", domain.ReportFact{Entity: "Türkiye Scholarships", Attribute: "x", Value: "see https://example.com 4 500", EvidenceID: "tb", Quote: "Undergraduate students receive 4 500 TL per month."}, false, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fact, ok, named := cleanReportFact(test.fact, sources)
			if ok != test.ok {
				t.Fatalf("ok=%v want %v (%+v)", ok, test.ok, fact)
			}
			if ok && fact.Year != test.year {
				t.Fatalf("year=%q want %q", fact.Year, test.year)
			}
			if ok && test.named && !named {
				t.Fatalf("entity named by the source was not recognised: %+v", fact)
			}
		})
	}
	// A shared distinctive word names the entity; generic words do not.
	if !reportEntityNamed("Türkiye Burslari", "Türkiye Scholarships") || reportEntityNamed("Global Korea Scholarship", "Türkiye Scholarships") {
		t.Fatal("entity naming check ignores distinctive words or matches generic ones")
	}
}

func TestReportFactsPersistedValidation(t *testing.T) {
	catalogue := []citationEvidence{{EvidenceID: "sh", Title: "Stipendium Hungaricum", Excerpt: "Stipendium Hungaricum provides a monthly stipend of HUF 43,700."}}
	good := domain.ReportFact{Entity: "Stipendium Hungaricum", Attribute: "стипендия", Value: "43 700 HUF", EvidenceID: "sh", Quote: "a monthly stipend of HUF 43,700", Check: "critic"}
	if err := validateReportFacts([]domain.ReportFact{good}, catalogue); err != nil {
		t.Fatal(err)
	}
	tampered := good
	tampered.Value = "99 000 HUF"
	unchecked := good
	unchecked.Check = ""
	for _, fact := range []domain.ReportFact{tampered, unchecked} {
		if err := validateReportFacts([]domain.ReportFact{fact}, catalogue); err == nil {
			t.Fatalf("invalid persisted fact accepted: %+v", fact)
		}
	}
}

// Run 2d18e5e5: the Stipendium Hungaricum section extracted only Türkiye
// Burslari facts, and the checker rejected "Open·Next: January – April"
// because the quote alone does not name CSC.
func TestReportFactExtractionHelpers(t *testing.T) {
	entities := []string{"Stipendium Hungaricum", "Türkiye Burslari", "GKS", "CSC"}
	if got := reportSectionEntity("Stipendium Hungaricum", entities); got != "Stipendium Hungaricum" || reportSameEntity("Türkiye Burslari", got) {
		t.Fatalf("section entity=%q", got)
	}
	if got := reportSectionEntity("GKS (Корея)", entities); got != "GKS" {
		t.Fatalf("section entity=%q", got)
	}
	if got := reportSectionEntity("Сравнение программ", entities); got != "" {
		t.Fatalf("generic section entity=%q", got)
	}
	excerpt := "China·Government\n\nChina Scholarship Council (CSC), Ministry of Education of China\n\nOpen·Next: January – April"
	if got := reportQuoteContext(excerpt, "Open·Next: January – April"); !strings.Contains(got, "China Scholarship Council (CSC)") {
		t.Fatalf("context=%q", got)
	}
	if got := reportQuoteContext(excerpt, "not in the excerpt"); got != "" {
		t.Fatalf("context of a missing quote=%q", got)
	}
}

func TestReportNumbers(t *testing.T) {
	got := strings.Join(reportNumbers("от 4 500 до 9 000 лир, IELTS 6.5, 2026/2027, 43,700"), ",")
	if got != "4500,9000,65,2026,2027,43700" {
		t.Fatalf("numbers=%s", got)
	}
}
