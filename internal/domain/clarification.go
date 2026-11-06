package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds shared by the clarification model schema, the API and the stored
// user-message metadata.
const (
	MaxClarifyInterpretations    = 4
	MaxClarifyQuestions          = 3
	MaxClarifyOptions            = 4
	MaxClarifyTitleRunes         = 80
	MaxClarifyGoalRunes          = 400
	MaxClarifyQuestionRunes      = 120
	MaxClarifyOptionRunes        = 40
	MaxClarifyAnswerRunes        = 200
	MaxClarifyExtraRunes         = 1000
	ClarificationStatusReady     = "ready"
	ClarificationStatusSkipped   = "unavailable"
	clarificationRefinedGoalLimt = 8 << 10
)

// ClarificationInterpretation is one reading of an ambiguous research request.
type ClarificationInterpretation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Goal  string `json:"goal"`
}

// ClarificationQuestion is a short follow-up question with suggested answers.
type ClarificationQuestion struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// ResearchClarification is the response of the clarify endpoint. Status is
// "unavailable" when the model could not produce valid options; clients then
// submit the original question unchanged.
type ResearchClarification struct {
	Status          string                        `json:"status"`
	Reason          string                        `json:"reason,omitempty"`
	Interpretations []ClarificationInterpretation `json:"interpretations"`
	Questions       []ClarificationQuestion       `json:"questions"`
}

// ClarificationAnswer is one answered follow-up question.
type ClarificationAnswer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// ClarificationChoice is what the user selected before research starts. It is
// user-controlled input and is stored with the user message for display.
type ClarificationChoice struct {
	InterpretationTitle string                `json:"interpretation_title,omitempty"`
	InterpretationGoal  string                `json:"interpretation_goal,omitempty"`
	Answers             []ClarificationAnswer `json:"answers,omitempty"`
	Extra               string                `json:"extra,omitempty"`
}

// Normalize trims every field and drops unanswered questions.
func (c ClarificationChoice) Normalize() ClarificationChoice {
	out := ClarificationChoice{
		InterpretationTitle: oneLine(c.InterpretationTitle),
		InterpretationGoal:  oneLine(c.InterpretationGoal),
		Extra:               strings.TrimSpace(c.Extra),
	}
	for _, answer := range c.Answers {
		a := ClarificationAnswer{Question: oneLine(answer.Question), Answer: oneLine(answer.Answer)}
		if a.Answer != "" {
			out.Answers = append(out.Answers, a)
		}
	}
	return out
}

// Empty reports whether the choice carries nothing that refines the question.
func (c ClarificationChoice) Empty() bool {
	n := c.Normalize()
	return n.InterpretationGoal == "" && n.InterpretationTitle == "" && len(n.Answers) == 0 && n.Extra == ""
}

// Validate checks the bounds of a normalized choice.
func (c ClarificationChoice) Validate() error {
	invalid := func(field string) error {
		return fmt.Errorf("%w: invalid clarification %s", ErrInvalidConversation, field)
	}
	fits := func(value string, limit int) bool {
		return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit
	}
	if !fits(c.InterpretationTitle, MaxClarifyTitleRunes) {
		return invalid("interpretation_title")
	}
	if !fits(c.InterpretationGoal, MaxClarifyGoalRunes) {
		return invalid("interpretation_goal")
	}
	if !fits(c.Extra, MaxClarifyExtraRunes) {
		return invalid("extra")
	}
	if len(c.Answers) > MaxClarifyQuestions {
		return invalid("answers")
	}
	for _, answer := range c.Answers {
		if !fits(answer.Question, MaxClarifyQuestionRunes) || !fits(answer.Answer, MaxClarifyAnswerRunes) || answer.Answer == "" {
			return invalid("answers")
		}
	}
	return nil
}

// RefinedResearchGoal merges the original question with the user's
// clarification. The refined goal comes first so planners and the report
// lead with it; the original question stays verbatim (whitespace collapsed)
// as context. Paragraphs are separated by blank lines.
func RefinedResearchGoal(original string, choice ClarificationChoice) string {
	choice = choice.Normalize()
	original = oneLine(original)
	if choice.Empty() {
		return original
	}
	ru := looksRussian(original + " " + choice.InterpretationGoal + " " + choice.InterpretationTitle)
	label := func(ruText, enText string) string {
		if ru {
			return ruText
		}
		return enText
	}
	var parts []string
	if choice.InterpretationGoal != "" {
		parts = append(parts, label("Уточнённая цель: ", "Refined goal: ")+choice.InterpretationGoal)
	} else if choice.InterpretationTitle != "" {
		parts = append(parts, label("Уточнённая цель: ", "Refined goal: ")+choice.InterpretationTitle)
	}
	parts = append(parts, label("Исходный вопрос: ", "Original question: ")+original)
	if len(choice.Answers) > 0 {
		answers := make([]string, 0, len(choice.Answers))
		for _, answer := range choice.Answers {
			if answer.Question == "" {
				answers = append(answers, answer.Answer)
				continue
			}
			answers = append(answers, strings.TrimRight(answer.Question, " ?:")+label(" — ", ": ")+answer.Answer)
		}
		parts = append(parts, label("Условия пользователя: ", "User constraints: ")+strings.Join(answers, "; ")+".")
	}
	if choice.Extra != "" {
		parts = append(parts, label("Дополнительно: ", "Additional notes: ")+oneLine(choice.Extra))
	}
	goal := strings.Join(parts, "\n\n")
	if len(goal) > clarificationRefinedGoalLimt {
		return original
	}
	return goal
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func looksRussian(text string) bool {
	cyrillic, latin := 0, 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}
	return cyrillic >= 3 && cyrillic*2 >= latin
}
