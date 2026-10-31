package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/slager2/qwenchik/internal/domain"
)

type SpaceService interface {
	CreateSpace(context.Context, domain.Space) (domain.Space, error)
	GetSpace(context.Context, string) (domain.Space, error)
	ListSpaces(context.Context, bool) ([]domain.Space, error)
	UpdateSpace(context.Context, string, string, string, *bool) (domain.Space, error)
	DeleteSpace(context.Context, string) error
	SpaceConversations(context.Context, string) ([]domain.Conversation, error)
	SpaceResearchRuns(context.Context, string, int) ([]domain.SpaceResearchRun, error)
	SpaceContextPreview(context.Context, string, int) ([]domain.SpaceContextItem, error)
}

func (s *Server) spaceService(w http.ResponseWriter) (SpaceService, bool) {
	v, ok := s.service.(SpaceService)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "spaces_unavailable", "Хранилище пространств недоступно.", true)
	}
	return v, ok
}

func (s *Server) spaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/spaces", s.listSpaces)
	mux.HandleFunc("POST /api/spaces", s.createSpace)
	mux.HandleFunc("GET /api/spaces/{spaceID}", s.getSpace)
	mux.HandleFunc("PATCH /api/spaces/{spaceID}", s.updateSpace)
	mux.HandleFunc("DELETE /api/spaces/{spaceID}", s.deleteSpace)
	mux.HandleFunc("GET /api/spaces/{spaceID}/conversations", s.spaceConversations)
	mux.HandleFunc("GET /api/spaces/{spaceID}/research", s.spaceResearch)
	mux.HandleFunc("GET /api/spaces/{spaceID}/context", s.spaceContext)
}

func (s *Server) listSpaces(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	archived := false
	if raw := r.URL.Query().Get("archived"); raw != "" {
		var err error
		archived, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный фильтр.", false)
			return
		}
	}
	items, err := v.ListSpaces(r.Context(), archived)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createSpace(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	var in struct {
		Title       string `json:"title"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if decodeRequest(w, r, &in) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректное пространство.", false)
		return
	}
	if in.Title == "" {
		in.Title = in.Name
	}
	item, err := v.CreateSpace(r.Context(), domain.Space{Title: in.Title, Description: in.Description})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/spaces/"+item.ID)
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getSpace(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	item, err := v.GetSpace(r.Context(), r.PathValue("spaceID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateSpace(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	var in struct {
		Title       string `json:"title"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Archived    *bool  `json:"archived"`
	}
	if decodeRequest(w, r, &in) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректное изменение пространства.", false)
		return
	}
	if in.Title == "" {
		in.Title = in.Name
	}
	item, err := v.UpdateSpace(r.Context(), r.PathValue("spaceID"), in.Title, in.Description, in.Archived)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteSpace(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	if err := v.DeleteSpace(r.Context(), r.PathValue("spaceID")); err != nil {
		s.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) spaceConversations(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	items, err := v.SpaceConversations(r.Context(), r.PathValue("spaceID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) spaceResearch(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := v.SpaceResearchRuns(r.Context(), r.PathValue("spaceID"), limit)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) spaceContext(w http.ResponseWriter, r *http.Request) {
	v, ok := s.spaceService(w)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := v.SpaceContextPreview(r.Context(), r.PathValue("spaceID"), limit)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}
