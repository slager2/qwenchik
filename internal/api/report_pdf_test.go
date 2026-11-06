package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	reportpdf "github.com/slager2/qwenchik/internal/report/pdf"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

type fakePDFRenderer struct{ calls atomic.Int32 }

func (f *fakePDFRenderer) Render(_ context.Context, html []byte) ([]byte, error) {
	f.calls.Add(1)
	if !strings.Contains(string(html), "Content-Security-Policy") {
		return nil, context.Canceled
	}
	return []byte("%PDF-1.7\nfake report\n%%EOF"), nil
}

func TestRunReportPDFEndpoint(t *testing.T) {
	ctx := context.Background()
	server, store := newTestServer(t)
	defer store.Close()
	h := server.Handler()
	rec := conversationRequest(t, h, "POST", "/api/conversations", `{"title":"Роадмап: биоинформатика","mode":"research"}`, "", 201)
	var c domain.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	rec = conversationRequest(t, h, "POST", "/api/conversations/"+c.ID+"/messages", `{"content":"Как стать биоинформатиком?","mode":"research"}`, "pdf-submit", 202)
	var submitted domain.SubmitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	runPath := "/api/runs/" + submitted.Run.ID + "/report.pdf"
	conversationRequest(t, h, "GET", runPath, "", "", http.StatusConflict)

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
	artifact, ref, err := artifactStore.Put(ctx, submitted.Run.ID, "research_report.md", "text/markdown; charset=utf-8", []byte("# Отчёт\n\n<script>alert(1)</script> Текст."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveResearchReportArtifact(ctx, artifact, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, submitted.Run.ID, domain.StatusCompleted, nil, "task.completed", "user", nil); err != nil {
		t.Fatal(err)
	}

	// Without a renderer the endpoint and health report it as unavailable.
	conversationRequest(t, h, "GET", runPath, "", "", http.StatusServiceUnavailable)
	rec = conversationRequest(t, h, "GET", "/api/health", "", "", http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"report_pdf":{"available":false}`) {
		t.Fatalf("health lacks renderer availability: %s", rec.Body.String())
	}

	renderer := &fakePDFRenderer{}
	server.SetArtifactReader(artifactStore)
	server.SetReportPDF(&reportpdf.Exporter{Renderer: renderer, Lister: store, Artifacts: artifactStore, Recorder: store})
	rec = conversationRequest(t, h, "GET", "/api/health", "", "", http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"report_pdf":{"available":true}`) {
		t.Fatalf("health lacks renderer availability: %s", rec.Body.String())
	}
	rec = conversationRequest(t, h, "GET", runPath, "", "", http.StatusOK)
	if rec.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(rec.Body.String(), "%PDF-") || rec.Header().Get("X-Qwenchik-PDF-Cache") != "miss" {
		t.Fatalf("unexpected PDF response: %v %q", rec.Header(), rec.Body.String())
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") || !strings.Contains(disposition, "filename*=utf-8''%D0%A0%D0%BE%D0%B0%D0%B4%D0%BC%D0%B0%D0%BF") {
		t.Fatalf("unexpected disposition %q", disposition)
	}
	rec = conversationRequest(t, h, "GET", runPath, "", "", http.StatusOK)
	if rec.Header().Get("X-Qwenchik-PDF-Cache") != "hit" || renderer.calls.Load() != 1 {
		t.Fatalf("second download re-rendered: calls=%d", renderer.calls.Load())
	}
	conversationRequest(t, h, "GET", "/api/runs/missing/report.pdf", "", "", http.StatusNotFound)
}
