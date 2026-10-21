package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type snapshotAwareRunRepository struct {
	*researchCaptureRepository
	run           domain.Run
	runErr        error
	session       domain.ResearchSession
	sessionErr    error
	model         domain.ModelProfileSnapshot
	modelErr      error
	runReads      int
	sessionReads  int
	modelReads    int
	snapshotRunID string
}

func (r *snapshotAwareRunRepository) GetRun(_ context.Context, id string) (domain.Run, error) {
	r.runReads++
	if id != r.run.ID {
		return domain.Run{}, sqlite.ErrNotFound
	}
	return r.run, r.runErr
}

func (r *snapshotAwareRunRepository) GetResearchSession(_ context.Context, id string) (domain.ResearchSession, error) {
	r.sessionReads++
	r.snapshotRunID = id
	return r.session, r.sessionErr
}

func (r *snapshotAwareRunRepository) GetRunModelSnapshot(_ context.Context, id string) (domain.ModelProfileSnapshot, error) {
	r.modelReads++
	r.snapshotRunID = id
	return r.model, r.modelErr
}

func legacyRunDefaultsRepository(mode string) *snapshotAwareRunRepository {
	conversationMode := "chat"
	if mode == "chat" {
		conversationMode = "research"
	}
	return &snapshotAwareRunRepository{
		researchCaptureRepository: &researchCaptureRepository{conversation: domain.Conversation{
			ID: "c", Title: "Legacy conversation", Mode: conversationMode,
			ModelProfile:     map[string]any{"temperature": 0, "top_p": 0.25, "max_output_tokens": float64(2048)},
			ResearchSettings: map[string]any{"profile": "deep_laptop", "time_budget_seconds": float64(3600)},
		}},
		run:        domain.Run{ID: "legacy", ConversationID: "c", Mode: mode, Status: domain.StatusCompleted},
		sessionErr: fmt.Errorf("legacy research snapshot: %w", sqlite.ErrNotFound),
		modelErr:   fmt.Errorf("legacy model snapshot: %w", sqlite.ErrNotFound),
	}
}

func TestLegacyRepeatResolvesTrustedDefaultsUsingSourceMode(t *testing.T) {
	cfg := researchTestConfig()
	cfg.LLMContextLength = 32768
	cfg.PDFPolicy.MaxInputBytes, cfg.PDFPolicy.MaxPages = 32<<20, 10
	for _, sourceMode := range []string{"research", "chat"} {
		for _, operation := range []string{"retry", "continue"} {
			for _, requestedMode := range []string{"", "chat", "research"} {
				t.Run(sourceMode+"/"+operation+"/"+requestedMode, func(t *testing.T) {
					repo := legacyRunDefaultsRepository(sourceMode)
					service := New(repo)
					policies := cfg.ResearchPolicies()
					if sourceMode == "chat" {
						policies = nil
					}
					service.SetResearchRunDefaults("standard", policies, cfg.ResearchModelSnapshot())
					_, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{
						ConversationID: "c", SourceRunID: "legacy", Operation: operation, Mode: requestedMode,
					}, "")
					if err != nil || len(repo.turns) != 1 {
						t.Fatalf("legacy %s mode=%q: err=%v turns=%v", operation, requestedMode, err, repo.turns)
					}
					turn := repo.turns[0]
					wantModel := cfg.ResearchModelSnapshot()
					wantModel.Temperature, wantModel.TopP, wantModel.MaxOutputTokens = 0, 0.25, 2048
					if turn.ModelSnapshot == nil || !reflect.DeepEqual(*turn.ModelSnapshot, wantModel) || turn.SourceRunID != "legacy" || turn.Operation != operation {
						t.Fatalf("legacy snapshot=%#v want=%#v turn=%#v", turn.ModelSnapshot, wantModel, turn)
					}
					if repo.runReads != 1 || repo.loads != 1 || repo.snapshotRunID != "legacy" {
						t.Fatalf("source reads=%d conversation reads=%d snapshot run=%q", repo.runReads, repo.loads, repo.snapshotRunID)
					}
					if sourceMode == "research" {
						wantPolicy, err := cfg.ResearchProfile("deep_laptop")
						if err != nil {
							t.Fatal(err)
						}
						wantPolicy.Budget.WallTimeSeconds = 3600
						if turn.ResearchProfile != "deep_laptop" || turn.ResearchPolicy == nil || *turn.ResearchPolicy != wantPolicy || turn.ResearchBudget == nil || *turn.ResearchBudget != wantPolicy.Budget || turn.DeadlineSeconds != 3600 || repo.sessionReads != 1 || repo.modelReads != 0 {
							t.Fatalf("legacy research did not receive effective app policy: %#v", turn)
						}
					} else if turn.ResearchProfile != "" || turn.ResearchPolicy != nil || turn.ResearchBudget != nil || turn.DeadlineSeconds != 0 || repo.sessionReads != 0 || repo.modelReads != 1 {
						t.Fatalf("legacy chat received research defaults: %#v", turn)
					}
					if repo.conversation.Mode == sourceMode || !errors.Is(repo.sessionErr, sqlite.ErrNotFound) || !errors.Is(repo.modelErr, sqlite.ErrNotFound) {
						t.Fatal("legacy source or conversation was mutated while preparing a new run")
					}
				})
			}
		}
	}
}

