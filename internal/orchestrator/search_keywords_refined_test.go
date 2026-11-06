package orchestrator

import (
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

// Regression: the user-language query of a clarified run started with the
// refined-goal label: "Уточнённая цель Предложить пошаговый маршрут ...".
func TestGoalKeywordQueryDropsRefinedGoalLabels(t *testing.T) {
	goal := domain.RefinedResearchGoal("сделай роадмап по биоинформатике", domain.ClarificationChoice{
		InterpretationTitle: "Самообучение",
		InterpretationGoal:  "Предложить пошаговый маршрут самостоятельного изучения биоинформатики",
		Answers:             []domain.ClarificationAnswer{{Question: "Ваш уровень?", Answer: "Начинающий"}},
	})
	query := strings.ToLower(goalKeywordQuery(goal, 8))
	for _, label := range []string{"уточнённая", "цель", "исходный", "вопрос", "условия", "пользователя", "предложить"} {
		for _, word := range strings.Fields(query) {
			if word == label {
				t.Fatalf("query %q keeps label word %q", query, label)
			}
		}
	}
	if !strings.Contains(query, "маршрут") || !strings.Contains(query, "биоинформатики") {
		t.Fatalf("query %q lost the goal keywords", query)
	}
}
