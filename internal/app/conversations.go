package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type ConversationRepository interface {
	CreateConversation(context.Context, domain.Conversation) (domain.Conversation, error)
	GetConversation(context.Context, string) (domain.Conversation, error)
	ListConversations(context.Context, string, bool) ([]domain.Conversation, error)
	UpdateConversation(context.Context, string, map[string]any) (domain.Conversation, error)
	DeleteConversation(context.Context, string) ([]string, error)
	ConversationMessages(context.Context, string) ([]domain.ConversationMessage, error)
	SubmitTurn(context.Context, domain.SubmitTurn, string) (domain.SubmitResponse, error)
	GetRun(context.Context, string) (domain.Run, error)
}

func (s *Service) conversationRepo() (ConversationRepository, error) {
	r, ok := s.repo.(ConversationRepository)
	if !ok {
		return nil, fmt.Errorf("conversation storage unavailable")
	}
	return r, nil
}

func validateConversation(c domain.Conversation) error {
	if c.Mode != "chat" && c.Mode != "research" {
		return fmt.Errorf("%w: mode must be chat or research", domain.ErrInvalidConversation)
	}
	if strings.TrimSpace(c.Title) == "" || utf8.RuneCountInString(c.Title) > 200 {
		return fmt.Errorf("%w: title must contain 1 to 200 characters", domain.ErrInvalidConversation)
	}
	for key, value := range c.ModelProfile {
		v, ok := modelProfileNumber(value)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: invalid model profile", domain.ErrInvalidConversation)
		}
		switch key {
		case "temperature":
			if v < 0 || v > 2 {
				return domain.ErrInvalidConversation
			}
		case "top_p":
			if v <= 0 || v > 1 {
				return domain.ErrInvalidConversation
			}
		case "max_output_tokens":
			if v < 1 || v > 32768 || v != math.Trunc(v) {
				return domain.ErrInvalidConversation
			}
		default:
			return fmt.Errorf("%w: unsupported model setting", domain.ErrInvalidConversation)
		}
	}
	for key, value := range c.ResearchSettings {
		switch key {
		case "web_enabled":
			if _, ok := value.(bool); !ok {
				return domain.ErrInvalidConversation
			}
		case "profile":
			profile, ok := value.(string)
			if !ok || !knownResearchProfile(profile) {
				return fmt.Errorf("%w: invalid research profile", domain.ErrInvalidConversation)
			}
		case "time_budget_seconds":
			seconds, ok := researchTimeBudget(value)
			if !ok || seconds < 60 || seconds > 21600 {
				return fmt.Errorf("%w: invalid research time budget", domain.ErrInvalidConversation)
			}
		default:
			return fmt.Errorf("%w: unsupported research setting", domain.ErrInvalidConversation)
		}
	}
	return nil
}

func (s *Service) CreateConversation(ctx context.Context, c domain.Conversation) (domain.Conversation, error) {
	return s.CreateConversationIdempotent(ctx, c, "")
}

