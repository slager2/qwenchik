package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/tools"
)

func TestResearchBundleIsHashedPinnedAndRejectsRuntimeDrift(t *testing.T) {
	ctx := context.Background()
	registry := tools.NewRegistry()
	bundle := ResearchAgentBundle("local-qwen", registry)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	if other := ResearchAgentBundle("local-qwen", registry); other.BundleHash != bundle.BundleHash || other.PromptHash != bundle.PromptHash {
		t.Fatal("bundle hash is not deterministic")
	}
	f := newResearchRunStore(t)
	f.submit(t, "Привет", func(session *domain.ResearchSession) {
		session.ModelSnapshot.Model = "local-qwen"
		session.ModelSnapshot.Bundle = &bundle
	})
	wanted := bundle.BundleHash
	bundle.EnabledTools = append(bundle.EnabledTools, "forged-tool")
	if bundle.Validate() == nil {
		t.Fatal("changed tool set retained a valid hash")
	}
	f.reopen(t)
	saved := researchRunSession(t, f.store, f.task.ID)
	if saved.ModelSnapshot.Bundle == nil || saved.ModelSnapshot.Bundle.BundleHash != wanted || saved.ModelSnapshot.Bundle.Validate() != nil {
		t.Fatal("bundle was not pinned independently of caller memory")
	}
	model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) { return "OK", nil }}
	w := New(f.store, model, registry, Options{RuntimeBundleHash: strings.Repeat("b", 64)})
	if _, _, err := w.researchExecutor(ctx, f.task.ID); !errors.Is(err, errResearchRuntimeChanged) || len(model.calls()) != 0 {
		t.Fatalf("bundle drift accepted: %v", err)
	}
	w = New(f.store, llm.NewGuardedClient(model), registry, Options{RuntimeBundleHash: wanted})
	w.runTask(ctx, f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	runtime, err := f.store.RunRuntime(ctx, f.task.ID)
	if err != nil || runtime["bundle_hash"] != wanted {
		t.Fatalf("runtime identity: %v %v", runtime, err)
	}
	snapshot, err := f.store.ContextSnapshot(ctx, f.task.ID)
	if err != nil || snapshot.BundleHash != wanted {
		t.Fatalf("context identity: %+v %v", snapshot, err)
	}
}
