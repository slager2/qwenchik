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
	Version                    int                      `json:"version"`
	ProtocolSHA256             string                   `json:"protocol_sha256"`
	ExperimentID               string                   `json:"experiment_id"`
	StartedAt                  time.Time                `json:"started_at"`
	FinishedAt                 time.Time                `json:"finished_at"`
	ConversationID             string                   `json:"conversation_id"`
	CollectionComplete         bool                     `json:"collection_complete"`
	OutputCollected            bool                     `json:"output_collected"`
	Answer                     string                   `json:"answer"`
	AssistantMessageStatus     string                   `json:"assistant_message_status"`
	OutputSHA256               string                   `json:"output_sha256"`
	Evidence                   []labBenchmarkEvidence   `json:"evidence"`
	ContextBudget              *domain.ContextBudget    `json:"context_budget"`
	Budget                     *domain.ResearchBudget   `json:"budget"`
	StopReason                 string                   `json:"stop_reason"`
	PauseReason                string                   `json:"pause_reason"`
	ResourceReason             string                   `json:"resource_reason"`
	Compactions                *int                     `json:"compactions"`
	ProbeAttempts              *int                     `json:"resource_probe_attempts"`
	AppRSS                     *int64                   `json:"sampled_peak_app_rss_bytes"`
	ModelRSS                   *int64                   `json:"sampled_peak_model_rss_bytes"`
	ResourceSamples            int                      `json:"fresh_resource_samples"`
	ResourceMaxAgeSeconds      int                      `json:"resource_sample_max_age_seconds"`
	ReportArtifactID           string                   `json:"report_artifact_id"`
	MarkdownArtifactID         string                   `json:"markdown_artifact_id"`
	ReportSHA256               string                   `json:"report_artifact_sha256,omitempty"`
	ReportOutcome              string                   `json:"report_outcome"`
	Errors                     []string                 `json:"errors"`
	ScoringComplete            bool                     `json:"scoring_complete"`
	Type                       string                   `json:"type"`
	CaseID                     string                   `json:"case_id"`
	Split                      string                   `json:"split"`
	Variant                    string                   `json:"variant"`
	DatasetSHA256              string                   `json:"dataset_sha256"`
	CorpusSHA256               string                   `json:"corpus_sha256"`
	ModelProfileHash           string                   `json:"model_profile_hash"`
	BudgetHash                 string                   `json:"budget_hash"`
	RunID                      string                   `json:"run_id"`
	Status                     domain.TaskStatus        `json:"status"`
	Outcome                    string                   `json:"outcome"`
	WallSeconds                float64                  `json:"wall_seconds"`
	Counters                   *domain.ResearchCounters `json:"counters"`
	SampledPeakRSS             *int64                   `json:"sampled_peak_rss_bytes"`
	OutputBytes                int                      `json:"output_bytes"`
	SourceRecall               *float64                 `json:"expected_source_recall"`
	ConceptHitRate             *float64                 `json:"lexical_concept_hit_rate"`
	CitationReferenceValidity  *float64                 `json:"citation_reference_validity"`
	IndependentFactualAccuracy *float64                 `json:"independent_factual_accuracy"`
	TotalOutputTokens          *int64                   `json:"total_output_tokens"`
	Sections                   *int                     `json:"sections"`
	UnrepresentedCriteria      *int                     `json:"unrepresented_criteria"`
	ErrorCode                  string                   `json:"error_code,omitempty"`
	lastResourceSample         time.Time
}

var benchmarkCitation = regexp.MustCompile(`\[([A-Za-z0-9_-]{1,128})\]`)

