package config

import "testing"

func TestLoadReportPDF(t *testing.T) {
	t.Setenv("PDF_RENDERER_BIN", " /usr/bin/chromium ")
	t.Setenv("REPORT_PDF_DIR", "")
	t.Setenv("REPORT_PDF_AUTO", "Always")
	var cfg Config
	if err := loadReportPDF(&cfg); err != nil || cfg.PDFRendererBin != "/usr/bin/chromium" || cfg.ReportPDFAuto != ReportPDFAutoAlways || cfg.ReportPDFDir != "" {
		t.Fatalf("unexpected config %+v %v", cfg, err)
	}
	t.Setenv("REPORT_PDF_AUTO", "sometimes")
	if err := loadReportPDF(&cfg); err == nil {
		t.Fatal("accepted invalid REPORT_PDF_AUTO")
	}
}
