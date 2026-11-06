package pdf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type memoryArtifacts struct {
	mu    sync.Mutex
	items []domain.Artifact
	data  map[string][]byte
	saved int
}

func (m *memoryArtifacts) Artifacts(_ context.Context, taskID string) ([]domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Artifact
	for _, item := range m.items {
		if item.TaskID == taskID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (m *memoryArtifacts) Read(_ context.Context, taskID, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.data[taskID+"/"+id]
	if !ok {
		return nil, errors.New("missing")
	}
	return data, nil
}

func (m *memoryArtifacts) Put(_ context.Context, taskID, name, mimeType string, content []byte) (domain.Artifact, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[taskID+"/"+hash] = append([]byte(nil), content...)
	return domain.Artifact{ID: hash, TaskID: taskID, Name: name, MIMEType: mimeType, SHA256: hash, SizeBytes: int64(len(content)), CreatedAt: time.Now()}, filepath.Join(taskID, hash), nil
}

func (m *memoryArtifacts) SaveReportPDFArtifact(_ context.Context, artifact domain.Artifact, _ string) (domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append(m.items, artifact)
	m.saved++
	return artifact, nil
}

func (m *memoryArtifacts) add(t *testing.T, taskID, name, mimeType, content string) domain.Artifact {
	t.Helper()
	artifact, _, err := m.Put(context.Background(), taskID, name, mimeType, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	m.items = append(m.items, artifact)
	return artifact
}

type countingRenderer struct {
	calls int
	last  string
	err   error
}

func (r *countingRenderer) Render(_ context.Context, html []byte) ([]byte, error) {
	r.calls++
	r.last = string(html)
	if r.err != nil {
		return nil, r.err
	}
	return []byte("%PDF-1.7 fake " + time.Now().String()), nil
}

func TestExporterRendersOnceAndServesCache(t *testing.T) {
	store := &memoryArtifacts{}
	store.add(t, "run-1", MarkdownArtifactName, "text/markdown; charset=utf-8", "# Отчёт\n\nТекст отчёта.")
	store.add(t, "run-1", JSONArtifactName, "application/vnd.qwenchik.research-report+json", `{"metadata":{"title":"Отчёт","research_question":"Что изучить?","final_outcome":"completed"},"evidence_coverage":[{"title":"Источник","url":"https://example.org/a"}]}`)
	renderer := &countingRenderer{}
	exporter := &Exporter{Renderer: renderer, Lister: store, Artifacts: store, Recorder: store}
	first, err := exporter.Export(context.Background(), Run{ID: "run-1", Title: "Роадмап: биоинформатика"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached || renderer.calls != 1 || first.Filename != "Роадмап биоинформатика.pdf" {
		t.Fatalf("unexpected first export: cached=%v calls=%d name=%q", first.Cached, renderer.calls, first.Filename)
	}
	if !strings.Contains(renderer.last, "Что изучить?") || !strings.Contains(renderer.last, "https://example.org/a") {
		t.Fatalf("title page lacks report metadata")
	}
	second, err := exporter.Export(context.Background(), Run{ID: "run-1", Title: "Роадмап: биоинформатика"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached || renderer.calls != 1 || string(second.PDF) != string(first.PDF) || store.saved != 1 {
		t.Fatalf("cache not used: cached=%v calls=%d saved=%d", second.Cached, renderer.calls, store.saved)
	}
	// A changed report (new markdown artifact) is rendered again.
	time.Sleep(time.Millisecond)
	store.add(t, "run-1", MarkdownArtifactName, "text/markdown; charset=utf-8", "# Отчёт\n\nНовый текст.")
	if third, err := exporter.Export(context.Background(), Run{ID: "run-1"}); err != nil || third.Cached || renderer.calls != 2 || third.Filename != "Отчёт.pdf" {
		t.Fatalf("changed report served from cache: %+v %v calls=%d", third.Cached, err, renderer.calls)
	}
}

func TestExporterFallbackAndErrors(t *testing.T) {
	store := &memoryArtifacts{}
	renderer := &countingRenderer{}
	exporter := &Exporter{Renderer: renderer, Lister: store, Artifacts: store, Recorder: store}
	if _, err := exporter.Export(context.Background(), Run{ID: "empty"}); !errors.Is(err, ErrNoReport) {
		t.Fatalf("expected ErrNoReport, got %v", err)
	}
	result, err := exporter.Export(context.Background(), Run{ID: "legacy", FallbackMarkdown: "Сохранённый ответ"})
	if err != nil || result.Cached || !strings.Contains(renderer.last, "Сохранённый ответ") {
		t.Fatalf("fallback markdown not printed: %v", err)
	}
	corrupt := store.add(t, "corrupt", MarkdownArtifactName, "text/markdown; charset=utf-8", "text")
	store.data["corrupt/"+corrupt.ID] = []byte("tampered")
	if _, err := exporter.Export(context.Background(), Run{ID: "corrupt"}); err == nil {
		t.Fatal("tampered report was exported")
	}
	renderer.err = errors.New("boom")
	if _, err := exporter.Export(context.Background(), Run{ID: "legacy2", FallbackMarkdown: "x"}); err == nil {
		t.Fatal("renderer error swallowed")
	}
	if (&Exporter{}).Available() {
		t.Fatal("exporter without renderer reported available")
	}
	if _, err := (&Exporter{}).Export(context.Background(), Run{ID: "x"}); !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("expected ErrRendererUnavailable, got %v", err)
	}
}