func scoreLabCase(c labBenchmarkCase, message string, evidence []domain.Evidence, runID string) labBenchmarkResult {
	r := labBenchmarkResult{Type: "lab_benchmark_case", CaseID: c.ID, Split: c.Split, OutputBytes: len(message)}
	recall, concepts := 0., 0.
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
			recall++
		}
	}
	recall /= float64(len(c.SourceURLs))
	r.SourceRecall = &recall
	for _, concept := range c.Concepts {
		for _, alternative := range strings.Split(concept, "|") {
			if strings.Contains(strings.ToLower(message), strings.ToLower(alternative)) {
				concepts++
				break
			}
		}
	}
	concepts /= float64(len(c.Concepts))
	r.ConceptHitRate = &concepts
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
	var variant, split, caseID, protocol, outputPath string
	var caseTimeout time.Duration
	var printCorpus, stopOnPause bool
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
		if !validBenchmarkHash(protocol) {
			return errors.New("--protocol-sha256 must be the lowercase SHA256 of the frozen experiment manifest")
		}
		cases, err := labCases()
		if err != nil {
			return err
		}
		if caseID != "" {
			found := false
			for _, c := range cases {
				found = found || c.ID == caseID && (split == "all" || split == c.Split)
			}
			if !found {
				return errors.New("--case must be an exact manifest ID within --split")
			}
		}
		var receipt *os.File
		if outputPath != "" {
			receipt, err = os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			defer receipt.Close()
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		client := newLabClient(cfg)
		var doctor struct {
			Hold   bool `json:"hold_resource_pauses"`
			Limits struct {
				Mode     string `json:"privacy_mode"`
				Hash     string `json:"offline_corpus_sha256"`
				Adaptive bool   `json:"adaptive_enabled"`
				Report   bool   `json:"sectioned_report_enabled"`
			} `json:"research_limits"`
		}
		if err := client.request(cmd.Context(), "GET", "/api/lab/policy", nil, &doctor); err != nil {
			return err
		}
		if doctor.Limits.Mode != domain.PrivacyOffline || doctor.Limits.Hash != labHash(labCorpusJSON) {
			return errors.New("benchmark requires the running server in offline mode with the exact --print-corpus bytes")
		}
		if doctor.Limits.Adaptive != (variant == "adaptive") || !doctor.Limits.Report {
			return errors.New("running server must match --variant and enable sectioned reports before submitting benchmark work")
		}
		if stopOnPause && !doctor.Hold {
			return errors.New("--stop-on-pause requires server hold_resource_pauses=true")
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		for _, c := range cases {
			if split != "all" && c.Split != split || caseID != "" && c.ID != caseID {
				continue
			}
			result, err := runLabCase(cmd.Context(), client, c, variant, caseTimeout, stopOnPause)
			result.Version, result.ProtocolSHA256, result.ExperimentID = 2, protocol, protocol
			if receipt != nil {
				if writeErr := json.NewEncoder(receipt).Encode(result); writeErr != nil {
					return errors.Join(err, writeErr)
				}
				if syncErr := receipt.Sync(); syncErr != nil {
					return errors.Join(err, syncErr)
				}
			}
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
	cmd.Flags().StringVar(&caseID, "case", "", "one exact manifest case ID (default all selected cases)")
	cmd.Flags().StringVar(&protocol, "protocol-sha256", "", "required SHA256 of the external frozen experiment manifest; also the experiment identity")
	cmd.Flags().StringVar(&outputPath, "output", "", "exclusive-create JSONL receipt file, also emits JSONL to stdout")
	cmd.Flags().BoolVar(&stopOnPause, "stop-on-pause", false, "retain paused checkpoints and stop with a nonzero exit; requires server resource-pause hold")
	return cmd
}

func runLabCase(parent context.Context, client *labClient, c labBenchmarkCase, variant string, timeout time.Duration, stopOnPause ...bool) (result labBenchmarkResult, resultErr error) {
	result = labBenchmarkResult{Type: "lab_benchmark_case", CaseID: c.ID, Split: c.Split, Variant: variant, DatasetSHA256: labHash(labCasesJSON), CorpusSHA256: labHash(labCorpusJSON), ResourceMaxAgeSeconds: benchmarkResourceMaxAgeSeconds}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result.StartedAt = time.Now().UTC()
	defer func() {
		result.FinishedAt = time.Now().UTC()
		result.WallSeconds = result.FinishedAt.Sub(result.StartedAt).Seconds()
		if resultErr != nil && result.ErrorCode == "" {
			result.ErrorCode = "benchmark_incomplete"
		}
		if resultErr != nil {
			result.Errors = append(result.Errors, resultErr.Error())
		}
	}()
	var conversation domain.Conversation
	if err := client.request(ctx, "POST", "/api/conversations", map[string]any{"title": "Lab " + c.ID, "mode": "research", "research_settings": map[string]any{"profile": "standard"}}, &conversation); err != nil {
		return result, err
	}
	result.ConversationID = conversation.ID
	retained := false
	defer func() {
		if retained {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if len(stopOnPause) > 0 && stopOnPause[0] {
			if result.RunID == "" {
				// The response may have failed after commit. Stop any work in the
				// owned conversation rather than leaving an unidentified run active.
				if err := client.request(cleanup, "DELETE", "/api/conversations/"+url.PathEscape(conversation.ID), nil, nil); err != nil {
					if result.ErrorCode == "" {
						result.ErrorCode = "context_cleanup_unconfirmed"
					}
					resultErr = errors.Join(resultErr, errors.New("ambiguous submission cleanup unconfirmed; conversation retained"))
				}
				return
			}
			if err := client.request(cleanup, "POST", "/api/tasks/"+url.PathEscape(result.RunID)+"/pause", nil, nil); err != nil {
				if result.ErrorCode == "" {
					result.ErrorCode = "context_cleanup_unconfirmed"
				}
				resultErr = errors.Join(resultErr, errors.New("could not confirm diagnostic pause; checkpoint retained"))
			}
			return
		}
		var err error
		if result.RunID != "" {
			err = client.request(cleanup, "POST", "/api/runs/"+url.PathEscape(result.RunID)+"/cancel", nil, nil)
		}
		if result.RunID == "" || err != nil {
			err = client.request(cleanup, "DELETE", "/api/conversations/"+url.PathEscape(conversation.ID), nil, nil)
		}
		if err != nil {
			if result.ErrorCode == "" {
				result.ErrorCode = "benchmark_cleanup_failed"
			}
			resultErr = errors.Join(resultErr, errors.New("could not confirm cancellation of benchmark-owned work"))
		}
	}()
	var submission domain.SubmitResponse
	if err := client.request(ctx, "POST", "/api/conversations/"+url.PathEscape(conversation.ID)+"/messages", map[string]any{"content": c.Goal, "mode": "research"}, &submission); err != nil {
		return result, err
	}
	result.RunID = submission.Run.ID
	// Collection uses a separate bounded context even after the observation deadline.
	// Registered after cleanup so collection runs first, before cancellation changes state.
	defer func() {
		if result.Status == "" || result.ErrorCode == "benchmark_policy_mismatch" || result.ErrorCode == "benchmark_snapshot_changed" {
			return
		}
		collectCtx, collectCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer collectCancel()
		if err := collectBenchmarkOutput(collectCtx, client, c, submission.Run.AssistantMessageID, &result); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		var observation labBenchmarkRuntime
		err := client.request(ctx, "GET", "/api/runs/"+url.PathEscape(result.RunID)+"/runtime", nil, &observation)
		if err != nil {
			result.ErrorCode = "runtime_unavailable"
			if errors.Is(err, errBenchmarkRuntimeInvalid) {
				result.ErrorCode = "benchmark_runtime_invalid"
			} else if ctx.Err() != nil {
				result.ErrorCode = "benchmark_timeout"
			}
			return result, err
		}
		r := observation.labRuntime
		if r.RunID != result.RunID {
			result.ErrorCode = "runtime_identity_mismatch"
			return result, errors.New("laboratory runtime run ID mismatch")
		}
		// Preserve every successfully observed state before policy/identity validation.
		oldModel, oldBudget := result.ModelProfileHash, result.BudgetHash
		observeBenchmarkRuntime(&result, observation)
		if r.ResearchSessionID == "" || r.PrivacyMode != domain.PrivacyOffline || r.CorpusSHA256 != result.CorpusSHA256 || r.AdaptiveEnabled != (variant == "adaptive") || !r.SectionedReportEnabled || r.ModelProfileHash == "" {
			result.ErrorCode = "benchmark_policy_mismatch"
			return result, errors.New("saved research policy does not match benchmark variant/corpus/report configuration")
		}
		if oldModel != "" && (oldModel != result.ModelProfileHash || oldBudget != result.BudgetHash) {
			result.ErrorCode = "benchmark_snapshot_changed"
			return result, errors.New("benchmark model/budget snapshot changed")
		}
		if r.Status.Terminal() {
			retained = true
			return result, nil
		}
		if len(stopOnPause) > 0 && stopOnPause[0] && r.Status == domain.StatusPaused {
			retained = true
			result.ErrorCode = "benchmark_paused"
			return result, errors.New("benchmark paused; checkpoint retained")
		} // preserve the measured checkpoint, do not cancel/retry it.
		select {
		case <-ctx.Done():
			result.ErrorCode = "benchmark_timeout"
			return result, ctx.Err()
		case <-ticker.C:
		}
	}
}

func readLabResults(path string) (map[string]labBenchmarkResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return nil, errors.New("benchmark results must be a regular file <=64 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return nil, errors.New("benchmark result must be a regular file <=64 MiB")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, (64<<20)+1))
	scanner.Buffer(make([]byte, 4096), 4<<20)
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
	cases, err := labCases()
	if err != nil {
		return nil, err
	}
	if len(base) != len(cases) || len(adaptive) != len(cases) {
		return nil, errors.New("comparison requires all 12 manifest cases in each variant")
	}
	identity := base[cases[0].ID]
	seenRuns := map[string]bool{}
	deltas := []map[string]any{}
	for _, c := range cases {
		b, a := base[c.ID], adaptive[c.ID]
		for variant, r := range map[string]labBenchmarkResult{"baseline": b, "adaptive": a} {
			if err := validateBenchmarkResult(r); err != nil {
				return nil, err
			}
			if r.CaseID != c.ID || r.Split != c.Split || r.Variant != variant || r.DatasetSHA256 != labHash(labCasesJSON) || r.CorpusSHA256 != labHash(labCorpusJSON) || r.ModelProfileHash != identity.ModelProfileHash || r.BudgetHash != identity.BudgetHash || r.ProtocolSHA256 != identity.ProtocolSHA256 || r.ExperimentID != identity.ExperimentID || seenRuns[r.RunID] {
				return nil, errors.New("incomparable benchmark identity, split, snapshots or duplicate run IDs")
			}
			seenRuns[r.RunID] = true
		}
		deltas = append(deltas, map[string]any{"case_id": c.ID, "split": c.Split, "baseline": benchmarkComparisonState(b), "adaptive": benchmarkComparisonState(a), "source_recall_delta": benchmarkDelta(b.SourceRecall, a.SourceRecall), "lexical_concept_hit_rate_delta": benchmarkDelta(b.ConceptHitRate, a.ConceptHitRate), "citation_reference_validity_delta": benchmarkDelta(b.CitationReferenceValidity, a.CitationReferenceValidity), "sections_delta": benchmarkIntDelta(b.Sections, a.Sections), "unrepresented_criteria_delta": benchmarkIntDelta(b.UnrepresentedCriteria, a.UnrepresentedCriteria), "wall_seconds_delta": a.WallSeconds - b.WallSeconds, "llm_reservations_delta": a.Counters.LLMCalls - b.Counters.LLMCalls, "independent_factual_accuracy": nil})
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
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"type": "lab_benchmark_comparison", "version": 2, "protocol_sha256": b["dev-fasta"].ProtocolSHA256, "planned_cases_per_variant": 12, "cases": deltas, "summaries": benchmarkSummaries(b, a), "interpretation": "Complete case accounting is not benchmark success. Paused, failed and incomplete measurements remain explicit. Lexical/source/citation ownership proxies are not independent factual accuracy; quality proxies summarize only collected nonempty outputs."})
	}}
}
