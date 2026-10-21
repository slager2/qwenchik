package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

const fixtureCitationID = "11111111-1111-4111-8111-111111111111"
const foreignCitationID = "22222222-2222-4222-8222-222222222222"
const fixtureCitationExcerpt = "The library opened in 1999. The archive contains 40 volumes."

type citationDelta struct {
	stream, text string
	index        int
}

type citationRepository struct {
	Repository
	evidence []domain.Evidence
	run      domain.Run
	runErr   error
	eventErr error
	deltaErr error
	events   []map[string]any
	deltas   []citationDelta
	paused   bool
}

func (r *citationRepository) GetRun(context.Context, string) (domain.Run, error) {
	return r.run, r.runErr
}
func (r *citationRepository) GetTask(context.Context, string) (domain.Task, error) {
	status := domain.StatusVerifying
	if r.paused {
		status = domain.StatusPaused
	}
	return domain.Task{ID: "research", Status: status}, nil
}
func (r *citationRepository) Evidence(context.Context, string) ([]domain.Evidence, error) {
	return append([]domain.Evidence(nil), r.evidence...), nil
}
func (r *citationRepository) RecordEvent(_ context.Context, _, kind string, _ *domain.Stage, visibility string, payload map[string]any) (domain.Event, error) {
	if kind != "verification.updated" || visibility != "user" {
		return domain.Event{}, errors.New("unexpected verification event")
	}
	r.events = append(r.events, payload)
	return domain.Event{}, r.eventErr
}
func (r *citationRepository) RecordMessageDelta(_ context.Context, _, stream, _ string, index int, text string) (domain.Event, error) {
	if r.deltaErr != nil {
		return domain.Event{}, r.deltaErr
	}
	r.deltas = append(r.deltas, citationDelta{stream, text, index})
	return domain.Event{}, nil
}

func citationFixture() (*citationRepository, checkpointState) {
	repo := &citationRepository{
		run:      domain.Run{ID: "research", ConversationID: "conversation", Mode: "research"},
		evidence: []domain.Evidence{{ID: fixtureCitationID, TaskID: "research", CanonicalURL: "https://example.org/library", Title: "Library archive", HTTPStatus: 200, Excerpt: fixtureCitationExcerpt}},
	}
	state := checkpointState{ToolResults: map[string]any{"fetch_1": map[string]any{"evidence_id": fixtureCitationID, "url": "https://example.org/library", "excerpt": fixtureCitationExcerpt}}}
	return repo, state
}

func reviewJSON(claim, id, quote, kind string) string {
	encoded, _ := json.Marshal(citationReview{Claims: []citationClaim{{Claim: claim, EvidenceID: id, Quote: quote, Kind: kind}}})
	return string(encoded)
}

