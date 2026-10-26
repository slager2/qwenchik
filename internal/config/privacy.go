package config

import (
	"errors"
	"github.com/slager2/qwenchik/internal/domain"
)

func (c Config) validatePrivacy() error {
	switch domain.EffectivePrivacyMode(c.ResearchPrivacyMode) {
	case domain.PrivacyExternalSearch:
	case domain.PrivacyLocalSearXNG:
		if c.SearXNGURL == "" {
			return errors.New("local_searxng requires SEARXNG_URL")
		}
	case domain.PrivacyOffline:
		if c.OfflineCorpusPath == "" || len(c.OfflineCorpusSHA256) != 64 {
			return errors.New("offline mode requires a loaded local corpus")
		}
		if c.NotificationChannel == "ntfy" {
			return errors.New("offline mode does not permit ntfy notifications; use a local channel")
		}
	default:
		return errors.New("RESEARCH_PRIVACY_MODE must be local_inference_external_search, local_searxng, or offline")
	}
	return nil
}
