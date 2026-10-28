package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

const benchmarkAnswerLimit = 512 << 10
const benchmarkResourceMaxAgeSeconds = 5

var errBenchmarkRuntimeInvalid = errors.New("benchmark runtime projection invalid")

var benchmarkCounterKeys = []string{"llm_calls", "http_attempts", "search_queries", "fetches", "fetched_bytes", "evidence", "questions_created", "questions_accepted", "rounds", "stagnant_rounds"}
var benchmarkBudgetKeys = []string{"wall_time_seconds", "max_rounds", "max_steps", "max_search_queries", "max_fetches", "max_llm_calls", "max_evidence", "max_questions_created", "max_questions_accepted", "max_stagnant_rounds", "max_citation_sources", "max_citation_claims"}

func benchmarkRequiredNumbers(raw json.RawMessage, required []string, positive bool) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	for _, key := range required {
		if value, ok := values[key]; !ok || string(value) == "null" {
			return fmt.Errorf("missing/null numeric field %s", key)
		}
	}
	for key, value := range values {
		var n int64
		if string(value) == "null" {
			return fmt.Errorf("null numeric field %s", key)
		}
		if err := json.Unmarshal(value, &n); err != nil {
			return fmt.Errorf("invalid numeric field %s: %w", key, err)
		}
		if n < 0 || positive && n == 0 {
			return fmt.Errorf("invalid numeric range for %s", key)
		}
	}
	return nil
}

func validBenchmarkBudget(b *domain.ResearchBudget) bool {
	if b == nil || b.WallTimeSeconds < 60 || b.MaxRounds > 10 {
		return false
	}
	raw, err := json.Marshal(b)
	return err == nil && benchmarkRequiredNumbers(raw, benchmarkBudgetKeys, true) == nil
}

type labBenchmarkEvidence struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	OriginalURL    string `json:"original_url"`
	ContentSHA256  string `json:"content_sha256"`
	TextArtifactID string `json:"text_artifact_id"`
}

// Extend the existing runtime projection locally; context-check keeps its API.
type labBenchmarkRuntime struct {
	labRuntime
	StopReason    string `json:"research_stop_reason"`
	ProbeAttempts int    `json:"resource_probe_attempts"`
	Report        *struct {
		ArtifactID            string          `json:"artifact_id"`
		ArtifactSHA256        string          `json:"artifact_sha256"`
		MarkdownArtifactID    string          `json:"markdown_artifact_id"`
		Outcome               string          `json:"outcome"`
		Sections              *int            `json:"sections"`
		UnrepresentedCriteria json.RawMessage `json:"unrepresented_criteria"`
	} `json:"research_report"`
}

// Validate presence and numeric types before decoding into zero-valued structs.
// The core budget keys exist in both saved v1 and v2 sessions; v2-only keys are
// optional here so context-check can still measure a supported legacy session.
func (r *labBenchmarkRuntime) UnmarshalJSON(raw []byte) error {
	decode := func() error {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for _, key := range []string{"budget", "counters", "compactions", "resource_probe_attempts"} {
			if value, ok := fields[key]; !ok || string(value) == "null" {
				return fmt.Errorf("missing/null runtime field %s", key)
			}
		}
		if err := benchmarkRequiredNumbers(fields["budget"], benchmarkBudgetKeys, true); err != nil {
			return fmt.Errorf("budget: %w", err)
		}
		if err := benchmarkRequiredNumbers(fields["counters"], benchmarkCounterKeys, false); err != nil {
			return fmt.Errorf("counters: %w", err)
		}
		type plain labBenchmarkRuntime
		var decoded plain
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		if !validBenchmarkBudget(&decoded.Budget) || decoded.Compactions < 0 || decoded.ProbeAttempts < 0 {
			return errors.New("invalid runtime budget/compaction/probe range")
		}
		*r = labBenchmarkRuntime(decoded)
		return nil
	}
	if err := decode(); err != nil {
		return fmt.Errorf("%w: %w", errBenchmarkRuntimeInvalid, err)
	}
	return nil
}

