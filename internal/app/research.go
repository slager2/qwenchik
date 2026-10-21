package app

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
)

type researchRunDefaults struct {
	profile            string
	policies           map[string]domain.ResearchPolicy
	model              domain.ModelProfileSnapshot
	allowEmptyEndpoint bool
}

func (s *Service) SetResearchRunDefaults(defaultProfile string, policies map[string]domain.ResearchPolicy, model domain.ModelProfileSnapshot) {
	copies := make(map[string]domain.ResearchPolicy, len(policies))
	for name, policy := range policies {
		copies[name] = policy
	}
	model.StopSequences = []string{}
	s.researchMu.Lock()
	s.researchRunDefaults = &researchRunDefaults{profile: defaultProfile, policies: copies, model: model}
	s.researchMu.Unlock()
}

func (s *Service) researchDefaults() researchRunDefaults {
	s.researchMu.RLock()
	defer s.researchMu.RUnlock()
	if defaults := s.researchRunDefaults; defaults != nil {
		return *defaults
	}
	policy := defaultResearchPolicy()
	return researchRunDefaults{
		profile:  domain.ResearchProfileStandard,
		policies: map[string]domain.ResearchPolicy{domain.ResearchProfileStandard: policy},
		model: domain.ModelProfileSnapshot{
			Version: 1, Model: "local-qwen", ContextLength: 131072, MaxOutputTokens: 4096,
			Temperature: 0.2, TopP: 0.8, StopSequences: []string{},
			PromptVersion: "local-research-v1", SchemaVersion: 1,
		},
		allowEmptyEndpoint: true,
	}
}

func defaultResearchPolicy() domain.ResearchPolicy {
	return domain.DefaultResearchPolicy()
}

func knownResearchProfile(profile string) bool {
	_, ok := domain.ResearchBudgetForProfile(profile)
	return ok
}

func researchTimeBudget(value any) (int64, bool) {
	switch seconds := value.(type) {
	case int:
		return int64(seconds), seconds >= 60 && seconds <= 21600
	case int64:
		return seconds, seconds >= 60 && seconds <= 21600
	case float64:
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds != math.Trunc(seconds) || seconds < 60 || seconds > 21600 {
			return 0, false
		}
		return int64(seconds), true
	default:
		return 0, false
	}
}

func modelProfileNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

func validateResearchModel(model domain.ModelProfileSnapshot, allowEmptyEndpoint bool) error {
	if model.Version != 1 || strings.TrimSpace(model.Model) == "" || model.PromptVersion != "local-research-v1" || model.SchemaVersion != 1 {
		return fmt.Errorf("%w: invalid research model snapshot", domain.ErrInvalidConversation)
	}
	if model.ContextLength < 2305 || model.MaxOutputTokens < 1 || model.MaxOutputTokens > model.ContextLength-2304 {
		return fmt.Errorf("%w: research model output must fit the configured context and prompt reserve", domain.ErrInvalidConversation)
	}
	if math.IsNaN(model.Temperature) || math.IsInf(model.Temperature, 0) || model.Temperature < 0 || model.Temperature > 2 || math.IsNaN(model.TopP) || math.IsInf(model.TopP, 0) || model.TopP <= 0 || model.TopP > 1 {
		return fmt.Errorf("%w: invalid research model sampling settings", domain.ErrInvalidConversation)
	}
	if allowEmptyEndpoint && model.BaseURL == "" {
		return nil
	}
	u, err := url.Parse(model.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: research model endpoint must be a credential-free local URL", domain.ErrInvalidConversation)
	}
	ip := net.ParseIP(u.Hostname())
	if !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("%w: research model endpoint must use loopback", domain.ErrInvalidConversation)
	}
	return nil
}

func (s *Service) researchSession(conversation domain.Conversation) (domain.ResearchSession, error) {
	if err := validateConversation(conversation); err != nil {
		return domain.ResearchSession{}, err
	}
	defaults := s.researchDefaults()
	profile := defaults.profile
	if configured, ok := conversation.ResearchSettings["profile"].(string); ok {
		profile = configured
	}
	policy, ok := defaults.policies[profile]
	if !ok || policy.Profile != profile {
		return domain.ResearchSession{}, fmt.Errorf("%w: research profile is unavailable", domain.ErrInvalidConversation)
	}
	if err := config.ValidateResearchPolicy(policy); err != nil {
		return domain.ResearchSession{}, fmt.Errorf("%w: %v", domain.ErrInvalidConversation, err)
	}
	if configured, ok := conversation.ResearchSettings["time_budget_seconds"]; ok {
		seconds, _ := researchTimeBudget(configured)
		policy.Budget.WallTimeSeconds = seconds
	}
	model, err := defaults.modelSnapshot(conversation.ModelProfile)
	if err != nil {
		return domain.ResearchSession{}, err
	}
	return domain.ResearchSession{Profile: profile, Policy: policy, Budget: policy.Budget, ModelSnapshot: model}, nil
}

func (defaults researchRunDefaults) modelSnapshot(overrides map[string]any) (domain.ModelProfileSnapshot, error) {
	model := defaults.model
	model.StopSequences = []string{}
	if err := validateResearchModel(model, defaults.allowEmptyEndpoint); err != nil {
		return domain.ModelProfileSnapshot{}, err
	}
	for key, raw := range overrides {
		value, _ := modelProfileNumber(raw)
		switch key {
		case "temperature":
			model.Temperature = value
		case "top_p":
			model.TopP = value
		case "max_output_tokens":
			if value > float64(model.MaxOutputTokens) || value > float64(model.ContextLength-2304) {
				return domain.ModelProfileSnapshot{}, fmt.Errorf("%w: max_output_tokens exceeds the configured research output limit", domain.ErrInvalidConversation)
			}
			model.MaxOutputTokens = int(value)
		}
	}
	return model, nil
}
