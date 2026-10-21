package app

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
)

type researchCaptureRepository struct {
	Repository
	ConversationRepository
	conversation domain.Conversation
	loadErr      error
	loads        int
	turns        []domain.SubmitTurn
}

func (r *researchCaptureRepository) GetConversation(context.Context, string) (domain.Conversation, error) {
	r.loads++
	return r.conversation, r.loadErr
}

func (r *researchCaptureRepository) CreateConversation(_ context.Context, conversation domain.Conversation) (domain.Conversation, error) {
	return conversation, nil
}

func (r *researchCaptureRepository) UpdateConversation(context.Context, string, map[string]any) (domain.Conversation, error) {
	return r.conversation, nil
}

func (r *researchCaptureRepository) SubmitTurn(_ context.Context, turn domain.SubmitTurn, _ string) (domain.SubmitResponse, error) {
	r.turns = append(r.turns, turn)
	return domain.SubmitResponse{}, nil
}

func researchTestConfig() config.Config {
	return config.Config{
		ResearchDefaultProfile: domain.ResearchProfileStandard, MaxSteps: 12, MaxSearchQueries: 12,
		MaxFetchBytes: 128 << 20, MaxSourceBytes: 16 << 20,
		PDFPolicy: pdfextract.DefaultPolicy(), PDFDownloadTimeoutSeconds: 120,
		FetchTempDir: "./data/fetch-temp", FetchTempTaskBytes: 128 << 20, FetchTempGlobalBytes: 256 << 20,
		LLMModel: "configured-local-model", LLMBaseURL: "http://127.0.0.1:8000/v1",
		LLMContextLength: 8192, LLMMaxOutputTokens: 4096, LLMTemperature: 0.4, LLMTopP: 0.8,
	}
}

func TestResearchSubmitUsesImmutableSnapshotsAndExplicitZero(t *testing.T) {
	cfg := researchTestConfig()
	policies := cfg.ResearchPolicies()
	model := cfg.ResearchModelSnapshot()
	model.StopSequences = []string{"unexposed-stop"}
	repo := &researchCaptureRepository{conversation: domain.Conversation{
		ID: "conversation", Title: "Research", Mode: "research",
		ModelProfile:     map[string]any{"temperature": 0, "top_p": 0.25, "max_output_tokens": float64(2048)},
		ResearchSettings: map[string]any{"profile": "quick", "time_budget_seconds": float64(21600)},
	}}
	service := New(repo)
	service.SetResearchRunDefaults("deep_laptop", policies, model)
	wantPolicy := policies["quick"]
	wantPolicy.Budget.WallTimeSeconds = 21600
	delete(policies, "quick")
	model.StopSequences[0] = "mutated"
	model.Temperature = 1.7

	submit := func() domain.SubmitTurn {
		t.Helper()
		if _, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "conversation", Content: " question "}, "key"); err != nil {
			t.Fatal(err)
		}
		return repo.turns[len(repo.turns)-1]
	}
	first := submit()
	if first.ResearchPolicy == nil || first.ResearchBudget == nil || first.ModelSnapshot == nil || first.ResearchProfile != "quick" || first.DeadlineSeconds != 21600 || first.Content != "question" {
		t.Fatalf("missing research parameters: %#v", first)
	}
	if *first.ResearchPolicy != wantPolicy || *first.ResearchBudget != wantPolicy.Budget {
		t.Fatalf("effective policy=%#v budget=%#v want=%#v", first.ResearchPolicy, first.ResearchBudget, wantPolicy)
	}
	wantModel := cfg.ResearchModelSnapshot()
	wantModel.Temperature, wantModel.TopP, wantModel.MaxOutputTokens = 0, 0.25, 2048
	if !reflect.DeepEqual(*first.ModelSnapshot, wantModel) {
		t.Fatalf("model=%#v want=%#v", first.ModelSnapshot, wantModel)
	}
	first.ResearchBudget.MaxSteps = 99
	first.ResearchPolicy.PDFPages = 999
	first.ModelSnapshot.BaseURL = "https://mutated.invalid"
	first.ModelSnapshot.StopSequences = append(first.ModelSnapshot.StopSequences, "mutated")
	second := submit()
	if *second.ResearchPolicy != wantPolicy || *second.ResearchBudget != wantPolicy.Budget || !reflect.DeepEqual(*second.ModelSnapshot, wantModel) {
		t.Fatalf("a prior run mutated the defaults: %#v", second)
	}
	if repo.conversation.ModelProfile["temperature"] != 0 || repo.conversation.ResearchSettings["profile"] != "quick" {
		t.Fatal("submit mutated conversation settings")
	}
}

