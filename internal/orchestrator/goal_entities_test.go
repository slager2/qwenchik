package orchestrator

import (
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
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

func TestEntityFacetsForLiveKazakhstanGoal(t *testing.T) {
	// The live planner packed every programme into one query (no results)
	// and the old facets searched "Computer Science deadline requirements".
	plan := domain.Plan{Subtasks: []domain.PlanStep{
		{ID: "search_1", Kind: "search", Query: "Stipendium Hungaricum Türkiye Burslari GKS CSC MEXT DAAD Bachelor Computer Science Software Engineering Data Science Kazakhstan citizens 2026 2027", SuccessCriteria: []string{"x"}},
		{ID: "search_2", Kind: "search", Query: "стипендии бакалавриат IT для граждан Казахстана 2026", SuccessCriteria: []string{"x"}},
		{ID: "fetch_1", Kind: "fetch", Dependencies: []string{"search_1"}, SuccessCriteria: []string{"x"}},
	}}
	got := ensureEntitySearchFacets(plan, kzITGrantGoal, 8, 30, searchParamsNow)
	queries := []string{}
	for _, step := range got.Subtasks[2:] {
		if step.Kind == "search" {
			queries = append(queries, step.Query)
		}
	}
	want := []string{
		"Stipendium Hungaricum scholarship undergraduate 2027 deadline requirements",
		"Türkiye Burslari undergraduate 2027 deadline requirements",
		"GKS Korea scholarship undergraduate 2027 deadline requirements",
		"CSC China scholarship undergraduate 2027 deadline requirements",
		"MEXT Japan scholarship undergraduate 2027 deadline requirements",
		"DAAD scholarship undergraduate 2027 deadline requirements",
	}
	if strings.Join(queries, "|") != strings.Join(want, "|") {
		t.Fatalf("queries:\n%s", strings.Join(queries, "\n"))
	}
	for _, query := range queries {
		if strings.Contains(query, "Computer Science") || strings.Contains(query, "Data Science") {
			t.Fatalf("generic field facet: %q", query)
		}
	}
	// A query naming at most two entities still covers them.
	covered := domain.Plan{Subtasks: []domain.PlanStep{{ID: "search_1", Kind: "search", Query: "GKS Korea scholarship 2027"}}}
	if !planCoversEntity(covered, "GKS", goalEntities(kzITGrantGoal)...) || planCoversEntity(plan, "GKS", goalEntities(kzITGrantGoal)...) {
		t.Fatal("coverage of packed and single-entity queries is wrong")
	}
}

func TestEntitySearchQueryWithoutProgrammeWords(t *testing.T) {
	goal := "Compare Notion and Obsidian Sync pricing for teams in 2026"
	if got := entitySearchQuery(goalEntity{Name: "Obsidian Sync"}, goal, searchParamsNow); got != "Obsidian Sync 2026 pricing" {
		t.Fatalf("query = %q", got)
	}
	if got := entitySearchQuery(goalEntity{Name: "Erasmus Mundus", Hint: "Европа"}, "Магистратура: стипендия Erasmus Mundus (Европа)", searchParamsNow); got != "Erasmus Mundus Europe scholarship master's" {
		t.Fatalf("query = %q", got)
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
