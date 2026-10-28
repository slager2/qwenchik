package api

import (
	"context"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"net/http"
	"strconv"
)

func (s *Server) inferenceSummaries(w http.ResponseWriter, r *http.Request) {
	service, ok := s.conversationService(w)
	if !ok {
		return
	}
	id := r.PathValue("runID")
	if _, err := service.GetRun(r.Context(), id); err != nil {
		s.writeServiceError(w, err)
		return
	}
	after := int64(0)
	until := int64(0)
	if raw := r.URL.Query().Get("until"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, 400, "invalid_cursor", "Invalid summary boundary", false)
			return
		}
		until = n
	}
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, 400, "invalid_cursor", "Invalid summary cursor", false)
			return
		}
		after = n
	}
	reader, ok := s.service.(interface {
		InferenceSummaries(context.Context, string, int64) ([]sqlite.InvocationSummaryRecord, error)
	})
	if !ok {
		writeError(w, 503, "summaries_unavailable", "Inference summaries unavailable", true)
		return
	}
	items, err := reader.InferenceSummaries(r.Context(), id, after)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	out := []sqlite.InvocationSummaryRecord{}
	next := after
	complete := len(items) < 128
	for _, item := range items {
		if until > 0 && item.Seq > until {
			complete = true
			break
		}
		safe, ok := SafeInferenceSummary(item.Summary)
		if !ok {
			writeError(w, 503, "summaries_invalid", "Invalid durable inference summary", false)
			return
		}
		item.Summary = safe
		out = append(out, item)
		next = item.Seq
	}
	if until > 0 && next >= until {
		complete = true
	}
	writeJSON(w, 200, map[string]any{"summaries": out, "next_after": next, "complete": complete, "until": until})
}
