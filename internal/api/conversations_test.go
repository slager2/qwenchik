package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

type conversationWorker struct {
	interrupted []string
	wakes       int
}

func (w *conversationWorker) Wake()               { w.wakes++ }
func (w *conversationWorker) Interrupt(id string) { w.interrupted = append(w.interrupted, id) }

func conversationRequest(t *testing.T, h http.Handler, method, path, body, key string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s: %d want %d body=%s", method, path, rec.Code, want, rec.Body.String())
	}
	return rec
}

func TestConversationAPICRUDPartialRetryContinueAndReplay(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	worker := &conversationWorker{}
	server.worker = worker
	h := server.Handler()
	rec := conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Local","mode":"chat"}`, "", 201)
	var c domain.Conversation
	if e := json.Unmarshal(rec.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	base := "/api/conversations/" + c.ID
	conversationRequest(t, h, "PATCH", base, `{"title":"Renamed"}`, "", 200)
	conversationRequest(t, h, "GET", "/api/conversations?q=Renamed", "", "", 200)
	rec = conversationRequest(t, h, "POST", base+"/messages", `{"content":"Question"}`, "submit", 202)
	var result domain.SubmitResponse
	if e := json.Unmarshal(rec.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	conversationRequest(t, h, "POST", base+"/messages", `{"content":"Question"}`, "submit", 202)
	conversationRequest(t, h, "POST", base+"/messages", `{"content":"Changed"}`, "submit", 409)
	conversationRequest(t, h, "POST", base+"/messages", `{"content":"Concurrent"}`, "", 409)
	runPath := "/api/runs/" + result.Run.ID
	if _, ok, e := store.ClaimNext(ctx); e != nil || !ok {
		t.Fatal(e)
	}
	for _, status := range []domain.TaskStatus{domain.StatusRunning, domain.StatusVerifying} {
		if _, e := store.TransitionTask(ctx, result.Run.ID, status, nil, "", "user", nil); e != nil {
			t.Fatal(e)
		}
	}
	delta, e := store.RecordMessageDelta(ctx, result.Run.ID, "stream", "research-random-id", 0, "Durable partial")
	if e != nil {
		t.Fatal(e)
	}
	conversationRequest(t, h, "POST", runPath+"/cancel", "", "", 202)
	rec = conversationRequest(t, h, "GET", base+"/messages", "", "", 200)
	if !strings.Contains(rec.Body.String(), "Durable partial") || !strings.Contains(rec.Body.String(), `"status":"cancelled"`) {
		t.Fatal(rec.Body.String())
	}
	rec = conversationRequest(t, h, "GET", fmt.Sprintf("%s/events/stream?after=%d", runPath, delta.Seq), "", "", 200)
	if strings.Contains(rec.Body.String(), "message.delta") || !strings.Contains(rec.Body.String(), "task.cancelled") {
		t.Fatal(rec.Body.String())
	}
	conversationRequest(t, h, "GET", runPath+"/events/stream?after=bad", "", "", 400)
	rec = conversationRequest(t, h, "POST", runPath+"/retry", "", "retry", 202)
	var retry domain.SubmitResponse
	if e = json.Unmarshal(rec.Body.Bytes(), &retry); e != nil {
		t.Fatal(e)
	}
	if retry.UserMessage.ID != result.UserMessage.ID {
		t.Fatal("duplicated user")
	}
	conversationRequest(t, h, "POST", "/api/runs/"+retry.Run.ID+"/cancel", "", "", 202)
	conversationRequest(t, h, "POST", runPath+"/continue", "", "", 409)
	conversationRequest(t, h, "POST", "/api/runs/"+retry.Run.ID+"/continue", "", "", 409)
	rec = conversationRequest(t, h, "POST", "/api/runs", `{"conversation_id":"`+c.ID+`","content":"Next"}`, "", 202)
	var next domain.SubmitResponse
	json.Unmarshal(rec.Body.Bytes(), &next)
	conversationRequest(t, h, "DELETE", base, "", "", 204)
	conversationRequest(t, h, "GET", base, "", "", 404)
	conversationRequest(t, h, "GET", "/api/runs/"+next.Run.ID, "", "", 404)
	if len(worker.interrupted) != 3 || worker.interrupted[2] != next.Run.ID {
		t.Fatalf("interrupts %#v", worker.interrupted)
	}
}

func TestConversationAPIValidationAndContinue(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	h := server.Handler()
	for _, body := range []string{`{"mode":"cloud"}`, `{"model_profile":{"api_key":"secret"}}`, `{"research_settings":{"token":"secret"}}`, `{"model_profile":{"temperature":4}}`, `{"unrecognized":true}`} {
		rec := conversationRequest(t, h, "POST", "/api/conversations", body, "", 400)
		if strings.Contains(rec.Body.String(), "secret") {
			t.Fatal("sensitive input echoed")
		}
	}
	c, e := store.CreateConversation(ctx, domain.Conversation{Title: "Continue", Mode: "chat"})
	if e != nil {
		t.Fatal(e)
	}
	turn, e := store.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: c.ID, Content: "Question"}, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, ok, e := store.ClaimNext(ctx); e != nil || !ok {
		t.Fatal(e)
	}
	for _, status := range []domain.TaskStatus{domain.StatusRunning, domain.StatusVerifying} {
		if _, e = store.TransitionTask(ctx, turn.Run.ID, status, nil, "", "user", nil); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = store.RecordMessageDelta(ctx, turn.Run.ID, "s", "random", 0, "Keep this"); e != nil {
		t.Fatal(e)
	}
	conversationRequest(t, h, "POST", "/api/runs/"+turn.Run.ID+"/cancel", "", "", 202)
	rec := conversationRequest(t, h, "POST", "/api/runs/"+turn.Run.ID+"/continue", "", "continue", 202)
	var continued domain.SubmitResponse
	json.Unmarshal(rec.Body.Bytes(), &continued)
	if continued.Run.ContinueFromMessageID != turn.AssistantMessage.ID || continued.AssistantMessage.ParentMessageID != turn.AssistantMessage.ID {
		t.Fatal(rec.Body.String())
	}
	conversationRequest(t, h, "GET", "/api/runs/"+continued.Run.ID+"/sources", "", "", 200)
}

func TestConversationAPIConcurrentSubmitAndSafeSourceProjection(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	h := server.Handler()
	c, e := store.CreateConversation(ctx, domain.Conversation{Title: "Concurrent", Mode: "research"})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/conversations/"+c.ID+"/messages", strings.NewReader(`{"content":"race"}`))
			req.Host = "127.0.0.1:8080"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			results <- rec
		}()
	}
	wg.Wait()
	close(results)
	var turn domain.SubmitResponse
	success, conflict := 0, 0
	for rec := range results {
		switch rec.Code {
		case 202:
			success++
			if e = json.Unmarshal(rec.Body.Bytes(), &turn); e != nil {
				t.Fatal(e)
			}
		case 409:
			conflict++
		default:
			t.Fatalf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success %d conflict %d", success, conflict)
	}
	for _, event := range []struct {
		visibility string
		url        string
	}{{"user", "https://example.org/source"}, {"user", "https://user:credential@example.org/private"}, {"internal", "https://example.org/internal"}} {
		if _, e = store.RecordEvent(ctx, turn.Run.ID, "search.hit", nil, event.visibility, map[string]any{"url": event.url, "title": "Source", "provider": "fixture", "api_key": "fixture-secret", "raw_body": "private"}); e != nil {
			t.Fatal(e)
		}
	}
	rec := conversationRequest(t, h, "GET", "/api/runs/"+turn.Run.ID+"/sources", "", "", 200)
	body := rec.Body.String()
	if !strings.Contains(body, "https://example.org/source") || strings.Contains(body, "fixture-secret") || strings.Contains(body, "credential") || strings.Contains(body, "/internal") || strings.Contains(body, "raw_body") {
		t.Fatal(body)
	}
}
