package app

import (
	"context"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

func (s *Service) RunRuntime(ctx context.Context, id string) (map[string]any, error) {
	if repo, ok := s.repo.(interface {
		RunRuntime(context.Context, string) (map[string]any, error)
	}); ok {
		return repo.RunRuntime(ctx, id)
	}
	return nil, sqlite.ErrNotFound
}
