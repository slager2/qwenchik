package config

import (
	"errors"
	"strings"
)

// Report PDF auto-save modes (REPORT_PDF_AUTO).
const (
	ReportPDFAutoLong   = "auto"
	ReportPDFAutoAlways = "always"
	ReportPDFAutoNever  = "never"
)

func loadReportPDF(cfg *Config) error {
	cfg.PDFRendererBin = strings.TrimSpace(env("PDF_RENDERER_BIN", ""))
	cfg.ReportPDFDir = strings.TrimSpace(env("REPORT_PDF_DIR", ""))
	cfg.ReportPDFAuto = strings.ToLower(strings.TrimSpace(env("REPORT_PDF_AUTO", ReportPDFAutoLong)))
	switch cfg.ReportPDFAuto {
	case ReportPDFAutoLong, ReportPDFAutoAlways, ReportPDFAutoNever:
		return nil
	}
	return errors.New("REPORT_PDF_AUTO must be auto, always or never")
}
