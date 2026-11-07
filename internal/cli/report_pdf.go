package cli

import (
	"log/slog"
	"time"

	"github.com/slager2/qwenchik/internal/config"
	reportpdf "github.com/slager2/qwenchik/internal/report/pdf"
)

// newReportPDFExporter wires the research report PDF export. A missing
// browser leaves the exporter without a renderer: downloads then report the
// renderer as unavailable and the UI disables the button.
func newReportPDFExporter(cfg config.Config, lister reportpdf.ArtifactLister, store reportpdf.ArtifactStore, recorder reportpdf.ArtifactRecorder, logger *slog.Logger) *reportpdf.Exporter {
	exporter := &reportpdf.Exporter{Lister: lister, Artifacts: store, Recorder: recorder, Logger: logger}
	binary, err := reportpdf.DetectChrome(cfg.PDFRendererBin)
	if err != nil {
		logger.Warn("research report PDF export disabled", "error", err)
		return exporter
	}
	exporter.Renderer = reportpdf.NewChrome(binary)
	logger.Info("research report PDF renderer detected", "binary", binary)
	return exporter
}

// newReportPDFAutosaver saves PDFs of long research runs (REPORT_PDF_AUTO)
// into REPORT_PDF_DIR, default <XDG documents>/Qwenchik. Only runs that
// complete from now on (with a short grace window for a restart right after
// completion) are considered.
func newReportPDFAutosaver(cfg config.Config, exporter *reportpdf.Exporter, repo reportpdf.AutosaveRepository, logger *slog.Logger) *reportpdf.Autosaver {
	dir := cfg.ReportPDFDir
	if dir == "" {
		dir = reportpdf.DefaultAutosaveDir()
	}
	mode := cfg.ReportPDFAuto
	if dir == "" && mode != config.ReportPDFAutoNever {
		logger.Warn("research report PDF auto-save disabled: no documents directory")
		mode = config.ReportPDFAutoNever
	}
	if mode != config.ReportPDFAutoNever && exporter.Available() {
		logger.Info("research report PDF auto-save enabled", "mode", mode, "dir", dir)
	}
	return &reportpdf.Autosaver{Exporter: exporter, Repo: repo, Dir: dir, Mode: mode, Since: time.Now().Add(-10 * time.Minute), Logger: logger}
}

// reportExportDir is where report exports (auto-saved PDFs, Obsidian
// vaults) go: REPORT_PDF_DIR, default <XDG documents>/Qwenchik.
func reportExportDir(cfg config.Config) string {
	if cfg.ReportPDFDir != "" {
		return cfg.ReportPDFDir
	}
	return reportpdf.DefaultAutosaveDir()
}
