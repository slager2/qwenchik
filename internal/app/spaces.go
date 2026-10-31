package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

// SpaceRepository is intentionally optional: old adapters can continue to
// serve conversations while the spaces feature is unavailable.
type SpaceRepository interface {
	CreateSpace(context.Context, domain.Space) (domain.Space, error)
	GetSpace(context.Context, string) (domain.Space, error)
	ListSpaces(context.Context, bool) ([]domain.Space, error)
	UpdateSpace(context.Context, string, string, string, *bool) (domain.Space, error)
	DeleteSpace(context.Context, string) error
	SpaceConversations(context.Context, string) ([]domain.Conversation, error)
	SpaceResearchRuns(context.Context, string, int) ([]domain.SpaceResearchRun, error)
	SpaceContextPreview(context.Context, string, int) ([]domain.SpaceContextItem, error)
}

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

func (s *Service) spaceRepo() (SpaceRepository, error) {
	r, ok := s.repo.(SpaceRepository)
	if !ok {
		return nil, fmt.Errorf("spaces storage unavailable")
	}
	return r, nil
}

func validateSpace(item domain.Space) error {
	if strings.TrimSpace(item.Title) == "" || utf8.RuneCountInString(item.Title) > 80 {
		return fmt.Errorf("%w: title must contain 1 to 80 characters", domain.ErrInvalidConversation)
	}
	if utf8.RuneCountInString(item.Description) > 280 {
		return fmt.Errorf("%w: description must contain at most 280 characters", domain.ErrInvalidConversation)
	}
	return nil
}

func (s *Service) CreateSpace(ctx context.Context, item domain.Space) (domain.Space, error) {
	item.Title = strings.TrimSpace(item.Title)
	item.Description = strings.TrimSpace(item.Description)
	if err := validateSpace(item); err != nil {
		return item, err
	}
	r, err := s.spaceRepo()
	if err != nil {
		return item, err
	}
	return r.CreateSpace(ctx, item)
}

func (s *Service) GetSpace(ctx context.Context, id string) (domain.Space, error) {
	r, err := s.spaceRepo()
	if err != nil {
		return domain.Space{}, err
	}
	return r.GetSpace(ctx, id)
}

func (s *Service) ListSpaces(ctx context.Context, archived bool) ([]domain.Space, error) {
	r, err := s.spaceRepo()
	if err != nil {
		return nil, err
	}
	return r.ListSpaces(ctx, archived)
}

func (s *Service) UpdateSpace(ctx context.Context, id, title, description string, archived *bool) (domain.Space, error) {
	if utf8.RuneCountInString(strings.TrimSpace(title)) > 80 || utf8.RuneCountInString(strings.TrimSpace(description)) > 280 {
		return domain.Space{}, fmt.Errorf("%w: space field is too long", domain.ErrInvalidConversation)
	}
	r, err := s.spaceRepo()
	if err != nil {
		return domain.Space{}, err
	}
	return r.UpdateSpace(ctx, id, strings.TrimSpace(title), strings.TrimSpace(description), archived)
}

func (s *Service) DeleteSpace(ctx context.Context, id string) error {
	r, err := s.spaceRepo()
	if err != nil {
		return err
	}
	return r.DeleteSpace(ctx, id)
}

func (s *Service) SpaceConversations(ctx context.Context, id string) ([]domain.Conversation, error) {
	r, err := s.spaceRepo()
	if err != nil {
		return nil, err
	}
	return r.SpaceConversations(ctx, id)
}

func (s *Service) SpaceResearchRuns(ctx context.Context, id string, limit int) ([]domain.SpaceResearchRun, error) {
	r, err := s.spaceRepo()
	if err != nil {
		return nil, err
	}
	return r.SpaceResearchRuns(ctx, id, limit)
}

func (s *Service) SpaceContextPreview(ctx context.Context, id string, limit int) ([]domain.SpaceContextItem, error) {
	r, err := s.spaceRepo()
	if err != nil {
		return nil, err
	}
	return r.SpaceContextPreview(ctx, id, limit)
}