func TestConversationResearchCitationsVerifyClaimsOrReplaceWithExcerpts(t *testing.T) {
	claim := "The archive contains 40 volumes."
	for _, fixture := range []struct {
		name, answer, review string
		fallback             bool
		calls                int
	}{
		{"supported", claim + " [" + fixtureCitationID + "]", reviewJSON(claim, fixtureCitationID, claim, "supported"), false, 2},
		{"unknown current fact", "Open today until 22:00. [" + fixtureCitationID + "]", reviewJSON("Open today until 22:00.", fixtureCitationID, "", "unknown"), true, 2},
		{"inference", "The archive is unusually large. [" + fixtureCitationID + "]", reviewJSON("The archive is unusually large.", fixtureCitationID, claim, "inference"), true, 2},
		{"invented quote", claim + " [" + fixtureCitationID + "]", reviewJSON(claim, fixtureCitationID, "The archive contains 400 volumes.", "supported"), true, 3},
		{"foreign citation", claim + " [" + foreignCitationID + "]", "", true, 1},
		{"critic foreign evidence", claim + " [" + fixtureCitationID + "]", reviewJSON(claim, foreignCitationID, claim, "supported"), true, 3},
		{"missing citation", claim, "", true, 1},
		{"uncited current fact", claim + " [" + fixtureCitationID + "]\nOpen today until 22:00.", "", true, 1},
		{"omitted paragraph", claim + " [" + fixtureCitationID + "]\nThe library opened in 1999. [" + fixtureCitationID + "]", reviewJSON(claim, fixtureCitationID, claim, "supported"), true, 3},
		{"invalid critic schema", claim + " [" + fixtureCitationID + "]", `{"claims":[],"reasoning":"not permitted"}`, true, 3},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			repo, state := citationFixture()
			model := &scriptedLLM{respond: func(request llm.ChatRequest, index int) (string, error) {
				if index == 0 {
					if !strings.Contains(request.Messages[0].Content, "inline [evidenceUUID]") {
						t.Error("research prompt did not request inline citations")
					}
					return fixture.answer, nil
				}
				return fixture.review, nil
			}}
			worker := New(repo, model, nil, Options{})
			answer, err := worker.synthesizeValidated(context.Background(), domain.Task{ID: "research"}, "Library research", "", state, "", []string{fixtureCitationID}, "message")
			if err != nil {
				t.Fatal(err)
			}
			if model.requests != fixture.calls {
				t.Fatalf("model calls=%d want=%d", model.requests, fixture.calls)
			}
			if !strings.Contains(answer, "["+fixtureCitationID+"]") || !strings.Contains(answer, "<https://example.org/library>") || strings.Contains(answer, foreignCitationID) {
				t.Fatalf("unsafe/missing source references: %s", answer)
			}
			fallback := strings.Contains(answer, "Дословные выдержки")
			if fallback != fixture.fallback {
				t.Fatalf("fallback=%t answer=%s", fallback, answer)
			}
			if fixture.fallback && strings.Contains(answer, "22:00") {
				t.Fatalf("unsupported current fact survived: %s", answer)
			}
			var assembled, lastStream string
			streamCount := 0
			for _, delta := range repo.deltas {
				if delta.stream != lastStream {
					if delta.index != 0 {
						t.Fatal("replacement stream did not begin at zero")
					}
					assembled, lastStream = "", delta.stream
					streamCount++
				}
				assembled += delta.text
			}
			if assembled != answer || streamCount != 1+boolCount(fixture.fallback) {
				t.Fatalf("stream reset mismatch: streams=%d assembled=%q answer=%q", streamCount, assembled, answer)
			}
			if len(repo.events) != 2 || repo.events[0]["status"] != "started" {
				t.Fatalf("verification events=%#v", repo.events)
			}
			if (repo.events[1]["status"] == "fallback_excerpts") != fixture.fallback {
				t.Fatalf("verification verdict=%#v", repo.events[1])
			}
		})
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestCitationCriticRepairsOnlyOnceWithoutRegeneratingAnswer(t *testing.T) {
	repo, state := citationFixture()
	claim := "The archive contains 40 volumes."
	model := &scriptedLLM{responses: []string{claim + " [" + fixtureCitationID + "]", "invalid", reviewJSON(claim, fixtureCitationID, claim, "supported")}}
	worker := New(repo, model, nil, Options{})
	answer, err := worker.synthesizeValidated(context.Background(), domain.Task{ID: "research"}, "Library", "", state, "", []string{fixtureCitationID}, "message")
	if err != nil || model.requests != 3 || strings.Contains(answer, "Дословные выдержки") {
		t.Fatalf("answer=%s err=%v calls=%d", answer, err, model.requests)
	}
	for _, delta := range repo.deltas {
		if delta.stream != repo.deltas[0].stream {
			t.Fatal("critic repair unnecessarily reset answer stream")
		}
	}
}

