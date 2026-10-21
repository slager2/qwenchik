package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestChatRunAppliesPersistedModelSnapshotWithoutResearchSession(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	conversation, err := f.store.CreateConversation(ctx, domain.Conversation{Title: "Chat snapshot", Mode: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	model := researchRunDefaults().ModelSnapshot
	f.turn, err = f.store.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: conversation.ID, Content: "Ответь кратко", ModelSnapshot: &model}, "")
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	f.claim(t)
	client := &researchRunModel{respond: func(_ context.Context, _ llm.ChatRequest, _ int, streaming bool) (string, error) {
		if !streaming {
			t.Error("Chat must not call a planner or verifier")
		}
		return "Готово.", nil
	}}
	worker := New(f.store, client, tools.NewRegistry(), Options{MaxOutputTokens: 4096, Temperature: 1.7, TopP: 0.9})
	worker.runTask(ctx, f.task)
	run, err := f.store.GetRun(ctx, f.turn.Run.ID)
	if err != nil || run.Status != domain.StatusCompleted {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	calls := client.calls()
	if len(calls) != 1 || !calls[0].streaming {
		t.Fatalf("calls=%d", len(calls))
	}
	assertResearchRunRequest(t, calls[0].request, model, 0, model.MaxOutputTokens)
	if _, err := f.store.GetResearchSession(ctx, f.turn.Run.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("Chat acquired a research session: %v", err)
	}
	saved, err := f.store.GetRunModelSnapshot(ctx, f.turn.Run.ID)
	if err != nil || saved.Model != model.Model || saved.ContextLength != model.ContextLength || saved.Temperature != 0 {
		t.Fatalf("snapshot=%+v err=%v", saved, err)
	}
}

func TestResearchCitationLimitsStayConsistentWithRunPolicy(t *testing.T) {
	repo, state := citationFixture()
	second := repo.evidence[0]
	second.ID, second.CanonicalURL = foreignCitationID, "https://example.net/archive"
	repo.evidence = append(repo.evidence, second)
	state.ToolResults["fetch_2"] = map[string]any{"evidence_id": second.ID, "excerpt": fixtureCitationExcerpt}
	budget, _ := domain.ResearchBudgetForProfile(domain.ResearchProfileStandard)
	budget.MaxCitationSources, budget.MaxCitationClaims = 1, 1
	ctx := context.WithValue(context.Background(), researchExecutionKey{}, domain.ResearchSession{Budget: budget})
	worker := New(repo, nil, nil, Options{})
	catalogue, err := worker.citationCatalogue(ctx, "research", state.ToolResults, []string{fixtureCitationID, foreignCitationID})
	if err != nil || len(catalogue) != 1 {
		t.Fatalf("catalogue=%v err=%v", catalogue, err)
	}
	_, limit := citationLimits(ctx)
	answer := "First claim. [" + catalogue[0].EvidenceID + "]\nSecond claim. [" + catalogue[0].EvidenceID + "]"
	if _, err := citationUnits(answer, catalogue, limit); !errors.Is(err, errCitationMapping) {
		t.Fatalf("claim ceiling not enforced: %v", err)
	}
	if !strings.Contains(researchSynthesisPrompt(limit), "at most 1 short paragraphs") {
		t.Fatal("prompt ignored run claim limit")
	}
	if err := tools.ValidateJSONSchema(citationReviewSchema(limit), []byte(`{"claims":[{"claim":"one","evidence_id":"e","quote":"first","kind":"supported"},{"claim":"two","evidence_id":"e","quote":"second","kind":"supported"}]}`)); err == nil {
		t.Fatal("schema ignored run claim limit")
	}
}

func TestResearchRunRejectsIncompatibleRuntimeBeforeInference(t *testing.T) {
	for _, smallerContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "source_policy", true: "model_context"}[smallerContext], func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			session := f.submit(t, "Привет", nil)
			policy := session.Policy
			options := Options{RuntimeResearchPolicy: &policy, RuntimeModelContext: session.ModelSnapshot.ContextLength}
			if smallerContext {
				options.RuntimeModelContext /= 2
			} else {
				policy.PDFPages++
			}
			client := &researchRunModel{}
			worker := New(f.store, client, tools.NewRegistry(), options)
			worker.runTask(ctx, f.task)
			run, err := f.store.GetRun(ctx, f.turn.Run.ID)
			if err != nil || run.Status != domain.StatusFailed || run.ErrorCode != "research_runtime_changed" {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			stored := researchRunSession(t, f.store, run.ID)
			if len(client.calls()) != 0 || stored.Counters.LLMCalls != 0 || stored.Policy != session.Policy || stored.ModelSnapshot.ContextLength != session.ModelSnapshot.ContextLength {
				t.Fatal("runtime change invoked inference or rewrote immutable settings")
			}
		})
	}
}
