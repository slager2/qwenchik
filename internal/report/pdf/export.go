package pdf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

const (
	MarkdownArtifactName = "research_report.md"
	JSONArtifactName     = "research_report.json"
	PDFMIMEType          = "application/pdf"
)

// ErrNoReport means the run has neither a saved report nor answer text.
var ErrNoReport = errors.New("research run has no report to export")

var cachedPDFName = regexp.MustCompile(`^research_report-[0-9a-f]{16}\.pdf$`)

// IsCachedPDFName reports whether name is a PDF cache artifact name.
func IsCachedPDFName(name string) bool { return cachedPDFName.MatchString(name) }

// ArtifactLister lists a task's artifact metadata, newest first.
type ArtifactLister interface {
	Artifacts(ctx context.Context, taskID string) ([]domain.Artifact, error)
}

// ArtifactStore reads and writes artifact content.
type ArtifactStore interface {
	Read(ctx context.Context, taskID, artifactID string) ([]byte, error)
	Put(ctx context.Context, taskID, name, mimeType string, content []byte) (domain.Artifact, string, error)
}

// ArtifactRecorder records a cached PDF artifact in the task database.
type ArtifactRecorder interface {
	SaveReportPDFArtifact(ctx context.Context, artifact domain.Artifact, ref string) (domain.Artifact, error)
}

// Run identifies the research run to export.
type Run struct {
	ID    string
	Title string
	// FallbackMarkdown is printed when the run has no report artifact,
	// e.g. the saved assistant answer of an older run.
	FallbackMarkdown string
	CompletedAt      time.Time
}

// Result is an exported PDF.
type Result struct {
	PDF      []byte
	Title    string
	Filename string
	Cached   bool
}

// Exporter renders run reports and caches the PDF as a run artifact keyed by
// the report content, so repeated downloads do not start a browser.
type Exporter struct {
	Renderer  Renderer
	Lister    ArtifactLister
	Artifacts ArtifactStore
	Recorder  ArtifactRecorder
	Logger    *slog.Logger
	mu        sync.Mutex
}

// Available reports whether a renderer is configured.
func (e *Exporter) Available() bool { return e != nil && e.Renderer != nil }

func (e *Exporter) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

func newestByName(items []domain.Artifact, name string) *domain.Artifact {
	var found *domain.Artifact
	for i := range items {
		if items[i].Name == name && (found == nil || items[i].CreatedAt.After(found.CreatedAt)) {
			found = &items[i]
		}
	}
	return found
}

func (e *Exporter) readVerified(ctx context.Context, artifact *domain.Artifact) ([]byte, error) {
	data, err := e.Artifacts.Read(ctx, artifact.TaskID, artifact.ID)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != artifact.SHA256 || int64(len(data)) != artifact.SizeBytes {
		return nil, fmt.Errorf("artifact %s failed its integrity check", artifact.Name)
	}
	return data, nil
}

// ReportFiles returns the newest integrity-checked report Markdown and
// research_report.json of a run (either may be empty); no renderer is
// needed. Other exports (Obsidian) build on it.
func (e *Exporter) ReportFiles(ctx context.Context, runID string) (string, []byte, error) {
	if e == nil || e.Lister == nil || e.Artifacts == nil {
		return "", nil, errors.New("report artifact store is unavailable")
	}
	items, err := e.Lister.Artifacts(ctx, runID)
	if err != nil {
		return "", nil, err
	}
	var markdown string
	var raw []byte
	if artifact := newestByName(items, JSONArtifactName); artifact != nil {
		if raw, err = e.readVerified(ctx, artifact); err != nil {
			return "", nil, err
		}
	}
	if artifact := newestByName(items, MarkdownArtifactName); artifact != nil {
		data, err := e.readVerified(ctx, artifact)
		if err != nil {
			return "", nil, err
		}
		markdown = string(data)
	}
	return markdown, raw, nil
}

