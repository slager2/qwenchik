package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestBundledResearchRepairsCitationFormatOnceBeforeCritic(t *testing.T) {
	for _, failsAgain := range []bool{false, true} {
		t.Run(fmt.Sprint(failsAgain), func(t *testing.T) {
			f := newResearchRunStore(t)
			bundle := ResearchAgentBundle("saved-local-model", tools.NewRegistry())
			f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) { s.ModelSnapshot.Bundle = &bundle })
			base := integrationResearchModel()
			streams, critics := 0, 0
			model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, streaming bool) (string, error) {
				if index < 6 {
					return base.respond(request, index)
				}
				if streaming {
					streams++
					if streams == 1 {
						return `{"answer":"An uncited finalizer claim."}`, nil
					}
					if !strings.Contains(request.Messages[1].Content, "failed deterministic citation formatting") {
						t.Fatal("missing bounded repair instruction")
					}
					if failsAgain {
						return `{"answer":"Still uncited; never promote this claim."}`, nil
					}
					var input struct {
						Results map[string]struct {
							ID      string `json:"evidence_id"`
							Excerpt string `json:"excerpt"`
						} `json:"tool_results"`
					}
					if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
						return "", err
					}
					paragraphs := []string{}
					for _, result := range input.Results {
						paragraphs = append(paragraphs, result.Excerpt+" ["+result.ID+"]")
					}
					data, err := json.Marshal(map[string]string{"answer": strings.Join(paragraphs, "\n\n")})
					return string(data), err
				}
				critics++
				var input struct {
					Claims []citationClaim `json:"claims_to_check"`
				}
				if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
					return "", err
				}
				for index := range input.Claims {
					input.Claims[index].Kind = "supported"
					input.Claims[index].Quote = input.Claims[index].Claim
				}
				data, err := json.Marshal(citationReview{Claims: input.Claims})
				return string(data), err
			}}
			w := New(f.store, llm.NewGuardedClient(model), researchRunRegistry(t, f.store, &researchRunSearch{}, &researchRunFetcher{}), Options{})
			w.runTask(context.Background(), f.task)
			assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
			wanted, wantedCritics := "completed", 1
			if failsAgain {
				wanted, wantedCritics = "partial", 0
			}
			task, err := f.store.GetTask(context.Background(), f.task.ID)
			if err != nil || task.TerminalOutcome != wanted || streams != 2 || critics != wantedCritics {
				t.Fatalf("repair outcome: %s streams=%d critics=%d %v", task.TerminalOutcome, streams, critics, err)
			}
		})
	}
}
