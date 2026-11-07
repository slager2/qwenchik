package orchestrator

import (
	"strings"
	"testing"
)

const kzGrantQuestion = "Исследуй, куда гражданин Казахстана может поступить на бакалавриат по IT (Computer Science, Software Engineering, Data Science/AI) за границей на полный грант. Найди государственные стипендии, например Stipendium Hungaricum, Türkiye Burslari, GKS (Корея), CSC (Китай), MEXT (Япония), DAAD. Для каждой программы: требования (GPA, IELTS/TOEFL, SAT)."

func TestReportQuestionEntities(t *testing.T) {
	got := strings.Join(reportQuestionEntities(kzGrantQuestion), ",")
	if got != "Stipendium Hungaricum,Türkiye Burslari,GKS,CSC,MEXT,DAAD" {
		t.Fatalf("entities=%s", got)
	}
}

func TestReportEntityOutlineGroupsSourcesPerEntity(t *testing.T) {
	_, catalogue, _ := sampleReportFixture()
	entities := filterReportEntities(append(reportQuestionEntities(kzGrantQuestion), "Invented University"), kzGrantQuestion, catalogue)
	// Question names are kept even without sources (they become gaps);
	// names neither the question nor a source mention are dropped.
	if strings.Contains(strings.Join(entities, ","), "Invented") || !strings.Contains(strings.Join(entities, ","), "MEXT") {
		t.Fatalf("entities=%v", entities)
	}
	sections := reportEntityOutline(entities, catalogue, []string{"goal.0"}, 4, reportLabelsRU)
	if len(sections) != 4 {
		t.Fatalf("sections=%+v", sections)
	}
	if sections[0].Title != "Stipendium Hungaricum" || strings.Join(sections[0].EvidenceIDs, ",") != evSH+","+evKZ {
		t.Fatalf("entity section lost its sources: %+v", sections[0])
	}
	if sections[3].Title != reportLabelsRU.CrossSection || len(sections[3].EvidenceIDs) != len(catalogue) || sections[3].Criteria[0] != "goal.0" {
		t.Fatalf("cross-cutting section missing: %+v", sections[3])
	}
	if reportEntityOutline([]string{"MEXT"}, catalogue, nil, 4, reportLabelsRU) != nil {
		t.Fatal("a single entity produced an entity outline")
	}
}

func TestOutlinePinnedLegacyDetection(t *testing.T) {
	if !reportOutlinePinnedLegacy(`{"goal":"x"}`) || reportOutlinePinnedLegacy(`{"goal":"x","outline_format":"entities_v1"}`) {
		t.Fatal("outline format detection is wrong")
	}
}
