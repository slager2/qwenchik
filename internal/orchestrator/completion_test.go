package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/app"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type interruptedContinuationStore struct{ *sqlite.Store }

func (s interruptedContinuationStore) RequestResearchContinuation(ctx context.Context, id string, count int, fingerprint, failure string, missing []string) error {
	if err := s.Store.RequestResearchContinuation(ctx, id, count, fingerprint, failure, missing); err != nil {
		return err
	}
	return ErrInterrupted
}

func TestCompletionGovernorRunsBoundedTargetedContinuation(t *testing.T) {
	cases := []struct {
		name                 string
		blocked              bool
		partialModelFails    bool
		partialModelTimesOut bool
	}{
		{name: "verified", blocked: false},
		{name: "blocked_with_evidence", blocked: true},
		{name: "blocked_model_failure_uses_excerpt_fallback", blocked: true, partialModelFails: true},
		{name: "blocked_partial_model_timeout_pauses_for_resources", blocked: true, partialModelTimesOut: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			blocked := testCase.blocked
			ctx := context.Background()
			f := newResearchRunStore(t)
			f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) { s.Policy.MaxContinuations = 1 })
			base := integrationResearchModel()
			reviews := 0
			model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, streaming bool) (string, error) {
				if (testCase.partialModelFails || testCase.partialModelTimesOut) && strings.Contains(request.Messages[0].Content, "deliberately partial research answer") {
					if testCase.partialModelTimesOut {
						return "", llm.ErrModelTimeout
					}
					return "", errors.New("injected partial finalizer failure")
				}
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
			options := Options{}
			if testCase.partialModelTimesOut {
				options.ResourcePolicy = resources.DefaultPolicy()
			}
			w := New(interruptedContinuationStore{f.store}, llm.NewGuardedClient(model), registry, options)
			w.runTask(ctx, f.task)
			if reviews != 1 || search.calls.Load() != 2 {
				t.Fatalf("interruption did not occur at durable continuation: reviews=%d searches=%d", reviews, search.calls.Load())
			}
			f.reopen(t)
			registry = researchRunRegistry(t, f.store, search, fetcher)
			w = New(f.store, llm.NewGuardedClient(model), registry, options)
			w.runTask(ctx, f.task)
			if testCase.partialModelTimesOut {
				if reviews != 2 || search.calls.Load() != 3 {
					t.Fatalf("partial finalizer timeout path: reviews=%d searches=%d", reviews, search.calls.Load())
				}
				runtimeGuardAssertPaused(t, f, "model_timeout")
				return
			}
			want, code := domain.StatusCompleted, ""
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
				task, err := f.store.GetTask(ctx, f.task.ID)
				if err != nil || task.TerminalOutcome != "partial" || task.Status != domain.StatusCompleted {
					t.Fatalf("partial terminal state=%#v err=%v", task, err)
				}
				evidence, err := f.store.Evidence(ctx, f.task.ID)
				if err != nil || len(evidence) == 0 {
					t.Fatalf("partial run lost persisted evidence: count=%d err=%v", len(evidence), err)
				}
				messages, err := f.store.ConversationMessages(ctx, f.turn.ConversationID)
				if err != nil {
					t.Fatal(err)
				}
				assistantCount := 0
				var partialMessage domain.ConversationMessage
				for _, message := range messages {
					if message.RunID == f.turn.Run.ID && message.Role == "assistant" {
						assistantCount++
						partialMessage = message
					}
				}
				if assistantCount != 1 || strings.TrimSpace(partialMessage.Content) == "" || partialMessage.Status != "completed" || !strings.Contains(partialMessage.Content, "Что не удалось подтвердить") {
					t.Fatalf("blocked run did not commit one nonempty partial assistant message: count=%d message=%+v", assistantCount, partialMessage)
				}
				allowed := map[string]bool{}
				for _, item := range evidence {
					allowed[item.ID] = true
				}
				for _, match := range citationPattern.FindAllStringSubmatch(partialMessage.Content, -1) {
					if len(match) != 2 || !allowed[match[1]] {
						t.Fatalf("partial answer contains an unowned citation: %q", match)
					}
				}
				if testCase.partialModelFails && !strings.Contains(partialMessage.Content, "Дословные выдержки") {
					t.Fatalf("failed partial synthesis did not use deterministic excerpt fallback: %s", partialMessage.Content)
				}
				if !testCase.partialModelFails && strings.Contains(partialMessage.Content, "Дословные выдержки") {
					t.Fatalf("valid partial synthesis unexpectedly used fallback: %s", partialMessage.Content)
				}
				if _, err := f.store.CompleteTask(ctx, f.task.ID, "duplicate", nil); err != nil {
					t.Fatalf("idempotent completion retry: %v", err)
				}
				messages, err = f.store.ConversationMessages(ctx, f.turn.ConversationID)
				if err != nil {
					t.Fatal(err)
				}
				assistantCount = 0
				for _, message := range messages {
					if message.RunID == f.turn.Run.ID && message.Role == "assistant" {
						assistantCount++
					}
				}
				if assistantCount != 1 {
					t.Fatalf("idempotent completion duplicated assistant message: %d", assistantCount)
				}
				continued, err := app.New(f.store).RepeatRun(ctx, f.turn.Run.ID, "continue", "blocked-continue-"+testCase.name)
				if err != nil || continued.Run.ID == f.turn.Run.ID {
					t.Fatalf("nonempty partial result could not be continued: run=%+v err=%v", continued.Run, err)
				}
				messages, err = f.store.ConversationMessages(ctx, f.turn.ConversationID)
				if err != nil {
					t.Fatal(err)
				}
				assistantCount = 0
				for _, message := range messages {
					if message.Role == "assistant" && (message.RunID == f.turn.Run.ID || message.RunID == continued.Run.ID) {
						assistantCount++
					}
				}
				if assistantCount != 2 || continued.AssistantMessage.Content != "" {
					t.Fatalf("continue duplicated/replaced prior assistant message: count=%d continued=%+v", assistantCount, continued.AssistantMessage)
				}
			}
		})
	}
}

