package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

type clarifyingWorker struct {
	conversationWorker
	questions []string
}

func (w *clarifyingWorker) Clarify(_ context.Context, question string) domain.ResearchClarification {
	w.questions = append(w.questions, question)
	return domain.ResearchClarification{
		Status:          domain.ClarificationStatusReady,
		Interpretations: []domain.ClarificationInterpretation{{ID: "i1", Title: "С нуля", Goal: "План с нуля."}},
		Questions:       []domain.ClarificationQuestion{{ID: "q1", Question: "Сколько часов в неделю?", Options: []string{"5", "10"}}},
	}
}

func TestClarifyEndpointAndClarifiedSubmit(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	worker := &clarifyingWorker{}
	server.worker = worker
	h := server.Handler()
	rec := conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Roadmap","mode":"research"}`, "", 201)
	var c domain.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	base := "/api/conversations/" + c.ID
	conversationRequest(t, h, "POST", base+"/clarify", `{"content":"  "}`, "", 400)
	conversationRequest(t, h, "POST", base+"/clarify", `{"content":"x","extra":1}`, "", 400)
	conversationRequest(t, h, "POST", "/api/conversations/missing/clarify", `{"content":"x"}`, "", 404)
	rec = conversationRequest(t, h, "POST", base+"/clarify", `{"content":" роадмап по биоинформатике "}`, "", 200)
	var clarification domain.ResearchClarification
	if err := json.Unmarshal(rec.Body.Bytes(), &clarification); err != nil {
		t.Fatal(err)
	}
	if clarification.Status != "ready" || len(clarification.Interpretations) != 1 || len(worker.questions) != 1 || worker.questions[0] != "роадмап по биоинформатике" {
		t.Fatalf("clarification = %s questions=%q", rec.Body.String(), worker.questions)
	}
	messages, err := store.ConversationMessages(ctx, c.ID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("clarify wrote messages: %d %v", len(messages), err)
	}
	body := `{"content":"роадмап по биоинформатике","mode":"research","clarification":{"interpretation_title":"С нуля","interpretation_goal":"План с нуля.","answers":[{"question":"Сколько часов в неделю?","answer":"10"}],"extra":"геномика"}}`
	conversationRequest(t, h, "POST", base+"/messages", `{"content":"x","mode":"research","clarification":{"bogus":1}}`, "", 400)
	rec = conversationRequest(t, h, "POST", base+"/messages", body, "clarified-submit", 202)
	var result domain.SubmitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(ctx, result.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "Уточнённая цель: План с нуля.\n\nИсходный вопрос: роадмап по биоинформатике\n\nУсловия пользователя: Сколько часов в неделю — 10.\n\nДополнительно: геномика"
	if task.Description != want || result.UserMessage.Content != "роадмап по биоинформатике" || result.UserMessage.Metadata["research_goal"] != want {
		t.Fatalf("task=%q message=%#v", task.Description, result.UserMessage)
	}
	conversationRequest(t, h, "POST", base+"/messages", body, "clarified-submit", 202)
}

func TestClarifyEndpointWithoutClarifierFallsBack(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	h := server.Handler()
	rec := conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Roadmap","mode":"research"}`, "", 201)
	var c domain.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	rec = conversationRequest(t, h, "POST", "/api/conversations/"+c.ID+"/clarify", `{"content":"What is WAL?"}`, "", 200)
	if rec.Body.String() != `{"status":"unavailable","reason":"clarifier_unavailable","interpretations":[],"questions":[]}`+"\n" {
		t.Fatalf("fallback = %q", rec.Body.String())
	}
}
