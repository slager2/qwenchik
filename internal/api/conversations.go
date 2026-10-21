package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/slager2/qwenchik/internal/domain"
)

type ConversationService interface {
	CreateConversation(context.Context, domain.Conversation) (domain.Conversation, error)
	CreateConversationIdempotent(context.Context, domain.Conversation, string) (domain.Conversation, error)
	GetConversation(context.Context, string) (domain.Conversation, error)
	ListConversations(context.Context, string, bool) ([]domain.Conversation, error)
	UpdateConversation(context.Context, string, map[string]any) (domain.Conversation, error)
	DeleteConversation(context.Context, string) ([]string, error)
	ConversationMessages(context.Context, string) ([]domain.ConversationMessage, error)
	SubmitTurn(context.Context, domain.SubmitTurn, string) (domain.SubmitResponse, error)
	GetRun(context.Context, string) (domain.Run, error)
	CancelRun(context.Context, string) (domain.Run, error)
	RepeatRun(context.Context, string, string, string) (domain.SubmitResponse, error)
}

func (s *Server) conversationService(w http.ResponseWriter) (ConversationService, bool) {
	v, ok := s.service.(ConversationService)
	if !ok {
		writeError(w, 503, "conversations_unavailable", "Хранилище разговоров недоступно.", true)
	}
	return v, ok
}

func (s *Server) conversationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/conversations", s.listConversations)
	mux.HandleFunc("POST /api/conversations", s.createConversation)
	mux.HandleFunc("GET /api/conversations/{conversationID}", s.getConversation)
	mux.HandleFunc("PATCH /api/conversations/{conversationID}", s.updateConversation)
	mux.HandleFunc("DELETE /api/conversations/{conversationID}", s.deleteConversation)
	mux.HandleFunc("GET /api/conversations/{conversationID}/messages", s.conversationMessages)
	mux.HandleFunc("POST /api/conversations/{conversationID}/messages", s.submitConversationMessage)
	mux.HandleFunc("POST /api/runs", s.createRun)
	mux.HandleFunc("GET /api/runs/{runID}", s.getRun)
	mux.HandleFunc("POST /api/runs/{runID}/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/runs/{runID}/retry", func(w http.ResponseWriter, r *http.Request) { s.repeatRun(w, r, "retry") })
	mux.HandleFunc("POST /api/runs/{runID}/continue", func(w http.ResponseWriter, r *http.Request) { s.repeatRun(w, r, "continue") })
	for _, suffix := range []string{"events", "events/stream", "sources", "evidence", "plan"} {
		suffix := suffix
		mux.HandleFunc("GET /api/runs/{runID}/"+suffix, func(w http.ResponseWriter, r *http.Request) { s.runDetail(w, r, suffix) })
	}
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	var in struct {
		Title            string         `json:"title"`
		Mode             string         `json:"mode"`
		ModelProfile     map[string]any `json:"model_profile"`
		ResearchSettings map[string]any `json:"research_settings"`
	}
	if decodeRequest(w, r, &in) != nil {
		writeError(w, 400, "invalid_request", "Некорректный разговор.", false)
		return
	}
	c, e := v.CreateConversationIdempotent(r.Context(), domain.Conversation{Title: in.Title, Mode: in.Mode, ModelProfile: in.ModelProfile, ResearchSettings: in.ResearchSettings}, r.Header.Get("Idempotency-Key"))
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	w.Header().Set("Location", "/api/conversations/"+c.ID)
	writeJSON(w, 201, c)
}
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	archived := false
	if a := r.URL.Query().Get("archived"); a != "" {
		var e error
		archived, e = strconv.ParseBool(a)
		if e != nil {
			writeError(w, 400, "invalid_request", "Некорректный фильтр.", false)
			return
		}
	}
	c, e := v.ListConversations(r.Context(), r.URL.Query().Get("q"), archived)
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	c, e := v.GetConversation(r.Context(), r.PathValue("conversationID"))
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) updateConversation(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	var patch map[string]any
	if decodeRequest(w, r, &patch) != nil || patch == nil {
		writeError(w, 400, "invalid_request", "Некорректное изменение разговора.", false)
		return
	}
	c, e := v.UpdateConversation(r.Context(), r.PathValue("conversationID"), patch)
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	ids, e := v.DeleteConversation(r.Context(), r.PathValue("conversationID"))
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	if s.worker != nil {
		for _, id := range ids {
			s.worker.Interrupt(id)
		}
	}
	w.WriteHeader(204)
}
func (s *Server) conversationMessages(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	m, e := v.ConversationMessages(r.Context(), r.PathValue("conversationID"))
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, m)
}
func (s *Server) submitConversationMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content         string `json:"content"`
		Mode            string `json:"mode"`
		ParentMessageID string `json:"parent_message_id"`
	}
	if decodeRequest(w, r, &in) != nil {
		writeError(w, 400, "invalid_request", "Некорректное сообщение.", false)
		return
	}
	s.submitRun(w, r, domain.SubmitTurn{ConversationID: r.PathValue("conversationID"), Content: in.Content, Mode: in.Mode, ParentMessageID: in.ParentMessageID})
}
func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConversationID  string `json:"conversation_id"`
		Content         string `json:"content"`
		Mode            string `json:"mode"`
		ParentMessageID string `json:"parent_message_id"`
	}
	if decodeRequest(w, r, &in) != nil {
		writeError(w, 400, "invalid_request", "Некорректный запуск.", false)
		return
	}
	s.submitRun(w, r, domain.SubmitTurn{ConversationID: in.ConversationID, Content: in.Content, Mode: in.Mode, ParentMessageID: in.ParentMessageID})
}
func (s *Server) submitRun(w http.ResponseWriter, r *http.Request, in domain.SubmitTurn) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	result, e := v.SubmitTurn(r.Context(), in, r.Header.Get("Idempotency-Key"))
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	if s.worker != nil {
		s.worker.Wake()
	}
	w.Header().Set("Location", "/api/runs/"+result.Run.ID)
	writeJSON(w, 202, result)
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	result, e := v.GetRun(r.Context(), r.PathValue("runID"))
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	id := r.PathValue("runID")
	result, e := v.CancelRun(r.Context(), id)
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	if s.worker != nil && result.Status == domain.StatusCancelled {
		s.worker.Interrupt(id)
	}
	writeJSON(w, 202, result)
}
func (s *Server) repeatRun(w http.ResponseWriter, r *http.Request, operation string) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	result, e := v.RepeatRun(r.Context(), r.PathValue("runID"), operation, r.Header.Get("Idempotency-Key"))
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	if s.worker != nil {
		s.worker.Wake()
	}
	writeJSON(w, 202, result)
}

func (s *Server) runDetail(w http.ResponseWriter, r *http.Request, suffix string) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	id := r.PathValue("runID")
	if _, e := v.GetRun(r.Context(), id); e != nil {
		s.writeServiceError(w, e)
		return
	}
	r.SetPathValue("taskID", id)
	switch suffix {
	case "events":
		s.events(w, r)
	case "events/stream":
		s.streamEvents(w, r)
	case "evidence":
		s.evidence(w, r)
	case "plan":
		t, e := s.service.GetTask(r.Context(), id)
		if e != nil {
			s.writeServiceError(w, e)
			return
		}
		writeJSON(w, 200, t.Plan)
	case "sources":
		s.runSources(w, r, id)
	}
}

func (s *Server) runSources(w http.ResponseWriter, r *http.Request, id string) {
	sources, e := s.sourceProjection(r.Context(), id)
	if e != nil {
		s.writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, sources)
}
