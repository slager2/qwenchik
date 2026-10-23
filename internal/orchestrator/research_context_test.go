package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/app"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type researchBridgeCapture struct{ request llm.ChatRequest }

func (c *researchBridgeCapture) Complete(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, errors.New("unexpected completion")
}
func (c *researchBridgeCapture) Stream(_ context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	c.request = request
	events := make(chan llm.ChatEvent, 1)
	events <- llm.ChatEvent{Text: "The answer remains source-backed."}
	close(events)
	return events, nil
}

func TestExplicitResearchBridgeIsCompactOwnedAndInsertedBeforeCurrentChatRequest(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "research-bridge.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := app.New(store)
	policy := domain.DefaultResearchPolicy()
	model := domain.ModelProfileSnapshot{Version: 1, Model: "local-qwen", BaseURL: "http://127.0.0.1:8000/v1", ContextLength: 8192, MaxOutputTokens: 512, Temperature: 0, TopP: 0.8, StopSequences: []string{}, PromptVersion: "local-research-v1", SchemaVersion: 1}
	service.SetResearchRunDefaults(domain.ResearchProfileStandard, map[string]domain.ResearchPolicy{domain.ResearchProfileStandard: policy}, model)
	researchConversation, err := service.CreateConversation(ctx, domain.Conversation{Title: "Research then chat", Mode: "research"})
	if err != nil {
		t.Fatal(err)
	}
	researchTurn, err := service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: researchConversation.ID, Content: "Research SQLite WAL concurrency", Mode: "research"}, "research-turn")
	if err != nil {
		t.Fatal(err)
	}
	researchTask, ok, err := store.ClaimNext(ctx)
	if err != nil || !ok || researchTask.ID != researchTurn.Run.ID {
		t.Fatalf("claim source research run: %v", err)
	}
	stage := domain.StageSearch
	if _, err := store.TransitionTask(ctx, researchTask.ID, domain.StatusRunning, &stage, "task.status_changed", "user", nil); err != nil {
		t.Fatal(err)
	}
	evidenceID := "11111111-1111-1111-1111-111111111111"
	excerpt := "PRIVATE_RAW_EXCERPT WAL permits concurrent readers and a writer."
	evidence, err := store.SaveEvidence(ctx, domain.Evidence{ID: evidenceID, TaskID: researchTask.ID, Provider: "http", OriginalURL: "https://sqlite.org/wal.html", CanonicalURL: "https://sqlite.org/wal.html", ContentSHA256: strings.Repeat("a", 64), ExtractionMethod: "html", HTTPStatus: 200, Excerpt: excerpt, QualityFlags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordEvent(ctx, researchTask.ID, "verification.updated", nil, "user", map[string]any{"phase": "citation_verification", "status": "supported", "method": "local_model_entailment_and_exact_excerpt_matching", "claim_count": 1, "citations": []map[string]string{{"evidence_id": evidence.ID, "url": evidence.CanonicalURL, "title": "Untrusted supplied title"}}}); err != nil {
		t.Fatal(err)
	}
	stage = domain.StageVerification
	if _, err := store.TransitionTask(ctx, researchTask.ID, domain.StatusVerifying, &stage, "task.status_changed", "user", nil); err != nil {
		t.Fatal(err)
	}
	answer := "WAL permits concurrent readers and a writer. [" + evidence.ID + "]\n\nИсточники:\n\n- [" + evidence.ID + "] SQLite WAL — <https://sqlite.org/wal.html>"
	if _, err := store.CompleteTask(ctx, researchTask.ID, answer, map[string]any{"completed_steps": []string{}}); err != nil {
		t.Fatal(err)
	}
	otherConversation, err := service.CreateConversation(ctx, domain.Conversation{Title: "Other", Mode: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: otherConversation.ID, Content: "Try foreign context", Mode: "chat", ResearchRunID: researchTask.ID}, "foreign-context"); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("cross-conversation bridge accepted: %v", err)
	}
	if _, err := service.UpdateConversation(ctx, researchConversation.ID, map[string]any{"mode": "chat"}); err != nil {
		t.Fatal(err)
	}
	chatTurn, err := service.SubmitTurn(ctx, domain.SubmitTurn{ConversationID: researchConversation.ID, Content: "Explain the WAL differences, concisely.", Mode: "chat", ResearchRunID: researchTask.ID}, "chat-with-research-context")
	if err != nil {
		t.Fatal(err)
	}
	userMetadataRaw, err := json.Marshal(chatTurn.UserMessage.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	userMetadata := string(userMetadataRaw)
	if strings.Contains(userMetadata, excerpt) || !strings.Contains(userMetadata, evidence.ID) || !strings.Contains(userMetadata, researchTask.ID) {
		t.Fatalf("bridge metadata is missing references or contains raw excerpt: %s", userMetadata)
	}
	loaded, err := store.ConversationMessages(ctx, researchConversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	chatTask, ok, err := store.ClaimNext(ctx)
	if err != nil || !ok || chatTask.ID != chatTurn.Run.ID {
		t.Fatalf("claim chat run: %v", err)
	}
	modelClient := &researchBridgeCapture{}
	worker := New(store, modelClient, tools.NewRegistry(), Options{MaxInputBytes: 64 << 10, MaxOutputTokens: 512, TaskTimeout: time.Minute})
	if err := worker.executeChat(ctx, chatTask, chatTurn.Run, store); err != nil {
		t.Fatal(err)
	}
	if len(modelClient.request.Messages) < 4 {
		t.Fatalf("chat request omitted bridge: %#v", modelClient.request.Messages)
	}
	bridgeIndex := -1
	for index, message := range modelClient.request.Messages {
		if message.Role == "user" && strings.Contains(message.Content, "Explicit research context selected by the user") {
			bridgeIndex = index
			if strings.Contains(message.Content, excerpt) {
				t.Fatal("raw evidence excerpt leaked into Chat prompt")
			}
		}
	}
	if bridgeIndex < 1 || bridgeIndex != len(modelClient.request.Messages)-2 || modelClient.request.Messages[len(modelClient.request.Messages)-1].Content != "Explain the WAL differences, concisely." {
		t.Fatalf("bridge/current request ordering: %#v", modelClient.request.Messages)
	}
	if !strings.Contains(modelClient.request.Messages[bridgeIndex].Content, evidence.ID) || !strings.Contains(modelClient.request.Messages[bridgeIndex].Content, "untrusted reference data") {
		t.Fatal("bridge not marked and linked as untrusted data")
	}
	for _, message := range loaded {
		if message.ID == chatTurn.UserMessage.ID {
			var metadata struct {
				Context domain.ResearchContext `json:"research_context"`
			}
			if json.Unmarshal([]byte(userMetadata), &metadata) != nil || metadata.Context.Sources[0].EvidenceID != evidence.ID || metadata.Context.Claims[0].EvidenceID != evidence.ID {
				t.Fatal("persisted bridge did not retain the verified claim/source link")
			}
			return
		}
	}
	t.Fatal("bridge user message missing")
}

func TestResearchContextBoundsAndSensitiveURLs(t *testing.T) {
	base := domain.ResearchContext{Version: 1, RunID: "run", VerificationStatus: "fallback_excerpts", Claims: []domain.ResearchContextClaim{}, Sources: []domain.ResearchContextSource{{EvidenceID: "e", Title: "title", CanonicalURL: "https://example.org/source?token=hidden"}}, Warnings: []string{}, Limitations: []string{}}
	if err := base.Validate(); err == nil {
		t.Fatal("credential-bearing source URL accepted")
	}
	base.Sources[0].CanonicalURL = "https://example.org/access_token/long-private-value"
	if err := base.Validate(); err == nil {
		t.Fatal("credential-like path accepted")
	}
	base.Sources[0].CanonicalURL = "https://example.org/source"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	base.Summary = strings.Repeat("x", domain.MaxResearchContextBytes)
	if err := base.Validate(); err == nil {
		t.Fatal("oversized context summary accepted")
	}
}
