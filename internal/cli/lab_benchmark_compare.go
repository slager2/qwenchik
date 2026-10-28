package cli

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/slager2/qwenchik/internal/domain"
)

func validBenchmarkHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

// Required fields are checked before decoding: JSON null must not silently become
// false/zero. Legacy context-check receipts still decode, but cannot be compared.
func (r *labBenchmarkResult) UnmarshalJSON(raw []byte) error {
	type plain labBenchmarkResult
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if decoded.Version == 2 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for _, key := range []string{"version", "type", "case_id", "split", "variant", "protocol_sha256", "experiment_id", "started_at", "finished_at", "conversation_id", "run_id", "status", "outcome", "wall_seconds", "counters", "model_profile_hash", "budget_hash", "budget", "dataset_sha256", "corpus_sha256", "collection_complete", "scoring_complete", "output_collected", "answer", "assistant_message_status", "output_bytes", "output_sha256", "compactions", "resource_probe_attempts", "fresh_resource_samples", "resource_sample_max_age_seconds", "stop_reason", "pause_reason", "resource_reason", "report_artifact_id", "markdown_artifact_id", "report_outcome"} {
			if value, ok := fields[key]; !ok || string(value) == "null" {
				return errors.New("version 2 receipt missing required value: " + key)
			}
		}
		for _, key := range []string{"expected_source_recall", "lexical_concept_hit_rate", "citation_reference_validity", "independent_factual_accuracy", "total_output_tokens", "sections", "unrepresented_criteria", "sampled_peak_app_rss_bytes", "sampled_peak_model_rss_bytes", "context_budget", "evidence", "errors"} {
			if _, ok := fields[key]; !ok {
				return errors.New("version 2 receipt missing nullable metric: " + key)
			}
		}
		if err := benchmarkRequiredNumbers(fields["budget"], benchmarkBudgetKeys, true); err != nil {
			return err
		}
		if err := benchmarkRequiredNumbers(fields["counters"], benchmarkCounterKeys, false); err != nil {
			return err
		}
	}
	*r = labBenchmarkResult(decoded)
	return nil
}

func validateBenchmarkResult(r labBenchmarkResult) error {
	bad := func() error { return errors.New("invalid version 2 benchmark receipt for " + r.CaseID) }
	if r.Version != 2 || r.Type != "lab_benchmark_case" || !validBenchmarkHash(r.ProtocolSHA256) || r.ExperimentID != r.ProtocolSHA256 || !validBenchmarkHash(r.ModelProfileHash) || !validBenchmarkHash(r.BudgetHash) || r.RunID == "" || r.ConversationID == "" || !r.Status.Valid() || r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || math.IsNaN(r.WallSeconds) || math.IsInf(r.WallSeconds, 0) || r.WallSeconds < 0 || r.IndependentFactualAccuracy != nil || r.TotalOutputTokens != nil || !validBenchmarkBudget(r.Budget) || r.Counters == nil || r.Compactions == nil || r.ProbeAttempts == nil || r.ResourceMaxAgeSeconds != benchmarkResourceMaxAgeSeconds {
		return bad()
	}
	switch r.ErrorCode {
	case "benchmark_policy_mismatch", "benchmark_snapshot_changed", "runtime_identity_mismatch", "benchmark_runtime_invalid":
		return errors.New("known runtime/policy drift or invalid projection is not comparable: " + r.CaseID)
	}
	budget, _ := json.Marshal(r.Budget)
	if labHash(budget) != r.BudgetHash || r.OutputBytes < 0 || *r.Compactions < 0 || *r.ProbeAttempts < 0 || r.ResourceSamples < 0 {
		return bad()
	}
	counters, _ := json.Marshal(r.Counters)
	var values map[string]int64
	if err := json.Unmarshal(counters, &values); err != nil {
		return bad()
	}
	for _, v := range values {
		if v < 0 {
			return bad()
		}
	}
	for _, p := range []*int64{r.AppRSS, r.ModelRSS, r.SampledPeakRSS} {
		if p != nil && (*p < 0 || r.ResourceSamples == 0) {
			return bad()
		}
	}
	for _, p := range []*float64{r.SourceRecall, r.ConceptHitRate, r.CitationReferenceValidity} {
		if p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 1) {
			return bad()
		}
	}
	for _, p := range []*int{r.Sections, r.UnrepresentedCriteria} {
		if p != nil && *p < 0 {
			return bad()
		}
	}
	if (r.Sections != nil || r.UnrepresentedCriteria != nil) && r.ReportArtifactID == "" {
		return bad()
	}
	if r.ReportSHA256 != "" && !validBenchmarkHash(r.ReportSHA256) {
		return bad()
	}
	if r.OutputCollected {
		if len(r.Answer) > benchmarkAnswerLimit || r.OutputBytes != len(r.Answer) || r.OutputSHA256 != labHash([]byte(r.Answer)) {
			return bad()
		}
	} else if r.Answer != "" || r.OutputBytes != 0 || r.OutputSHA256 != "" || r.CollectionComplete {
		return bad()
	}
	if r.ScoringComplete {
		if !r.CollectionComplete || !r.OutputCollected || r.Answer == "" || r.SourceRecall == nil || r.ConceptHitRate == nil {
			return bad()
		}
	} else if r.SourceRecall != nil || r.ConceptHitRate != nil || r.CitationReferenceValidity != nil {
		return bad()
	}
	seen := map[string]bool{}
	for _, e := range r.Evidence {
		if e.ID == "" || seen[e.ID] || e.TextArtifactID == "" || !validBenchmarkHash(e.ContentSHA256) || e.URL == "" && e.OriginalURL == "" {
			return bad()
		}
		seen[e.ID] = true
	}
	return nil
}