func TestResearchSubmitSelectsConfiguredAndConversationProfiles(t *testing.T) {
	cfg := researchTestConfig()
	for _, override := range []string{"", "quick", "standard", "deep_laptop", "long_laptop", "overnight_laptop"} {
		t.Run(override, func(t *testing.T) {
			settings := map[string]any{}
			wantProfile := "long_laptop"
			if override != "" {
				settings["profile"], wantProfile = override, override
			}
			repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Title", Mode: "research", ResearchSettings: settings}}
			service := New(repo)
			service.SetResearchRunDefaults("long_laptop", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
			if _, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Content: "Question"}, ""); err != nil {
				t.Fatal(err)
			}
			turn := repo.turns[0]
			want, err := cfg.ResearchProfile(wantProfile)
			want.KeepAwake = override == domain.ResearchProfileOvernight
			if err != nil || turn.ResearchProfile != wantProfile || turn.DeadlineSeconds != int(want.Budget.WallTimeSeconds) || *turn.ResearchPolicy != want {
				t.Fatalf("turn=%#v want=%#v err=%v", turn, want, err)
			}
		})
	}
}

func TestSubmitResolvesModeAndPropagatesConversationErrors(t *testing.T) {
	cfg := researchTestConfig()
	for _, test := range []struct {
		conversationMode string
		requestedMode    string
		wantResearch     bool
	}{
		{"research", "", true}, {"chat", "research", true}, {"research", "chat", false}, {"chat", "", false}, {"chat", "chat", false},
	} {
		t.Run(test.conversationMode+"/"+test.requestedMode, func(t *testing.T) {
			repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Title", Mode: test.conversationMode}}
			service := New(repo)
			service.SetResearchRunDefaults("standard", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
			if _, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Content: " Question ", Mode: test.requestedMode, DeadlineSeconds: 777}, ""); err != nil {
				t.Fatal(err)
			}
			turn := repo.turns[0]
			if turn.ModelSnapshot == nil || !reflect.DeepEqual(*turn.ModelSnapshot, cfg.ResearchModelSnapshot()) || turn.Content != "Question" || repo.loads != 1 {
				t.Fatalf("resolved turn=%#v", turn)
			}
			if !test.wantResearch && (turn.ResearchBudget != nil || turn.ResearchPolicy != nil || turn.ResearchProfile != "" || turn.DeadlineSeconds != 777) {
				t.Fatalf("chat received research defaults: %#v", turn)
			}
		})
	}
	for _, mode := range []string{"", "chat", "research"} {
		repo := &researchCaptureRepository{loadErr: errors.New("conversation unavailable")}
		_, err := New(repo).SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "missing", Content: "Question", Mode: mode}, "")
		if !errors.Is(err, repo.loadErr) || len(repo.turns) != 0 {
			t.Fatalf("mode=%q conversation lookup err=%v submitted=%v", mode, err, repo.turns)
		}
	}
}

func TestRepeatLeavesSnapshotCloningToStorage(t *testing.T) {
	for _, operation := range []string{"retry", "continue"} {
		for _, mode := range []string{"", "chat", "research"} {
			repo := &researchCaptureRepository{loadErr: errors.New("unexpected conversation lookup")}
			_, err := New(repo).SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Mode: mode, Operation: operation, SourceRunID: "prior"}, "")
			if err != nil || repo.loads != 0 || len(repo.turns) != 1 {
				t.Fatalf("%s mode=%q err=%v loads=%d turns=%v", operation, mode, err, repo.loads, repo.turns)
			}
			turn := repo.turns[0]
			if turn.ResearchBudget != nil || turn.ResearchPolicy != nil || turn.ModelSnapshot != nil || turn.DeadlineSeconds != 0 || turn.SourceRunID != "prior" {
				t.Fatalf("%s mode=%q replaced prior defaults: %#v", operation, mode, turn)
			}
		}
	}
}

