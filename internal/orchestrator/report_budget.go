package orchestrator

import (
	"encoding/json"
	"fmt"
	"github.com/slager2/qwenchik/internal/domain"
	"strings"
)

// Byte exhaustion is a deterministic partial delivery, not another inference
// retry. Full provenance/state remains in the checkpoint and Evidence Ledger.
func boundedReportDelivery(runID string, r *domain.ResearchReportState, sources []citationEvidence, criteria []string, budget, paragraphs int) (string, []byte, error) {
	r.ByteBudgetFallback = true
	r.Outcome = "partial"
	r.UnrepresentedCriteria = append([]string{}, criteria...)
	for excerptLimit := min(500, budget/8); excerptLimit >= 0; excerptLimit /= 2 {
		var b strings.Builder
		ids := []string{}
		for _, source := range sources[:min(len(sources), min(2, paragraphs))] {
			text := truncateBytesUTF8(reportText(source.Excerpt), excerptLimit)
			fmt.Fprintf(&b, "%s [%s]\n\n", text, source.EvidenceID)
			ids = append(ids, source.EvidenceID)
			if len(source.URL) <= 180 {
				fmt.Fprintf(&b, "Источник: <%s>\n\n", source.URL)
			}
		}
		body := b.String()
		r.Sections = []domain.ReportSection{{ID: "section_budget", Title: "Сохранённые выдержки", EvidenceIDs: ids, Criteria: []string{}, Status: "fallback_excerpts", Body: body, Limitation: "report_byte_budget"}}
		answer := "## Частичный результат\n\nИсчерпан бюджет размера отчёта. Сохранены дословные выдержки; содержательное покрытие запроса не подтверждено. Полные источники и состояние сохранены в Evidence Ledger.\n\n" + body
		encoded, err := json.Marshal(map[string]any{"type": "research_report", "version": 1, "run_id": runID, "markdown": answer, "compact_metadata": true, "metadata": map[string]any{"final_outcome": "partial", "limitation": "report_byte_budget", "unrepresented_criteria": criteria, "generated_at": r.GeneratedAt, "privacy_mode": r.PrivacyMode, "sections": []map[string]string{{"id": "section_budget", "status": "fallback_excerpts"}}}, "actual_size": map[string]int{"markdown_bytes": len(answer), "sections": 1, "accepted_sources": len(sources)}})
		if err != nil {
			return "", nil, err
		}
		if len(answer) <= budget && len(encoded) <= budget {
			return answer, encoded, nil
		}
		if excerptLimit == 0 {
			break
		}
	}
	return "", nil, errInputBudgetExceeded
}