func (s *Service) CreateConversationIdempotent(ctx context.Context, c domain.Conversation, key string) (domain.Conversation, error) {
	if len(key) > 200 {
		return c, domain.ErrInvalidConversation
	}
	if c.Mode == "" {
		c.Mode = "chat"
	}
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		c.Title = "Новый чат"
	}
	if c.ModelProfile == nil {
		c.ModelProfile = map[string]any{}
	}
	if c.ResearchSettings == nil {
		c.ResearchSettings = map[string]any{}
	}
	if err := validateConversation(c); err != nil {
		return c, err
	}
	if c.Mode == "research" {
		if _, err := s.researchDefaults().modelSnapshot(c.ModelProfile); err != nil {
			return c, err
		}
	}
	r, err := s.conversationRepo()
	if err != nil {
		return c, err
	}
	if keyed, ok := r.(interface {
		CreateConversationIdempotent(context.Context, domain.Conversation, string) (domain.Conversation, error)
	}); ok {
		return keyed.CreateConversationIdempotent(ctx, c, key)
	}
	if key != "" {
		return c, fmt.Errorf("idempotent conversation storage unavailable")
	}
	return r.CreateConversation(ctx, c)
}
func (s *Service) GetConversation(ctx context.Context, id string) (domain.Conversation, error) {
	r, e := s.conversationRepo()
	if e != nil {
		return domain.Conversation{}, e
	}
	return r.GetConversation(ctx, id)
}
func (s *Service) ListConversations(ctx context.Context, q string, archived bool) ([]domain.Conversation, error) {
	if len(q) > 1000 {
		return nil, domain.ErrInvalidConversation
	}
	r, e := s.conversationRepo()
	if e != nil {
		return nil, e
	}
	return r.ListConversations(ctx, q, archived)
}
func (s *Service) UpdateConversation(ctx context.Context, id string, patch map[string]any) (domain.Conversation, error) {
	c, e := s.GetConversation(ctx, id)
	if e != nil {
		return c, e
	}
	for k, v := range patch {
		switch k {
		case "title":
			s, ok := v.(string)
			if !ok {
				return c, domain.ErrInvalidConversation
			}
			c.Title = strings.TrimSpace(s)
			patch[k] = c.Title
		case "mode":
			s, ok := v.(string)
			if !ok {
				return c, domain.ErrInvalidConversation
			}
			c.Mode = s
		case "archived":
			if _, ok := v.(bool); !ok {
				return c, domain.ErrInvalidConversation
			}
		case "model_profile":
			m, ok := v.(map[string]any)
			if !ok {
				return c, domain.ErrInvalidConversation
			}
			c.ModelProfile = m
		case "research_settings":
			m, ok := v.(map[string]any)
			if !ok {
				return c, domain.ErrInvalidConversation
			}
			c.ResearchSettings = m
		default:
			return c, domain.ErrInvalidConversation
		}
	}
	if e = validateConversation(c); e != nil {
		return c, e
	}
	if c.Mode == "research" {
		if _, e = s.researchDefaults().modelSnapshot(c.ModelProfile); e != nil {
			return c, e
		}
	}
	r, e := s.conversationRepo()
	if e != nil {
		return c, e
	}
	return r.UpdateConversation(ctx, id, patch)
}
func (s *Service) DeleteConversation(ctx context.Context, id string) ([]string, error) {
	r, e := s.conversationRepo()
	if e != nil {
		return nil, e
	}
	return r.DeleteConversation(ctx, id)
}
func (s *Service) ConversationMessages(ctx context.Context, id string) ([]domain.ConversationMessage, error) {
	r, e := s.conversationRepo()
	if e != nil {
		return nil, e
	}
	return r.ConversationMessages(ctx, id)
}
func (s *Service) GetRun(ctx context.Context, id string) (domain.Run, error) {
	r, e := s.conversationRepo()
	if e != nil {
		return domain.Run{}, e
	}
	run, e := r.GetRun(ctx, id)
	if e != nil {
		return run, e
	}
	_, e = r.GetConversation(ctx, run.ConversationID)
	return run, e
}
func (s *Service) SubmitTurn(ctx context.Context, in domain.SubmitTurn, key string) (domain.SubmitResponse, error) {
	if len(key) > 200 {
		return domain.SubmitResponse{}, domain.ErrInvalidConversation
	}
	if in.Operation == "" {
		in.Operation = "send"
	}
	if in.Operation != "send" && in.Operation != "retry" && in.Operation != "continue" {
		return domain.SubmitResponse{}, domain.ErrInvalidConversation
	}
	if in.Mode != "" && in.Mode != "chat" && in.Mode != "research" {
		return domain.SubmitResponse{}, domain.ErrInvalidConversation
	}
	if in.Operation == "send" {
		in.Content = strings.TrimSpace(in.Content)
		if in.Content == "" || len(in.Content) > maxTaskTextBytes || !utf8.ValidString(in.Content) {
			return domain.SubmitResponse{}, domain.ErrInvalidConversation
		}
	}
	r, e := s.conversationRepo()
	if e != nil {
		return domain.SubmitResponse{}, e
	}
	mode := in.Mode
	resolveDefaults := in.Operation == "send"
	if !resolveDefaults {
		var err error
		mode, resolveDefaults, err = repeatRunNeedsDefaults(ctx, r, in)
		if err != nil {
			return domain.SubmitResponse{}, err
		}
	}
	if resolveDefaults {
		conversation, err := r.GetConversation(ctx, in.ConversationID)
		if err != nil {
			return domain.SubmitResponse{}, err
		}
		if mode == "" {
			mode = conversation.Mode
		}
		if in.Operation != "send" {
			conversation.Mode = mode
		}
		if mode == "research" {
			session, err := s.researchSession(conversation)
			if err != nil {
				return domain.SubmitResponse{}, err
			}
			in.DeadlineSeconds = int(session.Budget.WallTimeSeconds)
			in.ResearchProfile = session.Profile
			in.ResearchPolicy = &session.Policy
			in.ResearchBudget = &session.Budget
			in.ModelSnapshot = &session.ModelSnapshot
		} else if mode == "chat" {
			if err := validateConversation(conversation); err != nil {
				return domain.SubmitResponse{}, err
			}
			model, err := s.researchDefaults().modelSnapshot(conversation.ModelProfile)
			if err != nil {
				return domain.SubmitResponse{}, err
			}
			in.ModelSnapshot = &model
		}
	}
	in.Mode = mode
	if in.ResearchRunID != "" {
		if in.Operation != "send" || mode != "chat" || len(in.ResearchRunID) > 128 {
			return domain.SubmitResponse{}, domain.ErrInvalidConversation
		}
		researchContext, err := s.researchContextForRun(ctx, in.ConversationID, in.ResearchRunID)
		if err != nil {
			return domain.SubmitResponse{}, err
		}
		in.ResearchContext = &researchContext
	} else if in.ResearchContext != nil {
		return domain.SubmitResponse{}, domain.ErrInvalidConversation
	}
	return r.SubmitTurn(ctx, in, key)
}

