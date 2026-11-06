package cli

import (
	"log/slog"

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
