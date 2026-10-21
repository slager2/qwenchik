package app

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

type completedChatRepository struct {
	*researchCaptureRepository
	run domain.Run
}

func (r *completedChatRepository) GetRun(context.Context, string) (domain.Run, error) {
	return r.run, nil
}

func TestChatSubmitFreezesModelSnapshotsAcrossCompletedTurns(t *testing.T) {
	ctx := context.Background()
	cfg := researchTestConfig()
	repo := &completedChatRepository{
		researchCaptureRepository: &researchCaptureRepository{conversation: domain.Conversation{
			ID: "c", Title: "Chat", Mode: "chat",
			ModelProfile:     map[string]any{"temperature": 0, "top_p": 0.25, "max_output_tokens": float64(2048)},
			ResearchSettings: map[string]any{"web_enabled": false},
		}},
		run: domain.Run{ID: "completed-chat", ConversationID: "c", Mode: "chat", Status: domain.StatusCompleted},
	}
	service := New(repo)
	model := cfg.ResearchModelSnapshot()
	model.StopSequences = []string{"unexposed-stop"}
	service.SetResearchRunDefaults("standard", nil, model)
	model.StopSequences[0] = "mutated"

	if _, err := service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: "c", Content: "First", Mode: "chat"}, "first"); err != nil {
		t.Fatal(err)
	}
	first := repo.turns[0]
	wantFirst := cfg.ResearchModelSnapshot()
	wantFirst.Temperature, wantFirst.TopP, wantFirst.MaxOutputTokens = 0, 0.25, 2048
	if first.ModelSnapshot == nil || !reflect.DeepEqual(*first.ModelSnapshot, wantFirst) {
		t.Fatalf("first chat snapshot=%#v want=%#v", first.ModelSnapshot, wantFirst)
	}
	if first.ResearchPolicy != nil || first.ResearchBudget != nil || first.ResearchProfile != "" {
		t.Fatalf("chat acquired a research budget: %#v", first)
	}

	cfg.LLMModel = "replacement-local-model"
	cfg.LLMBaseURL = "http://127.0.0.1:8001/v1"
	cfg.LLMContextLength, cfg.LLMMaxOutputTokens = 16384, 3072
	cfg.LLMTemperature, cfg.LLMTopP = 0.7, 0.9
	service.SetResearchRunDefaults("standard", nil, cfg.ResearchModelSnapshot())
	repo.conversation.ModelProfile = map[string]any{"temperature": 1.2, "top_p": 0.6, "max_output_tokens": 1024}

	for _, operation := range []string{"retry", "continue"} {
		if _, err := service.RepeatRun(ctx, "completed-chat", operation, operation); err != nil {
			t.Fatal(err)
		}
		repeated := repo.turns[len(repo.turns)-1]
		if repeated.SourceRunID != "completed-chat" || repeated.ModelSnapshot != nil || repeated.ResearchPolicy != nil || repeated.ResearchBudget != nil {
			t.Fatalf("%s substituted current model settings for the prior run: %#v", operation, repeated)
		}
	}
	if !reflect.DeepEqual(*first.ModelSnapshot, wantFirst) {
		t.Fatal("changed defaults or conversation settings mutated the completed chat snapshot")
	}

	if _, err := service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: "c", Content: "Next"}, "next"); err != nil {
		t.Fatal(err)
	}
	next := repo.turns[len(repo.turns)-1]
	wantNext := cfg.ResearchModelSnapshot()
	wantNext.Temperature, wantNext.TopP, wantNext.MaxOutputTokens = 1.2, 0.6, 1024
	if next.ModelSnapshot == nil || !reflect.DeepEqual(*next.ModelSnapshot, wantNext) {
		t.Fatalf("next chat snapshot=%#v want=%#v", next.ModelSnapshot, wantNext)
	}
	next.ModelSnapshot.Model = "mutated"
	next.ModelSnapshot.StopSequences = append(next.ModelSnapshot.StopSequences, "mutated")
	if _, err := service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: "c", Content: "Another"}, "another"); err != nil {
		t.Fatal(err)
	}
	if got := repo.turns[len(repo.turns)-1].ModelSnapshot; got == nil || !reflect.DeepEqual(*got, wantNext) || !reflect.DeepEqual(*first.ModelSnapshot, wantFirst) {
		t.Fatal("mutating a submitted chat snapshot changed another run or the service defaults")
	}
}

func TestChatSubmitUsesRuntimeDefaultsAndCompatibleFallback(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, mode := range []string{"", "chat"} {
			repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Chat", Mode: "chat"}}
			service := New(repo)
			want := domain.ModelProfileSnapshot{
				Version: 1, Model: "local-qwen", ContextLength: 131072, MaxOutputTokens: 4096,
				Temperature: 0.2, TopP: 0.8, StopSequences: []string{}, PromptVersion: "local-research-v1", SchemaVersion: 1,
			}
			if configured {
				want = researchTestConfig().ResearchModelSnapshot()
				service.SetResearchRunDefaults("standard", nil, want)
			}
			if _, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Content: "Chat", Mode: mode}, ""); err != nil {
				t.Fatal(err)
			}
			turn := repo.turns[0]
			if turn.ModelSnapshot == nil || !reflect.DeepEqual(*turn.ModelSnapshot, want) || turn.ResearchBudget != nil || turn.ResearchPolicy != nil || turn.ResearchProfile != "" {
				t.Fatalf("configured=%v mode=%q snapshot=%#v want=%#v", configured, mode, turn.ModelSnapshot, want)
			}
		}
	}
}

func TestChatSubmitRejectsInvalidModelProfileBeforeSubmission(t *testing.T) {
	for _, profile := range []map[string]any{
		{"temperature": math.NaN()}, {"temperature": 2.1}, {"temperature": "0"},
		{"top_p": 0}, {"top_p": math.Inf(1)},
		{"max_output_tokens": 4097}, {"max_output_tokens": 1.5}, {"max_output_tokens": 0},
		{"model": "arbitrary-model"}, {"base_url": "https://remote.invalid/v1"},
		{"api_key": "unsupported"}, {"stop_sequences": []string{"stop"}},
	} {
		for _, mode := range []string{"", "chat"} {
			repo := &researchCaptureRepository{conversation: domain.Conversation{ID: "c", Title: "Chat", Mode: "chat", ModelProfile: profile}}
			service := New(repo)
			service.SetResearchRunDefaults("standard", nil, researchTestConfig().ResearchModelSnapshot())
			_, err := service.SubmitTurn(context.Background(), domain.SubmitTurn{ConversationID: "c", Content: "Question", Mode: mode}, "")
			if !errors.Is(err, domain.ErrInvalidConversation) || len(repo.turns) != 0 {
				t.Fatalf("accepted chat mode=%q model profile=%v err=%v", mode, profile, err)
			}
		}
	}
}
