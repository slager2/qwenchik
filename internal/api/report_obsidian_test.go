package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	reportpdf "github.com/slager2/qwenchik/internal/report/pdf"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

func TestRunReportObsidianEndpoint(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	h := server.Handler()
	rec := conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Гранты","mode":"research"}`, "", 201)
	var c domain.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	rec = conversationRequest(t, h, "POST", "/api/conversations/"+c.ID+"/messages", `{"content":"Какие гранты?","mode":"research"}`, "obsidian-submit", 202)
	var submitted domain.SubmitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	path := "/api/runs/" + submitted.Run.ID + "/report/obsidian"
	conversationRequest(t, h, "POST", path, "", "", http.StatusConflict)
	if _, ok, err := store.ClaimNext(ctx); err != nil || !ok {
		t.Fatal(err)
	}
	for _, status := range []domain.TaskStatus{domain.StatusRunning, domain.StatusVerifying} {
		if _, err := store.TransitionTask(ctx, submitted.Run.ID, status, nil, "", "user", nil); err != nil {
			t.Fatal(err)
		}
	}
	artifactStore, err := artifacts.New(filepath.Join(t.TempDir(), "artifacts"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	report := "# Гранты\n\n## Коротко\n\n- Вывод.\n\n## Stipendium Hungaricum\n\nФакт.\n\n## Источники\n\n1. SH\n"
	artifact, ref, err := artifactStore.Put(ctx, submitted.Run.ID, "research_report.md", "text/markdown; charset=utf-8", []byte(report))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveResearchReportArtifact(ctx, artifact, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, submitted.Run.ID, domain.StatusCompleted, nil, "task.completed", "user", nil); err != nil {
		t.Fatal(err)
	}
	// Without a configured folder the export is unavailable.
	conversationRequest(t, h, "POST", path, "", "", http.StatusServiceUnavailable)

	dir := t.TempDir()
	server.SetReportPDF(&reportpdf.Exporter{Lister: store, Artifacts: artifactStore, Recorder: store})
	server.SetObsidianExportDir(dir)
	rec = conversationRequest(t, h, "POST", path, "", "", http.StatusOK)
	var result struct {
		Path  string `json:"path"`
		Notes int    `json:"notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Notes != 4 || filepath.Dir(result.Path) != dir {
		t.Fatalf("unexpected export result %s %v", rec.Body.String(), err)
	}
	moc, err := os.ReadFile(filepath.Join(result.Path, "00 MOC.md"))
	if err != nil || !strings.Contains(string(moc), "[[01 Stipendium Hungaricum]]") || !strings.Contains(string(moc), "> [!abstract] TL;DR") {
		t.Fatalf("MOC not written: %q %v", moc, err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "01 Stipendium Hungaricum.md")); err != nil {
		t.Fatal(err)
	}
	conversationRequest(t, h, "POST", "/api/runs/missing/report/obsidian", "", "", http.StatusNotFound)
}
