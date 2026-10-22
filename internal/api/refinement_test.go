package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/app"
	"github.com/slager2/qwenchik/internal/domain"
)

func TestRefinementAPIRequiresTypedIdempotentCommandAndNeverDirectlyPromotes(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	base := domain.AgentBundle{BundleID: "research-default", Version: 1, ModelProfile: "local-qwen", PromptHash: strings.Repeat("a", 64), RoleSchemaVersion: 1, EnabledTools: []string{"text.inspect"}, MemoryPolicyVersion: 1, BudgetProfile: "snapshotted-research-v1", EvaluatorVersion: "baseline-v1"}
	base.BundleHash = base.Hash()
	if _, err := store.EnsureBaselineBundle(ctx, base); err != nil {
		t.Fatal(err)
	}
	model := domain.ModelProfileSnapshot{Bundle: &base, Version: 1, Model: "local-qwen", BaseURL: "http://127.0.0.1:8000/v1", ContextLength: 8192, MaxOutputTokens: 512, TopP: 0.8, PromptVersion: "local-research-v1", SchemaVersion: 1}
	server.service.(*app.Service).SetResearchRunDefaults("standard", map[string]domain.ResearchPolicy{"standard": domain.DefaultResearchPolicy()}, model)
	source, err := store.CreateTask(ctx, domain.Task{ID: "refinement-source", Title: "Source", Description: "Goal"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueTask(ctx, source.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext(ctx); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, source.ID, domain.StatusFailed, nil, "task.failed", "user", nil); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"source_task_ids":[%q]}`, source.ID)
	handler := server.Handler()
	conversationRequest(t, handler, "POST", "/api/refinement", body, "", 400)
	first := conversationRequest(t, handler, "POST", "/api/refinement", body, "bounded-refinement", 202)
	second := conversationRequest(t, handler, "POST", "/api/refinement", body, "bounded-refinement", 202)
	var a, b domain.Task
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if a.ID == "" || a.ID != b.ID {
		t.Fatal("command was not idempotent")
	}
	model.Temperature = 0.4
	server.service.(*app.Service).SetResearchRunDefaults("standard", map[string]domain.ResearchPolicy{"standard": domain.DefaultResearchPolicy()}, model)
	replayed := conversationRequest(t, handler, "POST", "/api/refinement", body, "bounded-refinement", 202)
	var again domain.Task
	_ = json.Unmarshal(replayed.Body.Bytes(), &again)
	if again.ID != a.ID {
		t.Fatal("server defaults changed the identity of an already committed command")
	}
	conversationRequest(t, handler, "POST", "/api/refinement", `{"source_task_ids":[],"security_policy":{"host_shell":true}}`, "unsafe", 400)
	conversationRequest(t, handler, "GET", "/api/bundles/"+base.BundleHash, "", "", 200)
	active, err := store.ActiveBundle(ctx, base.BundleHash)
	if err != nil || active.BundleHash != base.BundleHash {
		t.Fatal("starting refinement promoted a bundle")
	}
}
