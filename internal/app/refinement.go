package app

import (
	"context"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

func (s *Service) StartRefinement(ctx context.Context, sources []string, key string) (domain.Task, error) {
	if repo, ok := s.repo.(interface {
		StartRefinement(context.Context, domain.ModelProfileSnapshot, []string, string) (domain.Task, error)
	}); ok {
		return repo.StartRefinement(ctx, s.researchDefaults().model, sources, key)
	}
	return domain.Task{}, sqlite.ErrNotFound
}

func (s *Service) BundleCandidate(ctx context.Context, hash string) (domain.BundleCandidate, error) {
	if repo, ok := s.repo.(interface {
		BundleCandidate(context.Context, string) (domain.BundleCandidate, error)
	}); ok {
		return repo.BundleCandidate(ctx, hash)
	}
	return domain.BundleCandidate{}, sqlite.ErrNotFound
}

func (s *Service) RollbackBundle(ctx context.Context, hash string) (domain.AgentBundle, error) {
	model := s.researchDefaults().model
	if model.Bundle == nil {
		return domain.AgentBundle{}, sqlite.ErrConflict
	}
	if repo, ok := s.repo.(interface {
		RollbackBundle(context.Context, string, string) (domain.AgentBundle, error)
	}); ok {
		return repo.RollbackBundle(ctx, model.Bundle.BaseHash(), hash)
	}
	return domain.AgentBundle{}, sqlite.ErrNotFound
}