func benchmarkDelta(b, a *float64) any {
	if b == nil || a == nil {
		return nil
	}
	return *a - *b
}

func benchmarkIntDelta(b, a *int) any {
	if b == nil || a == nil {
		return nil
	}
	return *a - *b
}

func benchmarkFinishedReport(r labBenchmarkResult) bool {
	return r.CollectionComplete && r.Status.Terminal() && r.ReportArtifactID != "" && r.MarkdownArtifactID != "" && r.Sections != nil && *r.Sections > 0 && r.OutputCollected && r.OutputBytes > 0
}

func benchmarkSuccessfulOutcome(r labBenchmarkResult) bool {
	return benchmarkFinishedReport(r) && r.Status == domain.StatusCompleted && r.Outcome == "completed" && r.ReportOutcome == "completed"
}

func benchmarkComparisonState(r labBenchmarkResult) map[string]any {
	return map[string]any{"status": r.Status, "outcome": r.Outcome, "assistant_message_status": r.AssistantMessageStatus, "collection_complete": r.CollectionComplete, "scoring_complete": r.ScoringComplete, "finished_report": benchmarkFinishedReport(r), "completed_status": r.Status == domain.StatusCompleted, "successful_run_outcome": benchmarkSuccessfulOutcome(r), "report_outcome": r.ReportOutcome, "error_code": r.ErrorCode, "stop_reason": r.StopReason, "pause_reason": r.PauseReason, "resource_reason": r.ResourceReason, "run_id": r.RunID}
}

func benchmarkSummaries(base, adaptive map[string]labBenchmarkResult) map[string]any {
	out := map[string]any{}
	for variant, rows := range map[string]map[string]labBenchmarkResult{"baseline": base, "adaptive": adaptive} {
		splits := map[string]any{}
		for _, split := range []string{"all", "development", "heldout"} {
			n, collected, outputs, scored, reports, completed, paused, failed, cancelled := 0, 0, 0, 0, 0, 0, 0, 0, 0
			successful := 0
			var recalls, concepts, citations float64
			citationN := 0
			outcomes := map[string]int{}
			for _, r := range rows {
				if split != "all" && r.Split != split {
					continue
				}
				n++
				outcomes[r.Outcome]++
				if r.CollectionComplete {
					collected++
				}
				if r.OutputCollected && r.OutputBytes > 0 {
					outputs++
				}
				if r.ScoringComplete {
					scored++
					recalls += *r.SourceRecall
					concepts += *r.ConceptHitRate
					if r.CitationReferenceValidity != nil {
						citations += *r.CitationReferenceValidity
						citationN++
					}
				}
				if benchmarkFinishedReport(r) {
					reports++
				}
				if benchmarkSuccessfulOutcome(r) {
					successful++
				}
				switch r.Status {
				case domain.StatusCompleted:
					completed++
				case domain.StatusPaused:
					paused++
				case domain.StatusFailed:
					failed++
				case domain.StatusCancelled:
					cancelled++
				}
			}
			mean := func(sum float64, count int) any {
				if count == 0 {
					return nil
				}
				return sum / float64(count)
			}
			splits[split] = map[string]any{"planned_cases": n, "collection_complete": collected, "collection_complete_rate": float64(collected) / float64(n), "nonempty_collected_outputs": outputs, "scored_outputs": scored, "finished_reports": reports, "finished_report_rate": float64(reports) / float64(n), "completed_status": completed, "completed_status_rate": float64(completed) / float64(n), "successful_run_outcomes": successful, "successful_run_outcome_rate": float64(successful) / float64(n), "paused": paused, "failed": failed, "cancelled": cancelled, "outcomes": outcomes, "mean_expected_source_recall": mean(recalls, scored), "mean_lexical_concept_hit_rate": mean(concepts, scored), "mean_citation_reference_validity": mean(citations, citationN), "citation_scored_outputs": citationN, "independent_factual_accuracy": nil}
		}
		out[variant] = splits
	}
	return out
}
