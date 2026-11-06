package api

import (
	"errors"
	"mime"
	"net/http"
	"strconv"

	reportpdf "github.com/slager2/qwenchik/internal/report/pdf"
)

// SetReportPDF enables GET /api/runs/{runID}/report.pdf. A nil exporter, or
// one without a renderer, makes the endpoint report the renderer as missing.
func (s *Server) SetReportPDF(exporter *reportpdf.Exporter) { s.reportPDF = exporter }

func (s *Server) reportPDFAvailable() bool { return s.reportPDF != nil && s.reportPDF.Available() }

func (s *Server) runReportPDF(w http.ResponseWriter, r *http.Request) {
	v, ok := s.conversationService(w)
	if !ok {
		return
	}
	id := r.PathValue("runID")
	run, err := v.GetRun(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if run.Mode != "research" {
		writeError(w, http.StatusNotFound, "not_research", "PDF доступен только для исследований.", false)
		return
	}
	if !run.Status.Terminal() {
		writeError(w, http.StatusConflict, "report_not_ready", "Исследование ещё не завершено.", true)
		return
	}
	if !s.reportPDFAvailable() {
		writeError(w, http.StatusServiceUnavailable, "pdf_renderer_unavailable", "PDF недоступен: не найден Chrome или Chromium. Укажите PDF_RENDERER_BIN.", false)
		return
	}
	task, err := s.service.GetTask(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	export := reportpdf.Run{ID: id, Title: task.Title}
	if run.CompletedAt != nil {
		export.CompletedAt = *run.CompletedAt
	}
	if messages, err := v.ConversationMessages(r.Context(), run.ConversationID); err == nil {
		for _, message := range messages {
			if message.ID == run.AssistantMessageID {
				export.FallbackMarkdown = message.Content
			}
		}
	}
	result, err := s.reportPDF.Export(r.Context(), export)
	switch {
	case errors.Is(err, reportpdf.ErrNoReport):
		writeError(w, http.StatusNotFound, "report_not_found", "У исследования нет сохранённого отчёта.", false)
		return
	case errors.Is(err, reportpdf.ErrRendererUnavailable):
		writeError(w, http.StatusServiceUnavailable, "pdf_renderer_unavailable", "PDF недоступен: не найден Chrome или Chromium. Укажите PDF_RENDERER_BIN.", false)
		return
	case err != nil:
		s.logger.Warn("report PDF export failed", "run_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "pdf_render_failed", "Не удалось подготовить PDF. Попробуйте ещё раз.", true)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": result.Filename}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(result.PDF)))
	w.Header().Set("X-Qwenchik-PDF-Cache", map[bool]string{true: "hit", false: "miss"}[result.Cached])
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.PDF)
}