func repeatRunNeedsDefaults(ctx context.Context, repo ConversationRepository, in domain.SubmitTurn) (string, bool, error) {
	sessions, hasSessions := repo.(interface {
		GetResearchSession(context.Context, string) (domain.ResearchSession, error)
	})
	models, hasModels := repo.(interface {
		GetRunModelSnapshot(context.Context, string) (domain.ModelProfileSnapshot, error)
	})
	if !hasSessions && !hasModels {
		return "", false, nil
	}
	source, err := repo.GetRun(ctx, in.SourceRunID)
	if err != nil {
		return "", false, err
	}
	if source.ConversationID != in.ConversationID {
		return "", false, sqlite.ErrConflict
	}
	switch source.Mode {
	case "research":
		if hasSessions {
			_, err = sessions.GetResearchSession(ctx, in.SourceRunID)
		} else if hasModels {
			_, err = models.GetRunModelSnapshot(ctx, in.SourceRunID)
		} else {
			return source.Mode, false, nil
		}
	case "chat":
		if !hasModels {
			return source.Mode, false, nil
		}
		_, err = models.GetRunModelSnapshot(ctx, in.SourceRunID)
	default:
		return "", false, domain.ErrInvalidConversation
	}
	if err != nil && !errors.Is(err, sqlite.ErrNotFound) {
		return source.Mode, false, err
	}
	return source.Mode, errors.Is(err, sqlite.ErrNotFound), nil
}

func (s *Service) CancelRun(ctx context.Context, id string) (domain.Run, error) {
	r, e := s.GetRun(ctx, id)
	if e != nil {
		return r, e
	}
	if r.Status.Terminal() {
		return r, nil
	}
	if _, e = s.CancelTask(ctx, id); e != nil {
		if errors.Is(e, domain.ErrInvalidTransition) || errors.Is(e, sqlite.ErrConflict) {
			current, readErr := s.GetRun(ctx, id)
			if readErr == nil && current.Status.Terminal() {
				return current, nil
			}
		}
		return r, e
	}
	return s.GetRun(ctx, id)
}
func (s *Service) RepeatRun(ctx context.Context, id, operation, key string) (domain.SubmitResponse, error) {
	r, e := s.GetRun(ctx, id)
	if e != nil {
		return domain.SubmitResponse{}, e
	}
	if !r.Status.Terminal() {
		return domain.SubmitResponse{}, sqlite.ErrConflict
	}
	return s.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: r.ConversationID, Operation: operation, SourceRunID: id}, key)
}
