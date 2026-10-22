package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

type interruptedContinuationStore struct{ *sqlite.Store }

func (s interruptedContinuationStore) RequestResearchContinuation(ctx context.Context, id string, count int, fingerprint, failure string, missing []string) error {
	if err := s.Store.RequestResearchContinuation(ctx, id, count, fingerprint, failure, missing); err != nil {
		return err
	}
	return ErrInterrupted
}

func TestCompletionGovernorRunsBoundedTargetedContinuation(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			ctx := context.Background()
			f := newResearchRunStore(t)
			f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) { s.Policy.MaxContinuations = 1 })
			base := integrationResearchModel()
			reviews := 0
			model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, streaming bool) (string, error) {
				if index < 5 {
					return base.respond(request, index)
				}
				var input struct {
					Catalog   []verificationCriterion    `json:"criterion_catalog"`
					Step      string                     `json:"step"`
					FetchURLs []string                   `json:"allowed_fetch_urls"`
					Claims    []citationClaim            `json:"claims_to_check"`
					Results   map[string]json.RawMessage `json:"tool_results"`
				}
				if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
					return "", err
				}
				if len(input.Catalog) > 0 {
					reviews++
					assessments := map[string]any{}
					for _, criterion := range input.Catalog {
						ids := criterion.EvidenceIDs
						if ids == nil {
							ids = []string{}
						}
						assessments[criterion.ID] = map[string]any{"met": true, "evidence_ids": ids, "rationale": "The recorded operation is supported"}
					}
					ready := reviews > 1 && !blocked
					missing := []string{}
					if !ready {
						missing = []string{"Inspect another independent path before stopping"}
					}
					data, err := json.Marshal(map[string]any{"ready_to_answer": ready, "summary": "Recorded sources checked", "missing": missing, "criterion_assessments": assessments})
					return string(data), err
				}
				if strings.Contains(input.Step, "new independent targeted query") {
					return `{"status":"continue","summary":"Targeted new discovery","tool_call":{"name":"search","arguments":{"query":"protocol deployment edge cases original research","language":null,"recency":null,"max_results":3}}}`, nil
				}
				if strings.Contains(input.Step, "Inspect a new source") && len(input.FetchURLs) > 0 {
					return fmt.Sprintf(`{"status":"continue","summary":"Inspect targeted source","tool_call":{"name":"fetch_url","arguments":{"url":%q,"original_query":"protocol deployment edge cases original research"}}}`, input.FetchURLs[0]), nil
				}
				if len(input.Claims) > 0 {
					for i := range input.Claims {
						input.Claims[i].Kind = "supported"
						input.Claims[i].Quote = input.Claims[i].Claim
					}
					data, err := json.Marshal(citationReview{Claims: input.Claims})
					return string(data), err
				}
				if streaming {
					paragraphs := []string{}
					for _, raw := range input.Results {
						var result struct {
							ID      string `json:"evidence_id"`
							Excerpt string `json:"excerpt"`
						}
						if json.Unmarshal(raw, &result) != nil {
							continue
						}
						if result.ID != "" {
							paragraphs = append(paragraphs, result.Excerpt+" ["+result.ID+"]")
						}
					}
					data, err := json.Marshal(map[string]string{"answer": strings.Join(paragraphs, "\n\n")})
					return string(data), err
				}
				return "", fmt.Errorf("unexpected governor model call index=%d step=%q", index, input.Step)
			}}
			search, fetcher := &researchRunSearch{}, &researchRunFetcher{}
			registry := researchRunRegistry(t, f.store, search, fetcher)
			w := New(interruptedContinuationStore{f.store}, llm.NewGuardedClient(model), registry, Options{})
			w.runTask(ctx, f.task)
			if reviews != 1 || search.calls.Load() != 2 {
				t.Fatalf("interruption did not occur at durable continuation: reviews=%d searches=%d", reviews, search.calls.Load())
			}
			f.reopen(t)
			registry = researchRunRegistry(t, f.store, search, fetcher)
			w = New(f.store, llm.NewGuardedClient(model), registry, Options{})
			w.runTask(ctx, f.task)
			want, code := domain.StatusCompleted, ""
			if blocked {
				want, code = domain.StatusFailed, "completion_blocked"
			}
			assertResearchRunTerminal(t, f, want, code)
			if reviews != 2 || search.calls.Load() != 3 {
				t.Fatalf("reviews=%d searches=%d", reviews, search.calls.Load())
			}
			f.reopen(t)
			state, err := wWithStore(w, f).currentCheckpoint(ctx, f.task.ID)
			if err != nil || state.Completion.Continuations != 1 {
				t.Fatalf("durable governor: %+v %v", state.Completion, err)
			}
			events, err := f.store.EventsAfter(ctx, f.task.ID, 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event.Type == "continuation.requested" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("continuations=%d", count)
			}
			if blocked {
				task, _ := f.store.GetTask(ctx, f.task.ID)
				if task.TerminalOutcome != "blocked" {
					t.Fatalf("outcome=%s", task.TerminalOutcome)
				}
			}
		})
	}
}

func wWithStore(w *Worker, f *researchRunFixture) *Worker {
	return New(f.store, w.client, w.tools, w.options)
}

func TestCompletionFingerprintIgnoresNarrativeButTracksEvidenceAndQueries(t *testing.T) {
	plan := domain.Plan{Subtasks: []domain.PlanStep{{ID: "s", Kind: "search"}}}
	state := checkpointState{CompletedSteps: []string{"s"}, SearchQueryHistory: []string{"Original  QUERY"}, ToolResults: map[string]any{"s": map[string]any{"snippet": "first narrative"}}}
	first, err := completionFingerprint(plan, state)
	if err != nil {
		t.Fatal(err)
	}
	state.Summary = "different reasoning must not count as progress"
	state.SearchQueryHistory = []string{"original query"}
	state.ToolResults["s"] = map[string]any{"snippet": "another model narrative"}
	second, _ := completionFingerprint(plan, state)
	if first != second {
		t.Fatal("narrative changed progress identity")
	}
	state.ToolResults["s"] = map[string]any{"evidence_id": "owned-evidence"}
	third, _ := completionFingerprint(plan, state)
	if third == second {
		t.Fatal("new evidence not counted")
	}
}
