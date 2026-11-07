package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/report/obsidian"
)

// SetObsidianExportDir enables POST /api/runs/{runID}/report/obsidian,
// which writes the report as an Obsidian vault folder into dir (default
// <documents>/Qwenchik, next to the auto-saved PDFs).
func (s *Server) SetObsidianExportDir(dir string) { s.obsidianDir = dir }

func (s *Server) runReportObsidian(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusNotFound, "not_research", "Экспорт доступен только для исследований.", false)
		return
	}
	if !run.Status.Terminal() {
		writeError(w, http.StatusConflict, "report_not_ready", "Исследование ещё не завершено.", true)
		return
	}
	if s.obsidianDir == "" || s.reportPDF == nil {
		writeError(w, http.StatusServiceUnavailable, "obsidian_export_unavailable", "Экспорт в Obsidian недоступен: не настроена папка документов.", false)
		return
	}
	task, err := s.service.GetTask(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	markdown, reportJSON, err := s.reportPDF.ReportFiles(r.Context(), id)
	if err != nil {
		s.logger.Warn("report files for Obsidian export are unreadable", "run_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "report_unreadable", "Не удалось прочитать сохранённый отчёт.", true)
		return
	}
	if strings.TrimSpace(markdown) == "" {
		if messages, err := v.ConversationMessages(r.Context(), run.ConversationID); err == nil {
			for _, message := range messages {
				if message.ID == run.AssistantMessageID {
					markdown = message.Content
				}
			}
		}
	}
	if strings.TrimSpace(markdown) == "" {
		writeError(w, http.StatusNotFound, "report_not_found", "У исследования нет сохранённого отчёта.", false)
		return
	}
	date := time.Now()
	if run.CompletedAt != nil {
		date = *run.CompletedAt
	}
	vault, err := obsidian.Build(markdown, reportJSON, task.Title, date)
	if err != nil {
		writeError(w, http.StatusNotFound, "report_not_found", "У исследования нет сохранённого отчёта.", false)
		return
	}
	path, err := obsidian.Write(s.obsidianDir, vault)
	if err != nil {
		s.logger.Warn("Obsidian export could not write the vault", "run_id", id, "dir", s.obsidianDir, "error", err)
		writeError(w, http.StatusInternalServerError, "obsidian_write_failed", "Не удалось записать папку Obsidian.", true)
		return
	}
	s.logger.Info("research report exported to Obsidian", "run_id", id, "path", path)
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "notes": len(vault.Notes)})
}
