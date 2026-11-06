package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestRefinedResearchGoalMergesChoice(t *testing.T) {
	original := "сделай мне роадмап по биоинформатике,\n\nчто я должен знать"
	goal := RefinedResearchGoal(original, ClarificationChoice{
		InterpretationTitle: "С нуля, самостоятельно",
		InterpretationGoal:  "Составить план самообучения биоинформатике с нуля с этапами и сроками.",
		Answers:             []ClarificationAnswer{{Question: "Сколько часов в неделю?", Answer: "10"}, {Question: "Опыт программирования?", Answer: "  "}},
		Extra:               "Интересует  геномика",
	})
	want := "Уточнённая цель: Составить план самообучения биоинформатике с нуля с этапами и сроками.\n\n" +
		"Исходный вопрос: сделай мне роадмап по биоинформатике, что я должен знать\n\n" +
		"Условия пользователя: Сколько часов в неделю — 10.\n\n" +
		"Дополнительно: Интересует геномика"
	if goal != want {
		t.Fatalf("goal =\n%q\nwant\n%q", goal, want)
	}
}

func TestRefinedResearchGoalEmptyChoiceKeepsOriginal(t *testing.T) {
	if got := RefinedResearchGoal("  What is WAL? ", ClarificationChoice{Answers: []ClarificationAnswer{{Question: "x", Answer: " "}}}); got != "What is WAL?" {
		t.Fatalf("got %q", got)
	}
	got := RefinedResearchGoal("What is WAL?", ClarificationChoice{Extra: "focus on SQLite"})
	if !strings.HasPrefix(got, "Original question: What is WAL?") || !strings.HasSuffix(got, "Additional notes: focus on SQLite") {
		t.Fatalf("english labels: %q", got)
	}
}

func TestClarificationChoiceValidateBounds(t *testing.T) {
	ok := ClarificationChoice{InterpretationTitle: "a", InterpretationGoal: "b", Answers: []ClarificationAnswer{{Question: "q", Answer: "a"}}, Extra: "e"}
	if err := ok.Normalize().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]ClarificationChoice{
		"title":   {InterpretationTitle: strings.Repeat("я", MaxClarifyTitleRunes+1)},
		"goal":    {InterpretationGoal: strings.Repeat("g", MaxClarifyGoalRunes+1)},
		"extra":   {Extra: strings.Repeat("e", MaxClarifyExtraRunes+1)},
		"answers": {Answers: []ClarificationAnswer{{Answer: "1"}, {Answer: "2"}, {Answer: "3"}, {Answer: "4"}}},
		"answer":  {Answers: []ClarificationAnswer{{Question: "q", Answer: strings.Repeat("a", MaxClarifyAnswerRunes+1)}}},
	} {
		if err := bad.Normalize().Validate(); !errors.Is(err, ErrInvalidConversation) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