func observeBenchmarkRuntime(result *labBenchmarkResult, r labBenchmarkRuntime) {
	result.Status, result.Outcome = r.Status, r.Outcome
	counters, compactions, attempts := r.Counters, r.Compactions, r.ProbeAttempts
	result.Counters, result.Compactions, result.ProbeAttempts = &counters, &compactions, &attempts
	result.PauseReason, result.ResourceReason = r.PauseReason, r.ResourceReason
	result.StopReason = r.StopReason
	result.ContextBudget = r.ContextBudget
	budget := r.Budget
	result.Budget = &budget
	raw, _ := json.Marshal(budget)
	result.BudgetHash, result.ModelProfileHash = labHash(raw), r.ModelProfileHash
	result.Sections, result.UnrepresentedCriteria = nil, nil
	result.ReportArtifactID, result.MarkdownArtifactID, result.ReportOutcome = "", "", ""
	result.ReportSHA256 = ""
	if r.Report != nil {
		result.ReportArtifactID, result.MarkdownArtifactID, result.ReportOutcome = r.Report.ArtifactID, r.Report.MarkdownArtifactID, r.Report.Outcome
		result.ReportSHA256 = r.Report.ArtifactSHA256
		if r.Report.ArtifactID != "" {
			result.Sections = r.Report.Sections
			var criteria []string
			if len(r.Report.UnrepresentedCriteria) > 0 && json.Unmarshal(r.Report.UnrepresentedCriteria, &criteria) == nil {
				n := len(criteria)
				result.UnrepresentedCriteria = &n
			}
		}
	}
	s := r.Runtime.Resources
	now := time.Now()
	result.ResourceMaxAgeSeconds = benchmarkResourceMaxAgeSeconds
	// This five-second collection rule is pinned by the benchmark protocol, not
	// inferred from an optional API field. Future-dated samples are not fresh.
	if !s.SnapshotAt.IsZero() && now.Sub(s.SnapshotAt) <= benchmarkResourceMaxAgeSeconds*time.Second && !s.SnapshotAt.After(now) && s.SnapshotAt.After(result.lastResourceSample) {
		result.lastResourceSample = s.SnapshotAt
		result.ResourceSamples++
		peak := func(dst **int64, value *int64) {
			if value != nil && *value >= 0 && (*dst == nil || *value > **dst) {
				v := *value
				*dst = &v
			}
		}
		peak(&result.AppRSS, s.RSSBytes)
		peak(&result.ModelRSS, s.ModelRSSBytes)
		result.SampledPeakRSS = result.AppRSS // legacy context-check receipt alias
	}
}

func collectBenchmarkOutput(ctx context.Context, client *labClient, c labBenchmarkCase, messageID string, result *labBenchmarkResult) error {
	var messages []domain.ConversationMessage
	if err := client.request(ctx, "GET", "/api/conversations/"+url.PathEscape(result.ConversationID)+"/messages", nil, &messages); err != nil {
		return err
	}
	found := false
	for _, m := range messages {
		if m.ID == messageID && m.RunID == result.RunID && m.Role == "assistant" {
			if found || len(m.Content) > benchmarkAnswerLimit {
				return errors.New("benchmark answer duplicate or exceeds 512 KiB receipt bound")
			}
			found = true
			result.Answer, result.OutputBytes, result.OutputSHA256 = m.Content, len(m.Content), labHash([]byte(m.Content))
			result.OutputCollected = true
			result.AssistantMessageStatus = m.Status
		}
	}
	if !found {
		return errors.New("benchmark assistant message is absent from the owned run")
	}
	var evidence []domain.Evidence
	if err := client.request(ctx, "GET", "/api/runs/"+url.PathEscape(result.RunID)+"/evidence", nil, &evidence); err != nil {
		return err
	}
	if len(evidence) > 128 {
		return errors.New("benchmark evidence exceeds receipt bound")
	}
	result.Evidence = []labBenchmarkEvidence{}
	for _, e := range evidence {
		if e.TaskID == result.RunID && e.ID != "" && e.ContentSHA256 != "" && e.TextArtifactID != "" {
			if len(e.ID) > 128 || len(e.TextArtifactID) > 128 || len(e.CanonicalURL) > 2048 || len(e.OriginalURL) > 2048 || !validBenchmarkHash(e.ContentSHA256) {
				return errors.New("invalid benchmark evidence provenance")
			}
			result.Evidence = append(result.Evidence, labBenchmarkEvidence{e.ID, e.CanonicalURL, e.OriginalURL, e.ContentSHA256, e.TextArtifactID})
		}
	}
	result.CollectionComplete = true
	// Empty placeholders are collected observations, not scored delivered answers.
	if result.Answer != "" {
		scores := scoreLabCase(c, result.Answer, evidence, result.RunID)
		result.SourceRecall, result.ConceptHitRate, result.CitationReferenceValidity = scores.SourceRecall, scores.ConceptHitRate, scores.CitationReferenceValidity
		result.ScoringComplete = true
	}
	return nil
}
