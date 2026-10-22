package api

import (
	"context"
	"github.com/slager2/qwenchik/internal/domain"
	"net/http"
)

type refinementService interface {
	StartRefinement(context.Context, []string, string) (domain.Task, error)
	BundleCandidate(context.Context, string) (domain.BundleCandidate, error)
	RollbackBundle(context.Context, string) (domain.AgentBundle, error)
}

func (s *Server) startRefinement(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(refinementService)
	if !ok {
		writeError(w, 503, "refinement_unavailable", "Refinement недоступен.", false)
		return
	}
	var input struct {
		Sources []string `json:"source_task_ids"`
	}
	if decodeRequest(w, r, &input) != nil || r.Header.Get("Idempotency-Key") == "" {
		writeError(w, 400, "invalid_request", "Нужны source_task_ids и Idempotency-Key.", false)
		return
	}
	task, err := service.StartRefinement(r.Context(), input.Sources, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if s.worker != nil {
		s.worker.Wake()
	}
	writeJSON(w, 202, task)
}

func (s *Server) bundleCandidate(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(refinementService)
	if !ok {
		writeError(w, 503, "refinement_unavailable", "Refinement недоступен.", false)
		return
	}
	candidate, err := service.BundleCandidate(r.Context(), r.PathValue("hash"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, 200, candidate)
}

func (s *Server) rollbackBundle(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(refinementService)
	if !ok {
		writeError(w, 503, "refinement_unavailable", "Refinement недоступен.", false)
		return
	}
	var input struct {
		Hash string `json:"bundle_hash"`
	}
	if decodeRequest(w, r, &input) != nil || !validFingerprint(input.Hash) {
		writeError(w, 400, "invalid_request", "Нужен bundle_hash ранее одобренной версии.", false)
		return
	}
	bundle, err := service.RollbackBundle(r.Context(), input.Hash)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if worker, ok := s.worker.(interface{ ApplyApprovedBundle(domain.AgentBundle) }); ok {
		worker.ApplyApprovedBundle(bundle)
	}
	writeJSON(w, 200, bundle)
}
