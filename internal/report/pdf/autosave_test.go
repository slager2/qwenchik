package pdf

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type fakeAutosaveRepo struct {
	pending  []domain.ReportPDFCandidate
	recorded map[string]string
	payloads map[string]map[string]any
}

func (f *fakeAutosaveRepo) PendingReportPDFAutosaves(context.Context, time.Time, int) ([]domain.ReportPDFCandidate, error) {
	var out []domain.ReportPDFCandidate
	for _, item := range f.pending {
		if _, done := f.recorded[item.TaskID]; !done {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f *fakeAutosaveRepo) RecordReportPDFAutosave(_ context.Context, taskID, kind string, payload map[string]any) error {
	if f.recorded == nil {
		f.recorded, f.payloads = map[string]string{}, map[string]map[string]any{}
	}
	f.recorded[taskID], f.payloads[taskID] = kind, payload
	return nil
}

func TestLongResearchRule(t *testing.T) {
	start := time.Date(2026, 11, 5, 22, 0, 0, 0, time.UTC)
	cases := []struct {
		candidate domain.ReportPDFCandidate
		runes     int
		want      string
	}{
		{domain.ReportPDFCandidate{Profile: domain.ResearchProfileOvernight}, 0, "profile"},
		{domain.ReportPDFCandidate{Profile: domain.ResearchProfileDeepLaptop}, 0, "profile"},
		{domain.ReportPDFCandidate{Profile: domain.ResearchProfileStandard, StartedAt: start, CompletedAt: start.Add(25 * time.Minute)}, 0, "wall_time"},
		{domain.ReportPDFCandidate{Profile: domain.ResearchProfileQuick}, LongReportRunes, "report_size"},
		{domain.ReportPDFCandidate{Profile: domain.ResearchProfileQuick, StartedAt: start, CompletedAt: start.Add(2 * time.Minute)}, 14000, ""},
	}
	for _, tc := range cases {
		if _, got := LongResearch(tc.candidate, tc.runes); got != tc.want {
			t.Errorf("LongResearch(%+v, %d)=%q want %q", tc.candidate, tc.runes, got, tc.want)
		}
	}
}

func TestAutosaverWritesLongRunsWithoutOverwriting(t *testing.T) {
	store := &memoryArtifacts{}
	for _, id := range []string{"long-1", "long-2", "short", "big"} {
		content := "# Отчёт\n\nТекст."
		if id == "big" {
			content = "# Большой\n\n" + strings.Repeat("я", LongReportRunes)
		}
		store.add(t, id, MarkdownArtifactName, "text/markdown; charset=utf-8", content)
	}
	completed := time.Date(2026, 11, 6, 7, 30, 0, 0, time.Local)
	repo := &fakeAutosaveRepo{pending: []domain.ReportPDFCandidate{
		{TaskID: "long-1", Title: "Ночное исследование", Profile: domain.ResearchProfileOvernight, CompletedAt: completed},
		{TaskID: "long-2", Title: "Ночное исследование", Profile: domain.ResearchProfileLongLaptop, CompletedAt: completed},
		{TaskID: "short", Title: "Быстрый вопрос", Profile: domain.ResearchProfileQuick, CompletedAt: completed, ReportMarkdownBytes: 30},
		{TaskID: "big", Title: "Большой отчёт", Profile: domain.ResearchProfileQuick, CompletedAt: completed, ReportMarkdownBytes: int64(2 * LongReportRunes)},
		{TaskID: "missing", Title: "Без отчёта", Profile: domain.ResearchProfileOvernight, CompletedAt: completed},
	}}
	dir := filepath.Join(t.TempDir(), "Документы", "Qwenchik")
	renderer := &countingRenderer{}
	saver := &Autosaver{Exporter: &Exporter{Renderer: renderer, Lister: store, Artifacts: store, Recorder: store}, Repo: repo, Dir: dir, Mode: AutoLong}
	if err := saver.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-11-06 Ночное исследование.pdf", "2026-11-06 Ночное исследование (2).pdf", "2026-11-06 Большой отчёт.pdf"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !strings.HasPrefix(string(data), "%PDF-") {
			t.Fatalf("missing auto-saved %q: %v", name, err)
		}
	}
	want := map[string]string{"long-1": eventAutosaved, "long-2": eventAutosaved, "big": eventAutosaved, "short": eventSkipped, "missing": eventSkipped}
	for id, kind := range want {
		if repo.recorded[id] != kind {
			t.Errorf("run %s recorded %q want %q", id, repo.recorded[id], kind)
		}
	}
	if repo.payloads["big"]["reason"] != "report_size" || repo.payloads["long-1"]["reason"] != "profile" {
		t.Fatalf("unexpected reasons: %+v", repo.payloads)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 || renderer.calls != 3 {
		t.Fatalf("unexpected files %v or renders %d", entries, renderer.calls)
	}
	// A second pass finds nothing left to do.
	if err := saver.Tick(context.Background()); err != nil || renderer.calls != 3 {
		t.Fatalf("second pass re-rendered: %v %d", err, renderer.calls)
	}
}

func TestAutosaverRecordsFailuresAndHonoursModes(t *testing.T) {
	store := &memoryArtifacts{}
	store.add(t, "run", MarkdownArtifactName, "text/markdown; charset=utf-8", "# T")
	repo := &fakeAutosaveRepo{pending: []domain.ReportPDFCandidate{{TaskID: "run", Title: "T", Profile: domain.ResearchProfileQuick}}}
	renderer := &countingRenderer{err: errors.New("chrome crashed")}
	saver := &Autosaver{Exporter: &Exporter{Renderer: renderer, Lister: store, Artifacts: store}, Repo: repo, Dir: t.TempDir(), Mode: AutoAlways}
	if err := saver.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.recorded["run"] != eventFailed || renderer.calls != 1 {
		t.Fatalf("failure not recorded: %+v", repo.recorded)
	}
	// "never" and a missing renderer return immediately.
	done := make(chan struct{})
	go func() {
		(&Autosaver{Exporter: &Exporter{Renderer: renderer}, Repo: repo, Mode: AutoNever}).Run(context.Background())
		(&Autosaver{Exporter: &Exporter{}, Repo: repo, Mode: AutoLong}).Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled auto-saver kept running")
	}
}

func TestWriteUniqueAndDocumentsDir(t *testing.T) {
	dir := t.TempDir()
	first, err := WriteUnique(dir, "a.pdf", []byte("1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteUnique(dir, "a.pdf", []byte("2"))
	if err != nil || filepath.Base(second) != "a (2).pdf" {
		t.Fatalf("unexpected second path %q %v", second, err)
	}
	if data, _ := os.ReadFile(first); string(data) != "1" {
		t.Fatal("existing file overwritten")
	}
	if _, err := WriteUnique("", "a.pdf", nil); err == nil {
		t.Fatal("empty dir accepted")
	}
	if docs := DocumentsDir(); docs != "" && !filepath.IsAbs(docs) {
		t.Fatalf("documents dir not absolute: %q", docs)
	}
}
