package app

import (
	"context"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestKeepAwakeRequiresTrustedDefaultOrExplicitOvernightSelection(t *testing.T) {
	for _, test := range []struct {
		name, defaultProfile  string
		settings              map[string]any
		global, want, invalid bool
	}{
		{name: "ordinary", defaultProfile: "standard"},
		{name: "environment_overnight", defaultProfile: "overnight_laptop"},
		{name: "explicit_overnight", defaultProfile: "standard", settings: map[string]any{"profile": "overnight_laptop"}, want: true},
		{name: "trusted_global", defaultProfile: "standard", global: true, want: true},
		{name: "wrong_type", defaultProfile: "standard", settings: map[string]any{"profile": true}, invalid: true},
		{name: "arbitrary_keep_awake", defaultProfile: "standard", settings: map[string]any{"keep_awake": true}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := researchTestConfig()
			repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Research", Mode: "research", ResearchSettings: test.settings}}
			service := New(repo)
			service.SetResearchRunDefaults(test.defaultProfile, cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
			service.SetKeepAwakeDefault(test.global)
			_, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Content: "Question"}, "")
			if test.invalid {
				if err == nil {
					t.Fatal("untrusted setting accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if repo.turns[0].ResearchPolicy.KeepAwake != test.want {
				t.Fatal("incorrect sleep policy snapshot")
			}
		})
	}
}