func TestCompletionBlockedWithoutEvidenceCommitsDiagnosticPartialAnswer(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	f.submit(t, "Research a question without any saved sources.", nil)
	if _, err := f.store.TransitionTask(ctx, f.task.ID, domain.StatusRunning, nil, "task.status_changed", "user", nil); err != nil {
		t.Fatal(err)
	}
	verificationStage := domain.StageVerification
	if _, err := f.store.TransitionTask(ctx, f.task.ID, domain.StatusVerifying, &verificationStage, "task.status_changed", "user", nil); err != nil {
		t.Fatal(err)
	}
	task, err := f.store.GetTask(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) {
		modelCalls++
		return "unsupported answer", nil
	}}
	worker := New(f.store, llm.NewGuardedClient(model), tools.NewRegistry(), Options{})
	cause := errors.Join(errCompletionBlocked, verificationIncompleteError{Summary: "The verifier could not establish the required evidence.", Missing: []string{"Compare the two primary criteria"}})
	completed, err := worker.completeBlockedPartial(ctx, task, cause)
	if err != nil || !completed {
		t.Fatalf("diagnostic partial completion: completed=%t err=%v", completed, err)
	}
	if modelCalls != 0 {
		t.Fatalf("model called without saved evidence: %d", modelCalls)
	}
	run, err := f.store.GetRun(ctx, f.turn.Run.ID)
	if err != nil || run.Status != domain.StatusCompleted {
		t.Fatalf("no-evidence run outcome=%#v err=%v", run, err)
	}
	task, err = f.store.GetTask(ctx, run.ID)
	if err != nil || task.Status != domain.StatusCompleted || task.TerminalOutcome != "partial" {
		t.Fatalf("no-evidence task outcome=%#v err=%v", task, err)
	}
	messages, err := f.store.ConversationMessages(ctx, f.turn.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	var assistant *domain.ConversationMessage
	for index := range messages {
		if messages[index].RunID == run.ID && messages[index].Role == "assistant" {
			assistant = &messages[index]
			break
		}
	}
	if assistant == nil || assistant.Status != "completed" || strings.TrimSpace(assistant.Content) == "" || !strings.Contains(assistant.Content, "Нет доступных проверенных выдержек") || !strings.Contains(assistant.Content, "Compare the two primary criteria") || !strings.Contains(assistant.Content, "Что не удалось подтвердить") {
		t.Fatalf("no-evidence diagnostic assistant message: %#v", assistant)
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
