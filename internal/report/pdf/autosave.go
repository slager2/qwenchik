package pdf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

// Auto-save modes (REPORT_PDF_AUTO).
const (
	AutoLong   = "auto"
	AutoAlways = "always"
	AutoNever  = "never"
)

// Auto-save outcome event types; they match the sqlite store constants.
const (
	eventAutosaved = "report_pdf.autosaved"
	eventSkipped   = "report_pdf.autosave_skipped"
	eventFailed    = "report_pdf.autosave_failed"
)

// A research run is "long" when any of these holds. Wall time and profile
// capture overnight work; the size threshold catches large reports from
// shorter runs. Quick runs full of verbatim excerpts reach ~14 KB, so the
// threshold sits well above them.
const (
	LongWallTime        = 20 * time.Minute
	LongReportRunes     = 20000
	longReportByteGuess = LongReportRunes // markdown bytes >= runes
)

var longProfiles = map[string]bool{domain.ResearchProfileDeepLaptop: true, domain.ResearchProfileLongLaptop: true, domain.ResearchProfileOvernight: true}

// AutosaveRepository finds finished research runs and records outcomes.
type AutosaveRepository interface {
	PendingReportPDFAutosaves(ctx context.Context, since time.Time, limit int) ([]domain.ReportPDFCandidate, error)
	RecordReportPDFAutosave(ctx context.Context, taskID, eventType string, payload map[string]any) error
}

// Autosaver writes PDFs of long research runs into the user's documents.
type Autosaver struct {
	Exporter *Exporter
	Repo     AutosaveRepository
	Dir      string
	Mode     string
	// Since bounds which completions are considered, so enabling the
	// feature does not export every historical run.
	Since    time.Time
	Interval time.Duration
	Logger   *slog.Logger
}

func (a *Autosaver) logger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

// LongResearch reports whether a candidate qualifies under the auto rule.
func LongResearch(c domain.ReportPDFCandidate, markdownRunes int) (bool, string) {
	switch {
	case longProfiles[c.Profile]:
		return true, "profile"
	case !c.StartedAt.IsZero() && !c.CompletedAt.IsZero() && c.CompletedAt.Sub(c.StartedAt) >= LongWallTime:
		return true, "wall_time"
	case markdownRunes >= LongReportRunes:
		return true, "report_size"
	}
	return false, ""
}

// Run polls for completed research runs until ctx is done.
func (a *Autosaver) Run(ctx context.Context) {
	if a == nil || a.Mode == AutoNever || a.Repo == nil || !a.Exporter.Available() {
		return
	}
	interval := a.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := a.Tick(ctx); err != nil && ctx.Err() == nil {
			a.logger().Warn("report PDF auto-save pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick handles every pending candidate once. Errors from individual runs
// are recorded and logged; they never affect the research itself.
func (a *Autosaver) Tick(ctx context.Context) error {
	candidates, err := a.Repo.PendingReportPDFAutosaves(ctx, a.Since, 10)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.handle(ctx, candidate)
	}
	return nil
}

func (a *Autosaver) record(ctx context.Context, taskID, kind string, payload map[string]any) {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Repo.RecordReportPDFAutosave(recordCtx, taskID, kind, payload); err != nil {
		a.logger().Warn("report PDF auto-save outcome not recorded", "run_id", taskID, "error", err)
	}
}

func (a *Autosaver) handle(ctx context.Context, candidate domain.ReportPDFCandidate) {
	reason := "always"
	if a.Mode != AutoAlways {
		// Cheap checks first; the report length is known only from the
		// artifact size until the export reads it.
		long, why := LongResearch(candidate, 0)
		if !long && candidate.ReportMarkdownBytes >= longReportByteGuess {
			long, why = a.longByContent(ctx, candidate)
		}
		if !long {
			a.record(ctx, candidate.TaskID, eventSkipped, map[string]any{"reason": "not_long"})
			return
		}
		reason = why
	}
	result, err := a.Exporter.Export(ctx, Run{ID: candidate.TaskID, Title: candidate.Title, CompletedAt: candidate.CompletedAt})
	if ctx.Err() != nil {
		return // shutting down: try again after restart
	}
	if errors.Is(err, ErrNoReport) {
		a.record(ctx, candidate.TaskID, eventSkipped, map[string]any{"reason": "no_report"})
		return
	}
	if err != nil {
		a.logger().Warn("report PDF auto-save failed", "run_id", candidate.TaskID, "error", err)
		a.record(ctx, candidate.TaskID, eventFailed, map[string]any{"reason": "render_failed"})
		return
	}
	date := candidate.CompletedAt
	if date.IsZero() {
		date = time.Now()
	}
	path, err := WriteUnique(a.Dir, date.Local().Format("2006-01-02")+" "+result.Filename, result.PDF)
	if err != nil {
		a.logger().Warn("report PDF auto-save could not write the file", "run_id", candidate.TaskID, "dir", a.Dir, "error", err)
		a.record(ctx, candidate.TaskID, eventFailed, map[string]any{"reason": "write_failed"})
		return
	}
	a.logger().Info("research report PDF saved", "run_id", candidate.TaskID, "path", path, "reason", reason)
	a.record(ctx, candidate.TaskID, eventAutosaved, map[string]any{"reason": reason, "file_name": filepath.Base(path), "size_bytes": len(result.PDF)})
}

func (a *Autosaver) longByContent(ctx context.Context, candidate domain.ReportPDFCandidate) (bool, string) {
	items, err := a.Exporter.Lister.Artifacts(ctx, candidate.TaskID)
	if err != nil {
		return false, ""
	}
	artifact := newestByName(items, MarkdownArtifactName)
	if artifact == nil {
		return false, ""
	}
	data, err := a.Exporter.readVerified(ctx, artifact)
	if err != nil {
		return false, ""
	}
	return LongResearch(candidate, utf8.RuneCount(data))
}

// WriteUnique writes data to dir/name without replacing an existing file,
// adding " (2)", " (3)", … before the extension when needed.
func WriteUnique(dir, name string, data []byte) (string, error) {
	if dir == "" {
		return "", errors.New("auto-save directory is not configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for attempt := 1; attempt <= 1000; attempt++ {
		candidate := name
		if attempt > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, attempt, ext)
		}
		path := filepath.Join(dir, candidate)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := file.Write(data); err != nil {
			file.Close()
			os.Remove(path)
			return "", err
		}
		if err := file.Close(); err != nil {
			os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", errors.New("no free file name for the report PDF")
}

// DocumentsDir returns the user's documents folder: the XDG DOCUMENTS
// directory (e.g. ~/Документы), falling back to ~/Documents.
func DocumentsDir() string {
	home, _ := os.UserHomeDir()
	if binary, err := exec.LookPath("xdg-user-dir"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, binary, "DOCUMENTS").Output(); err == nil {
			dir := strings.TrimSpace(string(out))
			// xdg-user-dir answers $HOME when DOCUMENTS is not configured.
			if filepath.IsAbs(dir) && filepath.Clean(dir) != filepath.Clean(home) {
				return dir
			}
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "Documents")
}

// DefaultAutosaveDir is <documents>/Qwenchik.
func DefaultAutosaveDir() string {
	documents := DocumentsDir()
	if documents == "" {
		return ""
	}
	return filepath.Join(documents, "Qwenchik")
}
