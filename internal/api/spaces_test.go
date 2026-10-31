package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestSpaceLifecycleAndConversationIsolationAPI(t *testing.T) {
	server, store := newTestServer(t)
	defer store.Close()
	created := conversationRequest(t, server.Handler(), http.MethodPost, "/api/spaces", `{"title":"Qwen research","description":"Verified notes"}`, "", http.StatusCreated)
	var space domain.Space
	if err := json.Unmarshal(created.Body.Bytes(), &space); err != nil || space.ID == "" {
		t.Fatalf("space response = %s, err=%v", created.Body.String(), err)
	}
	conversation := conversationRequest(t, server.Handler(), http.MethodPost, "/api/conversations", `{"title":"Scoped","mode":"chat","space_id":"`+space.ID+`"}`, "", http.StatusCreated)
	if !strings.Contains(conversation.Body.String(), `"space_id":"`+space.ID+`"`) {
		t.Fatalf("conversation was not assigned to space: %s", conversation.Body.String())
	}
	conversationRequest(t, server.Handler(), http.MethodGet, "/api/spaces/"+space.ID+"/conversations", "", "", http.StatusOK)
	conversationRequest(t, server.Handler(), http.MethodGet, "/api/spaces/"+space.ID+"/context", "", "", http.StatusOK)
	conversationRequest(t, server.Handler(), http.MethodPatch, "/api/spaces/"+space.ID, `{"description":"Updated"}`, "", http.StatusOK)
	conversationRequest(t, server.Handler(), http.MethodDelete, "/api/spaces/"+space.ID, "", "", http.StatusNoContent)
	conversationRequest(t, server.Handler(), http.MethodGet, "/api/spaces/"+space.ID, "", "", http.StatusNotFound)
}