// Export returns the PDF for run, rendering it at most once per report
// content and layout version.
func (e *Exporter) Export(ctx context.Context, run Run) (Result, error) {
	if !e.Available() {
		return Result{}, ErrRendererUnavailable
	}
	if e.Lister == nil || e.Artifacts == nil {
		return Result{}, errors.New("report artifact store is unavailable")
	}
	items, err := e.Lister.Artifacts(ctx, run.ID)
	if err != nil {
		return Result{}, err
	}
	var markdown string
	var meta Report
	var markdownHash, jsonHash string
	if artifact := newestByName(items, JSONArtifactName); artifact != nil {
		raw, err := e.readVerified(ctx, artifact)
		if err != nil {
			return Result{}, err
		}
		if meta, err = ParseReportJSON(raw); err != nil {
			e.logger().Warn("research report metadata is unreadable; printing without it", "run_id", run.ID, "error", err)
			meta = Report{}
		}
		jsonHash = artifact.SHA256
	}
	if artifact := newestByName(items, MarkdownArtifactName); artifact != nil {
		raw, err := e.readVerified(ctx, artifact)
		if err != nil {
			return Result{}, err
		}
		markdown, markdownHash = string(raw), artifact.SHA256
	} else if strings.TrimSpace(meta.Markdown) != "" {
		markdown = meta.Markdown
	} else if strings.TrimSpace(run.FallbackMarkdown) != "" {
		markdown = run.FallbackMarkdown
	}
	if strings.TrimSpace(markdown) == "" {
		return Result{}, ErrNoReport
	}
	if markdownHash == "" {
		sum := sha256.Sum256([]byte(markdown))
		markdownHash = hex.EncodeToString(sum[:])
	}
	reportTitle := strings.TrimSpace(meta.Title)
	if reportTitle == "" {
		reportTitle = MarkdownTitle(markdown)
	}
	reportTitle = CleanTitle(reportTitle)
	fileTitle := strings.TrimSpace(run.Title)
	if fileTitle == "" {
		fileTitle = reportTitle
	}
	generated := meta.GeneratedAt
	if generated.IsZero() {
		generated = run.CompletedAt
	}
	keySum := sha256.Sum256([]byte(strings.Join([]string{TemplateVersion, run.ID, markdownHash, jsonHash, reportTitle}, "\x00")))
	cacheName := "research_report-" + hex.EncodeToString(keySum[:])[:16] + ".pdf"
	result := Result{Title: fileTitle, Filename: Filename(fileTitle)}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Recorder != nil {
		// Re-list under the lock: a concurrent request may have cached it.
		if current, err := e.Lister.Artifacts(ctx, run.ID); err == nil {
			items = current
		}
		for i := range items {
			if items[i].Name == cacheName && items[i].MIMEType == PDFMIMEType {
				if data, err := e.readVerified(ctx, &items[i]); err == nil {
					result.PDF, result.Cached = data, true
					return result, nil
				} else {
					e.logger().Warn("cached report PDF is unreadable; rendering again", "run_id", run.ID, "error", err)
				}
			}
		}
	}
	html, err := BuildHTML(Document{Title: reportTitle, Question: meta.Question, GeneratedAt: generated, RunID: run.ID, Outcome: meta.Outcome, Sources: meta.Sources, Markdown: markdown})
	if err != nil {
		return Result{}, err
	}
	data, err := e.Renderer.Render(ctx, html)
	if err != nil {
		return Result{}, err
	}
	result.PDF = data
	if e.Recorder != nil {
		artifact, ref, err := e.Artifacts.Put(ctx, run.ID, cacheName, PDFMIMEType, data)
		if err == nil {
			_, err = e.Recorder.SaveReportPDFArtifact(ctx, artifact, ref)
		}
		if err != nil {
			e.logger().Warn("report PDF was rendered but not cached", "run_id", run.ID, "error", err)
		}
	}
	return result, nil
}