func TestResearchFallbackIsDeterministicAndWellFormed(t *testing.T) {
	wantPolicy := domain.DefaultResearchPolicy()
	configuredPolicy, err := researchTestConfig().ResearchProfile("standard")
	if err != nil || configuredPolicy != wantPolicy {
		t.Fatalf("configured standard=%#v canonical fallback=%#v err=%v", configuredPolicy, wantPolicy, err)
	}
	var snapshots []domain.SubmitTurn
	for i := 0; i < 2; i++ {
		repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Title", Mode: "research"}}
		if _, err := New(repo).SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Content: "Question"}, ""); err != nil {
			t.Fatal(err)
		}
		turn := repo.turns[0]
		if err := config.ValidateResearchPolicy(*turn.ResearchPolicy); err != nil {
			t.Fatal(err)
		}
		if *turn.ResearchPolicy != wantPolicy || *turn.ResearchBudget != wantPolicy.Budget || turn.DeadlineSeconds != 1800 || turn.ResearchBudget.MaxSteps != 12 || turn.ResearchBudget.MaxSearchQueries != 8 || turn.ResearchBudget.MaxFetches != 8 {
			t.Fatalf("fallback policy=%#v budget=%#v deadline=%d; want canonical=%#v", turn.ResearchPolicy, turn.ResearchBudget, turn.DeadlineSeconds, wantPolicy)
		}
		if turn.ResearchProfile != "standard" || turn.ModelSnapshot.Model != "local-qwen" || turn.ModelSnapshot.BaseURL != "" || turn.ModelSnapshot.PromptVersion != "local-research-v1" || turn.ModelSnapshot.SchemaVersion != 1 || turn.ModelSnapshot.StopSequences == nil || len(turn.ModelSnapshot.StopSequences) != 0 {
			t.Fatalf("fallback=%#v", turn)
		}
		snapshots = append(snapshots, turn)
	}
	if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		t.Fatal("default snapshots depend on process state")
	}
}

func TestResearchSettingsAndModelOverridesRejectInvalidValues(t *testing.T) {
	for _, settings := range []map[string]any{
		{"profile": "unknown"}, {"profile": true}, {"time_budget_seconds": 59}, {"time_budget_seconds": 21601},
		{"time_budget_seconds": 60.5}, {"time_budget_seconds": "600"}, {"time_budget_seconds": math.Inf(1)},
		{"time_budget_seconds": math.NaN()}, {"web_enabled": "true"}, {"adaptive_enabled": true},
	} {
		c := domain.Conversation{Title: "Title", Mode: "research", ResearchSettings: settings}
		if err := validateConversation(c); !errors.Is(err, domain.ErrInvalidConversation) {
			t.Fatalf("accepted research settings=%v err=%v", settings, err)
		}
	}
	for _, seconds := range []any{60, int64(21600), float64(3600)} {
		c := domain.Conversation{Title: "Title", Mode: "research", ResearchSettings: map[string]any{"profile": "standard", "time_budget_seconds": seconds, "web_enabled": false}}
		if err := validateConversation(c); err != nil {
			t.Fatalf("valid research settings=%v err=%v", c.ResearchSettings, err)
		}
	}
	cfg := researchTestConfig()
	for _, overrides := range []map[string]any{
		{"temperature": math.NaN()}, {"temperature": 2.1}, {"top_p": 0}, {"top_p": math.Inf(1)},
		{"max_output_tokens": 4097}, {"max_output_tokens": 1.5}, {"model": "arbitrary"},
		{"base_url": "https://remote.invalid"}, {"api_key": "unsupported"}, {"stop_sequences": []string{"stop"}},
	} {
		repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Title", Mode: "research", ModelProfile: overrides}}
		service := New(repo)
		service.SetResearchRunDefaults("standard", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
		if _, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Content: "Question"}, ""); !errors.Is(err, domain.ErrInvalidConversation) || len(repo.turns) != 0 {
			t.Fatalf("accepted overrides=%v err=%v turns=%v", overrides, err, repo.turns)
		}
	}
}

