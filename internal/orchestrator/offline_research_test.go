package orchestrator

import (
	"context"
	"encoding/json"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/tools"
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineCorpusResearchUsesDurableNotesAndReport(t *testing.T) {
	ctx := context.Background()
	f := newResearchRunStore(t)
	p := filepath.Join(t.TempDir(), "corpus.json")
	raw := `{"documents":[{"url":"https://docs.example.com/protocol","title":"Specification","text":"Protocol architecture specification describes explicit version negotiation and compatibility limitations."},{"url":"https://reference.example.net/protocol","title":"Independent reference","text":"Protocol architecture and limitations include incompatible versions. Compatibility requires explicit version negotiation."}]}`
	if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	corpus, err := research.LoadOfflineCorpus(p)
	if err != nil {
		t.Fatal(err)
	}
	f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) {
		s.Policy.PrivacyMode = domain.PrivacyOffline
		s.Policy.OfflineCorpusSHA256 = corpus.SHA256()
		s.Policy.SectionedReportEnabled = true
		s.Budget.MaxLLMCalls = 40
	})
	files, err := artifacts.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	fetchPolicy := research.DefaultFetchPolicy()
	fetchPolicy.TempRoot = filepath.Join(t.TempDir(), "fetch-temp")
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{SearchProvider: corpus, Fetcher: corpus, Extractor: corpus, Artifacts: files, Repository: f.store, FetchPolicy: fetchPolicy, MaxFetchBytes: 128 << 20}); err != nil {
		t.Fatal(err)
	}
	w := New(f.store, llm.NewGuardedClient(reportModelFixture(t, "valid")), registry, Options{ReportArtifacts: files})
	w.runTask(ctx, f.task)
	task, err := f.store.GetTask(ctx, f.task.ID)
	if err != nil || task.Status != domain.StatusCompleted {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	evidence, err := f.store.Evidence(ctx, f.task.ID)
	if err != nil || len(evidence) != 2 {
		t.Fatalf("evidence=%v err=%v", evidence, err)
	}
	for _, e := range evidence {
		if e.Provider != "offline_corpus" {
			t.Fatalf("network source entered offline report: %+v", e)
		}
	}
	checkpoint, err := f.store.LatestCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(checkpoint.State)
	var state checkpointState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.SourceNotes) != 2 || state.Report == nil || state.Report.ArtifactID == "" {
		t.Fatalf("offline notes/report missing: %+v", state.Report)
	}
	runtime, err := f.store.RunRuntime(ctx, f.task.ID)
	if err != nil || runtime["privacy_mode"] != domain.PrivacyOffline || runtime["offline_corpus_sha256"] != corpus.SHA256() {
		t.Fatalf("runtime=%v err=%v", runtime, err)
	}
}
