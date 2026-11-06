package api

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

// Clarifier proposes interpretations and clarifying questions for a research
// request. It must not fail: problems are reported as status "unavailable".
type Clarifier interface {
	Clarify(context.Context, string) domain.ResearchClarification
}

const maxClarifyRequestBytes = 512 << 10

// clarifyConversationMessage is a read-only step before a research send: it
// stores nothing, so it needs no Idempotency-Key. The client submits the
// user's choice with POST .../messages as "clarification"; when this returns
// status "unavailable" the client sends the original question unchanged.
func (s *Server) clarifyConversationMessage(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if decodeRequest(w, r, &in) != nil {
		writeError(w, 400, "invalid_request", "Некорректное сообщение.", false)
		return
	}
	content := strings.TrimSpace(in.Content)
	if content == "" || len(content) > maxClarifyRequestBytes || !utf8.ValidString(content) {
		writeError(w, 400, "invalid_request", "Некорректное сообщение.", false)
		return
	}
	if _, err := v.GetConversation(r.Context(), r.PathValue("conversationID")); err != nil {
		s.writeServiceError(w, err)
		return
	}
	clarifier, ok := s.worker.(Clarifier)
	if !ok {
		writeJSON(w, 200, domain.ResearchClarification{Status: domain.ClarificationStatusSkipped, Reason: "clarifier_unavailable", Interpretations: []domain.ClarificationInterpretation{}, Questions: []domain.ClarificationQuestion{}})
		return
	}
	writeJSON(w, 200, clarifier.Clarify(r.Context(), content))
}
