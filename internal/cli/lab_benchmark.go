package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/spf13/cobra"
)

//go:embed testdata/lab-cases.json
var labCasesJSON []byte

//go:embed testdata/lab-corpus.json
var labCorpusJSON []byte

type labBenchmarkCase struct {
	ID         string   `json:"id"`
	Split      string   `json:"split"`
	Goal       string   `json:"goal"`
	Concepts   []string `json:"concepts"`
	SourceURLs []string `json:"source_urls"`
}

func labHash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func labCases() ([]labBenchmarkCase, error) {
	var data struct {
		Version   int                `json:"version"`
		Synthetic bool               `json:"synthetic"`
		Cases     []labBenchmarkCase `json:"cases"`
	}
	if err := json.Unmarshal(labCasesJSON, &data); err != nil {
		return nil, err
	}
	if data.Version != 1 || !data.Synthetic || len(data.Cases) < 10 || len(data.Cases) > 20 {
		return nil, errors.New("invalid laboratory benchmark manifest")
	}
	seen := map[string]bool{}
	heldout := 0
	for _, c := range data.Cases {
		if seen[c.ID] || c.ID == "" || len(c.Goal) > 20000 || len(c.Concepts) == 0 || len(c.SourceURLs) < 2 {
			return nil, errors.New("invalid laboratory case")
		}
		seen[c.ID] = true
		if c.Split == "heldout" {
			heldout++
		} else if c.Split != "development" {
			return nil, errors.New("unknown benchmark split")
		}
	}
	if heldout < 3 {
		return nil, errors.New("benchmark requires a fixed held-out split")
	}
	return data.Cases, nil
}

type labBenchmarkResult struct {
	ScoringComplete            bool                    `json:"scoring_complete"`
	Type                       string                  `json:"type"`
	CaseID                     string                  `json:"case_id"`
	Split                      string                  `json:"split"`
	Variant                    string                  `json:"variant"`
	DatasetSHA256              string                  `json:"dataset_sha256"`
	CorpusSHA256               string                  `json:"corpus_sha256"`
	ModelProfileHash           string                  `json:"model_profile_hash"`
	BudgetHash                 string                  `json:"budget_hash"`
	RunID                      string                  `json:"run_id"`
	Status                     domain.TaskStatus       `json:"status"`
	Outcome                    string                  `json:"outcome"`
	WallSeconds                float64                 `json:"wall_seconds"`
	Counters                   domain.ResearchCounters `json:"counters"`
	SampledPeakRSS             *int64                  `json:"sampled_peak_rss_bytes"`
	OutputBytes                int                     `json:"output_bytes"`
	SourceRecall               float64                 `json:"expected_source_recall"`
	ConceptHitRate             float64                 `json:"lexical_concept_hit_rate"`
	CitationReferenceValidity  *float64                `json:"citation_reference_validity"`
	IndependentFactualAccuracy *float64                `json:"independent_factual_accuracy"`
	TotalOutputTokens          *int64                  `json:"total_output_tokens"`
	Sections                   int                     `json:"sections"`
	UnrepresentedCriteria      int                     `json:"unrepresented_criteria"`
	ErrorCode                  string                  `json:"error_code,omitempty"`
}

var benchmarkCitation = regexp.MustCompile(`\[([A-Za-z0-9_-]{1,128})\]`)

func scoreLabCase(c labBenchmarkCase, message string, evidence []domain.Evidence, runID string) labBenchmarkResult {
	r := labBenchmarkResult{Type: "lab_benchmark_case", CaseID: c.ID, Split: c.Split, OutputBytes: len(message)}
	validIDs := map[string]bool{}
	urls := map[string]bool{}
	for _, e := range evidence {
		if e.TaskID == runID && e.ID != "" && e.ContentSHA256 != "" && e.TextArtifactID != "" {
			validIDs[e.ID] = true
			urls[e.CanonicalURL] = true
			urls[e.OriginalURL] = true
		}
	}
	for _, u := range c.SourceURLs {
		if urls[u] {
			r.SourceRecall++
		}
	}
	r.SourceRecall /= float64(len(c.SourceURLs))
	for _, concept := range c.Concepts {
		for _, alternative := range strings.Split(concept, "|") {
			if strings.Contains(strings.ToLower(message), strings.ToLower(alternative)) {
				r.ConceptHitRate++
				break
			}
		}
	}
	r.ConceptHitRate /= float64(len(c.Concepts))
	markers := benchmarkCitation.FindAllStringSubmatch(message, -1)
	if len(markers) > 0 {
		supported := 0.
		for _, m := range markers {
			if validIDs[m[1]] {
				supported++
			}
		}
		value := supported / float64(len(markers))
		r.CitationReferenceValidity = &value
	}
	return r
}