func TestCitationContextBudgetUsesDeterministicExcerptsWithoutModel(t *testing.T) {
	repo, state := citationFixture()
	model := &scriptedLLM{}
	worker := New(repo, model, nil, Options{MaxInputBytes: 256})
	answer, err := worker.synthesizeValidated(context.Background(), domain.Task{ID: "research"}, strings.Repeat("long goal ", 200), "", state, "", []string{fixtureCitationID}, "message")
	if err != nil || model.requests != 0 || !strings.Contains(answer, fixtureCitationExcerpt) || !strings.Contains(answer, "Ограничения") {
		t.Fatalf("answer=%s err=%v calls=%d", answer, err, model.requests)
	}
}

func TestCitationCriticHonorsInputBudgetBeforeCompletion(t *testing.T) {
	repo, state := citationFixture()
	model := &scriptedLLM{}
	worker := New(repo, model, nil, Options{MaxInputBytes: 256})
	catalogue, err := worker.citationCatalogue(context.Background(), "research", state.ToolResults, []string{fixtureCitationID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.reviewCitationClaims(context.Background(), "research", "The archive contains 40 volumes. ["+fixtureCitationID+"]", catalogue)
	if !errors.Is(err, errInputBudgetExceeded) || model.requests != 0 {
		t.Fatalf("critic budget err=%v calls=%d", err, model.requests)
	}
}

func TestCitationCatalogueRejectsForeignAndForgedEvidence(t *testing.T) {
	for _, mutate := range []func(*citationRepository, checkpointState){
		func(repo *citationRepository, _ checkpointState) { repo.evidence[0].TaskID = "another-task" },
		func(repo *citationRepository, _ checkpointState) {
			repo.evidence[0].QualityFlags = []string{"challenge_detected"}
		},
		func(repo *citationRepository, _ checkpointState) {
			repo.evidence[0].CanonicalURL += "?access_token=private"
		},
		func(_ *citationRepository, state checkpointState) {
			state.ToolResults["fetch_1"].(map[string]any)["excerpt"] = "Invented tool excerpt absent from durable evidence."
		},
	} {
		repo, state := citationFixture()
		mutate(repo, state)
		worker := New(repo, &scriptedLLM{}, nil, Options{})
		catalogue, err := worker.citationCatalogue(context.Background(), "research", state.ToolResults, []string{fixtureCitationID})
		if err != nil || len(catalogue) != 0 {
			t.Fatalf("unowned or unverified source admitted: %#v err=%v", catalogue, err)
		}
	}
}

func TestCitationCancellationAndPersistenceErrorsPropagate(t *testing.T) {
	repo, state := citationFixture()
	worker := New(repo, &scriptedLLM{}, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := worker.synthesizeValidated(ctx, domain.Task{ID: "research"}, "Library", "", state, "", []string{fixtureCitationID}, "message"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	for _, eventFailure := range []bool{false, true} {
		repo, state = citationFixture()
		persistErr := errors.New("storage failed")
		if eventFailure {
			repo.eventErr = persistErr
		} else {
			repo.deltaErr = persistErr
		}
		worker = New(repo, &scriptedLLM{responses: []string{"Uncited draft"}}, nil, Options{})
		if _, err := worker.synthesizeValidated(context.Background(), domain.Task{ID: "research"}, "Library", "", state, "", []string{fixtureCitationID}, "message"); !errors.Is(err, persistErr) {
			t.Fatalf("persistence failure=%v", err)
		}
	}
}

func TestCitationRunGatingPreservesLegacyAndChat(t *testing.T) {
	repo, _ := citationFixture()
	worker := New(repo, nil, nil, Options{})
	for _, run := range []domain.Run{{ID: "research", Mode: "research"}, {ID: "research", ConversationID: "conversation", Mode: "chat"}} {
		repo.run = run
		if linked, err := worker.isConversationResearch(context.Background(), "research"); err != nil || linked {
			t.Fatalf("unexpected enhanced path: %t %v", linked, err)
		}
	}
	repo.runErr = sqlite.ErrNotFound
	if linked, err := worker.isConversationResearch(context.Background(), "research"); err != nil || linked {
		t.Fatalf("legacy gating: %t %v", linked, err)
	}
}