func TestLegacyResearchRepeatUsesConfiguredDefaultProfile(t *testing.T) {
	cfg := researchTestConfig()
	cfg.LLMContextLength = 32768
	repo := legacyRunDefaultsRepository("research")
	repo.conversation.ModelProfile = nil
	repo.conversation.ResearchSettings = nil
	service := New(repo)
	service.SetResearchRunDefaults("quick", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
	if _, err := service.RepeatRun(context.Background(), "legacy", "retry", "retry"); err != nil {
		t.Fatal(err)
	}
	turn := repo.turns[0]
	wantPolicy, err := cfg.ResearchProfile("quick")
	if err != nil || turn.ResearchPolicy == nil || *turn.ResearchPolicy != wantPolicy || turn.ResearchBudget == nil || *turn.ResearchBudget != wantPolicy.Budget || turn.ResearchProfile != "quick" || turn.DeadlineSeconds != 300 || turn.ModelSnapshot == nil || !reflect.DeepEqual(*turn.ModelSnapshot, cfg.ResearchModelSnapshot()) {
		t.Fatalf("legacy retry ignored configured defaults: turn=%#v policy=%#v err=%v", turn, wantPolicy, err)
	}
}

func TestRepeatWithSnapshotSkipsCurrentDefaultsAndConversationOverrides(t *testing.T) {
	priorConfig := researchTestConfig()
	priorPolicy, err := priorConfig.ResearchProfile("quick")
	if err != nil {
		t.Fatal(err)
	}
	currentConfig := priorConfig
	currentConfig.LLMContextLength, currentConfig.LLMMaxOutputTokens = 4096, 1024
	currentConfig.LLMModel = "replacement-local-model"
	for _, sourceMode := range []string{"research", "chat"} {
		for _, operation := range []string{"retry", "continue"} {
			t.Run(sourceMode+"/"+operation, func(t *testing.T) {
				repo := legacyRunDefaultsRepository(sourceMode)
				repo.loadErr = errors.New("current conversation settings must not be loaded for a snapshot clone")
				repo.conversation.ModelProfile = map[string]any{"max_output_tokens": 32768}
				repo.session = domain.ResearchSession{Profile: "quick", Policy: priorPolicy, Budget: priorPolicy.Budget, ModelSnapshot: priorConfig.ResearchModelSnapshot()}
				repo.model = priorConfig.ResearchModelSnapshot()
				repo.sessionErr, repo.modelErr = nil, nil
				service := New(repo)
				service.SetResearchRunDefaults("long_laptop", currentConfig.ResearchPolicies(), currentConfig.ResearchModelSnapshot())
				_, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", SourceRunID: "legacy", Operation: operation, Mode: repo.conversation.Mode}, "")
				if err != nil || len(repo.turns) != 1 || repo.loads != 0 {
					t.Fatalf("existing snapshot clone consulted current settings: err=%v turns=%v loads=%d", err, repo.turns, repo.loads)
				}
				turn := repo.turns[0]
				if turn.SourceRunID != "legacy" || turn.ModelSnapshot != nil || turn.ResearchPolicy != nil || turn.ResearchBudget != nil || turn.ResearchProfile != "" || turn.DeadlineSeconds != 0 {
					t.Fatalf("existing snapshot clone received replacement settings: %#v", turn)
				}
				if sourceMode == "research" && (repo.sessionReads != 1 || repo.modelReads != 0) || sourceMode == "chat" && (repo.sessionReads != 0 || repo.modelReads != 1) {
					t.Fatalf("wrong source snapshot lookup: sessions=%d models=%d", repo.sessionReads, repo.modelReads)
				}
			})
		}
	}
}

func TestLegacyRepeatValidatesCurrentModelBeforeSubmission(t *testing.T) {
	cfg := researchTestConfig()
	for _, sourceMode := range []string{"research", "chat"} {
		for _, operation := range []string{"retry", "continue"} {
			repo := legacyRunDefaultsRepository(sourceMode)
			repo.conversation.ModelProfile = map[string]any{"max_output_tokens": 4097}
			service := New(repo)
			service.SetResearchRunDefaults("standard", cfg.ResearchPolicies(), cfg.ResearchModelSnapshot())
			_, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", SourceRunID: "legacy", Operation: operation}, "")
			if !errors.Is(err, domain.ErrInvalidConversation) || len(repo.turns) != 0 {
				t.Fatalf("legacy %s %s accepted invalid current model: err=%v turns=%v", sourceMode, operation, err, repo.turns)
			}
		}
	}
}

func TestRepeatSnapshotReadFailuresDoNotSelectFreshDefaults(t *testing.T) {
	for _, sourceMode := range []string{"research", "chat"} {
		for _, operation := range []string{"retry", "continue"} {
			for _, failure := range []string{"source", "snapshot", "conversation"} {
				repo := legacyRunDefaultsRepository(sourceMode)
				cause := errors.New("storage lookup failed")
				switch failure {
				case "source":
					repo.runErr = cause
				case "snapshot":
					repo.sessionErr, repo.modelErr = cause, cause
				case "conversation":
					repo.loadErr = cause
				}
				_, err := New(repo).SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", SourceRunID: "legacy", Operation: operation}, "")
				if !errors.Is(err, cause) || len(repo.turns) != 0 || failure != "conversation" && repo.loads != 0 {
					t.Fatalf("%s %s lookup %s: err=%v turns=%v loads=%d", sourceMode, operation, failure, err, repo.turns, repo.loads)
				}
			}
		}
	}
}

func TestLegacyRepeatRejectsForeignConversationBeforeResolvingDefaults(t *testing.T) {
	repo := legacyRunDefaultsRepository("research")
	repo.run.ConversationID = "another-conversation"
	_, err := New(repo).SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", SourceRunID: "legacy", Operation: "retry"}, "")
	if !errors.Is(err, sqlite.ErrConflict) || len(repo.turns) != 0 || repo.loads != 0 || repo.sessionReads != 0 || repo.modelReads != 0 {
		t.Fatalf("foreign source selected conversation defaults: err=%v turns=%v loads=%d", err, repo.turns, repo.loads)
	}
}
