package orchestrator

import (
	"strings"
	"testing"
)

// kzITGrantGoal is the goal of a live deep run whose facets searched
// "Computer Science deadline requirements 2026" and fetched conference
// call-for-papers pages.
const kzITGrantGoal = "Исследуй, куда гражданин Казахстана может поступить на бакалавриат по IT (Computer Science, Software Engineering, Data Science/AI) за границей на полный грант. Найди государственные стипендии и университеты с полным покрытием (обучение, проживание, перелёт, стипендия), например Stipendium Hungaricum, Türkiye Burslari, GKS (Корея), CSC (Китай), MEXT (Япония), DAAD, а также университеты с need-based full ride для иностранцев. Для каждой программы: что покрывает, требования (GPA, IELTS/TOEFL, SAT, документы, эссе, собеседование), дедлайны приёма 2026/2027, статистика прошлого года (конкурс, проходные или средние баллы, сколько поступило из Казахстана) и сколько стоит попытка поступления (сборы, экзамены, перевод и апостиль документов, виза). Используй официальные сайты и PDF с правилами приёма, указывай год данных; если цифр нет, так и напиши."

func formatGoalEntities(entities []goalEntity) string {
	parts := []string{}
	for _, entity := range entities {
		if entity.Hint != "" {
			parts = append(parts, entity.Name+"("+entity.Hint+")")
		} else {
			parts = append(parts, entity.Name)
		}
	}
	return strings.Join(parts, "|")
}

func TestExtractGoalEntities(t *testing.T) {
	for _, test := range []struct{ goal, want string }{
		{kzITGrantGoal, "Stipendium Hungaricum|Türkiye Burslari|GKS(Корея)|CSC(Китай)|MEXT(Япония)|DAAD"},
		{kzGrantQuestion, "Stipendium Hungaricum|Türkiye Burslari|GKS(Корея)|CSC(Китай)|MEXT(Япония)|DAAD"},
		{"Сравни магистратуру по Machine Learning и Data Science в Европе: стипендии (Erasmus Mundus, Eiffel, Chevening), требования IELTS и GRE", "Erasmus Mundus|Eiffel|Chevening"},
		{"Find fully funded bachelor scholarships in Computer Science for Kazakh students, e.g. Chevening, Fulbright and DAAD (Germany); list GPA, SAT and TOEFL requirements.", "Chevening|Fulbright|DAAD(Germany)"},
		{"Compare Seoul National University and the University of Tokyo admission requirements for Software Engineering (IELTS, GPA)", "Seoul National University|University of Tokyo"},
		{"Какие есть программы бакалавриата по AI и Computer Science с need-based full ride для иностранцев? Нужны дедлайны 2026, SAT, IELTS.", ""},
		{"Расскажи про программу «Глобальное образование» и DAAD", "Глобальное образование|DAAD"},
		{"Сравни стипендии Stipendium Hungaricum, GKS и Türkiye Burslari: дедлайны 2026 и требования, дай PDF", "Stipendium Hungaricum|GKS|Türkiye Burslari"},
		{"Найди гранты, например Болашак и Erasmus+, а также другие варианты.", "Болашак|Erasmus+"},
	} {
		if got := formatGoalEntities(extractGoalEntities(test.goal)); got != test.want {
			t.Errorf("%.60q:\n got %q\nwant %q", test.goal, got, test.want)
		}
	}
}

func TestExtractGoalEntitiesCap(t *testing.T) {
	goal := "Сравни стипендии, например Alpha One, Beta Two, Gamma Three, Delta Four, Epsilon Five, Zeta Six, Eta Seven, Theta Eight, Iota Nine, Kappa Ten."
	if got := extractGoalEntities(goal); len(got) != maxGoalEntities || got[0].Name != "Alpha One" {
		t.Fatalf("entities = %+v", got)
	}
}

func TestGenericGoalTerm(t *testing.T) {
	for name, want := range map[string]bool{
		"Computer Science": true, "Software Engineering": true, "Data Science": true, "AI": true, "IT": true,
		"Machine Learning": true, "Bachelor's": true, "IELTS": true, "need-based": true, "Full Ride": true,
		"GKS": false, "Data Science Institute": false, "University of Tokyo": false, "Stipendium Hungaricum": false,
	} {
		if got := genericGoalTerm(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}