func TestResearchConversationValidationAcceptsProfilesAndRejectsExcessOutput(t *testing.T) {
	ctx := context.Background()
	repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Title", Mode: "research"}}
	service := New(repo)
	for _, profile := range []string{"quick", "standard", "deep_laptop", "long_laptop", "overnight_laptop"} {
		if _, err := service.CreateConversation(ctx, domain.Conversation{Mode: "research", ResearchSettings: map[string]any{"profile": profile}}); err != nil {
			t.Fatalf("valid profile %s rejected by conversation validation: %v", profile, err)
		}
	}
	cfg := researchTestConfig()
	service.SetResearchRunDefaults("standard", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
	for _, maxOutput := range []any{4097, float64(32768)} {
		profile := map[string]any{"max_output_tokens": maxOutput}
		if _, err := service.CreateConversation(ctx, domain.Conversation{Mode: "research", ModelProfile: profile}); !errors.Is(err, domain.ErrInvalidConversation) {
			t.Fatalf("create accepted excess output %v: %v", maxOutput, err)
		}
		if _, err := service.UpdateConversation(ctx, "c", map[string]any{"model_profile": profile}); !errors.Is(err, domain.ErrInvalidConversation) {
			t.Fatalf("update accepted excess output %v: %v", maxOutput, err)
		}
	}
}

func TestResearchDefaultsRejectInvalidTrustedModelAndPolicy(t *testing.T) {
	cfg := researchTestConfig()
	for _, mutate := range []func(*domain.ModelProfileSnapshot){
		func(m *domain.ModelProfileSnapshot) { m.BaseURL = "" },
		func(m *domain.ModelProfileSnapshot) { m.BaseURL = "https://remote.invalid/v1" },
		func(m *domain.ModelProfileSnapshot) { m.BaseURL = "http://user:password@127.0.0.1/v1" },
		func(m *domain.ModelProfileSnapshot) { m.MaxOutputTokens = m.ContextLength },
		func(m *domain.ModelProfileSnapshot) { m.Temperature = math.NaN() },
		func(m *domain.ModelProfileSnapshot) { m.TopP = 0 },
	} {
		model := cfg.ResearchModelSnapshot()
		mutate(&model)
		service := New(nil)
		service.SetResearchRunDefaults("standard", cfg.ResearchPolicies(), model)
		if _, err := service.researchSession(domain.Conversation{Title: "Title", Mode: "research"}); !errors.Is(err, domain.ErrInvalidConversation) {
			t.Fatalf("accepted invalid model snapshot=%#v err=%v", model, err)
		}
	}
	policies := cfg.ResearchPolicies()
	policy := policies["standard"]
	policy.AdaptiveEnabled = true
	policies["standard"] = policy
	service := New(nil)
	service.SetResearchRunDefaults("standard", policies, cfg.ResearchModelSnapshot())
	if _, err := service.researchSession(domain.Conversation{Title: "Title", Mode: "research"}); !errors.Is(err, domain.ErrInvalidConversation) {
		t.Fatalf("accepted adaptive research policy err=%v", err)
	}
}

func TestResearchDefaultsConcurrentReplacementKeepsCoherentCopies(t *testing.T) {
	cfg := researchTestConfig()
	service := New(nil)
	service.SetResearchRunDefaults("quick", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				service.SetResearchRunDefaults("quick", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
				session, err := service.researchSession(domain.Conversation{Title: "Title", Mode: "research"})
				if err != nil || session.Profile != "quick" || session.Budget != session.Policy.Budget {
					t.Errorf("incoherent snapshot=%#v err=%v", session, err)
				}
			}
		}()
	}
	wg.Wait()
}