func newLabBenchmarkCommand() *cobra.Command {
	var variant, split string
	var caseTimeout time.Duration
	var printCorpus bool
	cmd := &cobra.Command{Use: "benchmark", Short: "Run fixed offline development/held-out cases through the existing local worker; emits JSONL", RunE: func(cmd *cobra.Command, _ []string) error {
		if printCorpus {
			_, err := cmd.OutOrStdout().Write(labCorpusJSON)
			return err
		}
		if variant != "baseline" && variant != "adaptive" {
			return errors.New("--variant must be baseline or adaptive")
		}
		if split != "all" && split != "development" && split != "heldout" {
			return errors.New("--split must be all, development or heldout")
		}
		if caseTimeout < time.Minute || caseTimeout > time.Hour {
			return errors.New("--case-timeout must be 1m..1h")
		}
		cases, err := labCases()
		if err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		client := newLabClient(cfg)
		var doctor struct {
			Limits struct {
				Mode     string `json:"privacy_mode"`
				Hash     string `json:"offline_corpus_sha256"`
				Adaptive bool   `json:"adaptive_enabled"`
				Report   bool   `json:"sectioned_report_enabled"`
			} `json:"research_limits"`
		}
		if err := client.request(cmd.Context(), "GET", "/api/doctor", nil, &doctor); err != nil {
			return err
		}
		if doctor.Limits.Mode != domain.PrivacyOffline || doctor.Limits.Hash != labHash(labCorpusJSON) {
			return errors.New("benchmark requires the running server in offline mode with the exact --print-corpus bytes")
		}
		if doctor.Limits.Adaptive != (variant == "adaptive") || !doctor.Limits.Report {
			return errors.New("running server must match --variant and enable sectioned reports before submitting benchmark work")
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		for _, c := range cases {
			if split != "all" && c.Split != split {
				continue
			}
			result, err := runLabCase(cmd.Context(), client, c, variant, caseTimeout)
			if encodeErr := encoder.Encode(result); encodeErr != nil {
				return encodeErr
			}
			if err != nil {
				return err
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&variant, "variant", "baseline", "baseline or adaptive; must match the running server's saved policy")
	cmd.Flags().StringVar(&split, "split", "all", "all, development, or heldout (fixed split; no training)")
	cmd.Flags().DurationVar(&caseTimeout, "case-timeout", 20*time.Minute, "finite timeout for each case, 1m..1h")
	cmd.Flags().BoolVar(&printCorpus, "print-corpus", false, "print the exact synthetic import corpus without submitting work")
	return cmd
}

func runLabCase(parent context.Context, client *labClient, c labBenchmarkCase, variant string, timeout time.Duration) (result labBenchmarkResult, resultErr error) {
	result = labBenchmarkResult{Type: "lab_benchmark_case", CaseID: c.ID, Split: c.Split, Variant: variant, DatasetSHA256: labHash(labCasesJSON), CorpusSHA256: labHash(labCorpusJSON)}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	start := time.Now()
	defer func() {
		result.WallSeconds = time.Since(start).Seconds()
		if resultErr != nil && result.ErrorCode == "" {
			result.ErrorCode = "benchmark_incomplete"
		}
	}()
	var conversation domain.Conversation
	if err := client.request(ctx, "POST", "/api/conversations", map[string]any{"title": "Lab " + c.ID, "mode": "research", "research_settings": map[string]any{"profile": "standard"}}, &conversation); err != nil {
		return result, err
	}
	terminal := false
	defer func() {
		if terminal {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		if result.RunID != "" {
			err = client.request(cleanup, "POST", "/api/runs/"+url.PathEscape(result.RunID)+"/cancel", nil, nil)
		}
		if result.RunID == "" || err != nil {
			err = client.request(cleanup, "DELETE", "/api/conversations/"+url.PathEscape(conversation.ID), nil, nil)
		}
		if err != nil {
			result.ErrorCode = "benchmark_cleanup_failed"
			resultErr = errors.Join(resultErr, errors.New("could not confirm cancellation of benchmark-owned work"))
		}
	}()
	var submission domain.SubmitResponse
	if err := client.request(ctx, "POST", "/api/conversations/"+url.PathEscape(conversation.ID)+"/messages", map[string]any{"content": c.Goal, "mode": "research"}, &submission); err != nil {
		return result, err
	}
	result.RunID = submission.Run.ID
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var latest labRuntime
	for {
		r, err := client.runtime(ctx, result.RunID)
		if err != nil {
			result.ErrorCode = "runtime_unavailable"
			return result, err
		}
		latest = r
		if r.ResearchSessionID == "" || r.PrivacyMode != domain.PrivacyOffline || r.CorpusSHA256 != result.CorpusSHA256 || r.AdaptiveEnabled != (variant == "adaptive") || !r.SectionedReportEnabled || r.ModelProfileHash == "" {
			result.ErrorCode = "benchmark_policy_mismatch"
			return result, errors.New("saved research policy does not match benchmark variant/corpus/report configuration")
		}
		budgetRaw, _ := json.Marshal(r.Budget)
		budgetHash := labHash(budgetRaw)
		if result.ModelProfileHash != "" && (result.ModelProfileHash != r.ModelProfileHash || result.BudgetHash != budgetHash) {
			result.ErrorCode = "benchmark_snapshot_changed"
			return result, errors.New("benchmark model/budget snapshot changed")
		}
		result.ModelProfileHash = r.ModelProfileHash
		result.BudgetHash = budgetHash
		if rss := r.Runtime.Resources.RSSBytes; rss != nil && (result.SampledPeakRSS == nil || *rss > *result.SampledPeakRSS) {
			value := *rss
			result.SampledPeakRSS = &value
		}
		if r.Status.Terminal() {
			terminal = true
			break
		}
		select {
		case <-ctx.Done():
			result.ErrorCode = "benchmark_timeout"
			return result, ctx.Err()
		case <-ticker.C:
		}
	}
	result.Status = latest.Status
	result.Outcome = latest.Outcome
	result.Counters = latest.Counters
	result.WallSeconds = time.Since(start).Seconds()
	var messages []domain.ConversationMessage
	if err := client.request(ctx, "GET", "/api/conversations/"+url.PathEscape(conversation.ID)+"/messages", nil, &messages); err != nil {
		return result, err
	}
	answer := ""
	found := false
	for _, m := range messages {
		if m.ID == submission.Run.AssistantMessageID && m.RunID == result.RunID && m.Role == "assistant" {
			answer = m.Content
			found = true
		}
	}
	if !found {
		return result, errors.New("benchmark assistant message is absent from the owned run")
	}
	var evidence []domain.Evidence
	if err := client.request(ctx, "GET", "/api/runs/"+url.PathEscape(result.RunID)+"/evidence", nil, &evidence); err != nil {
		return result, err
	}
	scores := scoreLabCase(c, answer, evidence, result.RunID)
	result.OutputBytes = scores.OutputBytes
	result.SourceRecall = scores.SourceRecall
	result.ConceptHitRate = scores.ConceptHitRate
	result.CitationReferenceValidity = scores.CitationReferenceValidity
	if latest.Report != nil {
		result.Sections = latest.Report.Sections
		result.UnrepresentedCriteria = len(latest.Report.UnrepresentedCriteria)
	}
	result.ScoringComplete = true
	return result, nil
}

func readLabResults(path string) (map[string]labBenchmarkResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, errors.New("benchmark results must be a regular file <=4 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, errors.New("benchmark result must be a regular file <=4 MiB")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, (4<<20)+1))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	rows := map[string]labBenchmarkResult{}
	for scanner.Scan() {
		var row labBenchmarkResult
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if row.Type != "lab_benchmark_case" || row.CaseID == "" || rows[row.CaseID].CaseID != "" {
			return nil, errors.New("unexpected/duplicate benchmark row")
		}
		rows[row.CaseID] = row
		if len(rows) > 20 {
			return nil, errors.New("too many benchmark rows")
		}
	}
	return rows, scanner.Err()
}
func compareLabResults(base, adaptive map[string]labBenchmarkResult) ([]map[string]any, error) {
	if len(base) == 0 || len(base) != len(adaptive) {
		return nil, errors.New("benchmark case sets differ")
	}
	cases, err := labCases()
	if err != nil {
		return nil, err
	}
	deltas := []map[string]any{}
	for _, c := range cases {
		b, ok := base[c.ID]
		if !ok {
			continue
		}
		a, ok := adaptive[c.ID]
		if !ok || b.Variant != "baseline" || a.Variant != "adaptive" || b.DatasetSHA256 != labHash(labCasesJSON) || a.DatasetSHA256 != b.DatasetSHA256 || b.CorpusSHA256 != labHash(labCorpusJSON) || a.CorpusSHA256 != b.CorpusSHA256 || b.ModelProfileHash == "" || a.ModelProfileHash != b.ModelProfileHash || b.BudgetHash == "" || a.BudgetHash != b.BudgetHash || b.ErrorCode != "" || a.ErrorCode != "" || !b.ScoringComplete || !a.ScoringComplete || !b.Status.Terminal() || !a.Status.Terminal() {
			return nil, errors.New("incomparable benchmark snapshots or incomplete rows")
		}
		deltas = append(deltas, map[string]any{"case_id": c.ID, "split": c.Split, "baseline_status": b.Status, "adaptive_status": a.Status, "source_recall_delta": a.SourceRecall - b.SourceRecall, "lexical_concept_hit_rate_delta": a.ConceptHitRate - b.ConceptHitRate, "wall_seconds_delta": a.WallSeconds - b.WallSeconds, "llm_calls_delta": a.Counters.LLMCalls - b.Counters.LLMCalls, "independent_factual_accuracy": nil})
	}
	if len(deltas) != len(base) {
		return nil, errors.New("unknown benchmark case IDs")
	}
	return deltas, nil
}
func newLabCompareCommand() *cobra.Command {
	return &cobra.Command{Use: "compare BASELINE.jsonl ADAPTIVE.jsonl", Short: "Compare matching fixed offline benchmark snapshots", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		b, err := readLabResults(args[0])
		if err != nil {
			return err
		}
		a, err := readLabResults(args[1])
		if err != nil {
			return err
		}
		deltas, err := compareLabResults(b, a)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"type": "lab_benchmark_comparison", "cases": deltas, "interpretation": "Lexical/source/ownership proxies; independent factual quality requires human review. Held-out cases are reported separately."})
	}}
}
