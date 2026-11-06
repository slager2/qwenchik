package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestClarifiedResearchTurnStoresRefinedGoal(t *testing.T) {
	ctx := context.Background()
	s, first := conversationFixture(t, "research")
	if _, err := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	choice := &domain.ClarificationChoice{
		InterpretationTitle: "С нуля",
		InterpretationGoal:  "План самообучения биоинформатике с нуля.",
		Answers:             []domain.ClarificationAnswer{{Question: "Сколько часов в неделю?", Answer: "10"}},
	}
	original := "роадмап по биоинформатике"
	turn := domain.SubmitTurn{ConversationID: first.ConversationID, Content: original, Mode: "research", Clarification: choice}
	result, err := s.SubmitTurn(ctx, turn, "clarified")
	if err != nil {
		t.Fatal(err)
	}
	want := domain.RefinedResearchGoal(original, *choice)
	if !strings.HasPrefix(want, "Уточнённая цель: План самообучения") {
		t.Fatalf("refined goal = %q", want)
	}
	if result.UserMessage.Content != original {
		t.Fatalf("user message content = %q, want the original question", result.UserMessage.Content)
	}
	if result.UserMessage.Metadata["research_goal"] != want || result.UserMessage.Metadata["clarification"] == nil {
		t.Fatalf("metadata = %#v", result.UserMessage.Metadata)
	}
	task, err := s.GetTask(ctx, result.Run.ID)
	if err != nil || task.Description != want {
		t.Fatalf("task description = %q (%v)", task.Description, err)
	}
	session, err := s.GetResearchSession(ctx, result.Run.ID)
	if err != nil || session.Goal != want {
		t.Fatalf("session goal = %q (%v)", session.Goal, err)
	}
	if replay, err := s.SubmitTurn(ctx, turn, "clarified"); err != nil || replay.Run.ID != result.Run.ID {
		t.Fatalf("idempotent replay = %v %v", replay.Run.ID, err)
	}
	changed := turn
	changed.Clarification = &domain.ClarificationChoice{Extra: "другое"}
	if _, err := s.SubmitTurn(ctx, changed, "clarified"); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("changed clarification reused key: %v", err)
	}

	if _, err := s.TransitionTask(ctx, result.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	retry, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Operation: "retry", SourceRunID: result.Run.ID}, "retry-clarified")
	if err != nil {
		t.Fatal(err)
	}
	retryTask, err := s.GetTask(ctx, retry.Run.ID)
	if err != nil || retryTask.Description != want {
		t.Fatalf("retry lost the refined goal: %q (%v)", retryTask.Description, err)
	}
	retrySession, err := s.GetResearchSession(ctx, retry.Run.ID)
	if err != nil || retrySession.Goal != want {
		t.Fatalf("retry session goal = %q (%v)", retrySession.Goal, err)
	}
}

func TestClarificationRejectedOutsideResearchSend(t *testing.T) {
	ctx := context.Background()
	s, first := conversationFixture(t, "chat")
	if _, err := s.TransitionTask(ctx, first.Run.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: first.ConversationID, Content: "hi", Mode: "chat", Clarification: &domain.ClarificationChoice{Extra: "x"}}, "")
	if !errors.Is(err, domain.ErrInvalidConversation) {
		t.Fatalf("chat clarification err = %v", err)
	}
}
