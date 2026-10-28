package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/resources"
)

func benchmarkRuntimeFixture(status domain.TaskStatus) map[string]any {
	budget, _ := domain.ResearchBudgetForProfile(domain.ResearchProfileStandard)
	return map[string]any{"task_id": "run", "status": status, "research_session_id": "session", "privacy_mode": domain.PrivacyOffline, "offline_corpus_sha256": labHash(labCorpusJSON), "sectioned_report_enabled": true, "model_profile_hash": labHash([]byte("model")), "budget": budget, "counters": domain.ResearchCounters{}, "compactions": 0, "resource_probe_attempts": 0}
}

func benchmarkComparisonFixture(t *testing.T) (map[string]labBenchmarkResult, map[string]labBenchmarkResult) {
	t.Helper()
	cases, err := labCases()
	if err != nil {
		t.Fatal(err)
	}
	base, adaptive := map[string]labBenchmarkResult{}, map[string]labBenchmarkResult{}
	budget, _ := domain.ResearchBudgetForProfile(domain.ResearchProfileStandard)
	raw, _ := json.Marshal(budget)
	for _, c := range cases {
		for variant, rows := range map[string]map[string]labBenchmarkResult{"baseline": base, "adaptive": adaptive} {
			zeroCompactions, zeroAttempts := 0, 0
			rows[c.ID] = labBenchmarkResult{Version: 2, Type: "lab_benchmark_case", CaseID: c.ID, Split: c.Split, Variant: variant,
				Counters: &domain.ResearchCounters{}, Compactions: &zeroCompactions, ProbeAttempts: &zeroAttempts, ResourceMaxAgeSeconds: benchmarkResourceMaxAgeSeconds,
				ProtocolSHA256: labHash([]byte("protocol")), ExperimentID: labHash([]byte("protocol")),
				DatasetSHA256: labHash(labCasesJSON), CorpusSHA256: labHash(labCorpusJSON), ModelProfileHash: labHash([]byte("model")), BudgetHash: labHash(raw), Budget: &budget,
				RunID: variant + "-" + c.ID, ConversationID: "conversation-" + variant + "-" + c.ID, Status: domain.StatusPaused,
				StartedAt: time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 10, 28, 1, 0, 1, 0, time.UTC), WallSeconds: 1,
				CollectionComplete: true, OutputCollected: true, OutputSHA256: labHash(nil), ErrorCode: "benchmark_paused", PauseReason: "manual", ResourceReason: "high_model_rss"}
		}
	}
	return base, adaptive
}

func TestBenchmarkFullComparisonAccountsPausedFailedAndMissingMetrics(t *testing.T) {
	b, a := benchmarkComparisonFixture(t)
	r := a["dev-fasta"]
	r.Status, r.Outcome = domain.StatusFailed, "failed"
	a[r.CaseID] = r
	rows, err := compareLabResults(b, a)
	if err != nil || len(rows) != 12 {
		t.Fatalf("%v %v", rows, err)
	}
	if rows[0]["source_recall_delta"] != nil || rows[0]["sections_delta"] != nil {
		t.Fatal("unknown became zero delta")
	}
	s := benchmarkSummaries(b, a)["adaptive"].(map[string]any)
	all := s["all"].(map[string]any)
	if all["planned_cases"] != 12 || all["paused"] != 11 || all["failed"] != 1 || all["finished_reports"] != 0 || all["mean_expected_source_recall"] != nil {
		t.Fatalf("%v", all)
	}
	if s["development"].(map[string]any)["planned_cases"] != 8 || s["heldout"].(map[string]any)["planned_cases"] != 4 {
		t.Fatal("wrong split denominators")
	}
}

func TestBenchmarkComparisonRejectsSubsetMixedIdentityAndInvalidMetrics(t *testing.T) {
	for _, mode := range []string{"subset", "split", "model", "budget", "protocol", "run", "accuracy", "range", "negative", "missing-proxy"} {
		t.Run(mode, func(t *testing.T) {
			b, a := benchmarkComparisonFixture(t)
			r := a["heldout-protocol"]
			v := 2.
			switch mode {
			case "subset":
				delete(a, r.CaseID)
			case "split":
				r.Split = "development"
			case "model":
				r.ModelProfileHash = labHash([]byte("other"))
			case "budget":
				cp := *r.Budget
				cp.MaxLLMCalls++
				r.Budget = &cp
				raw, _ := json.Marshal(cp)
				r.BudgetHash = labHash(raw)
			case "protocol":
				r.ProtocolSHA256 = labHash([]byte("other"))
				r.ExperimentID = r.ProtocolSHA256
			case "run":
				r.RunID = b["dev-fasta"].RunID
			case "accuracy":
				r.IndependentFactualAccuracy = &v
			case "range":
				r.SourceRecall = &v
			case "negative":
				r.Counters.LLMCalls = -1
			case "missing-proxy":
				r.ScoringComplete = true
			}
			if mode != "subset" {
				a[r.CaseID] = r
			}
			if _, err := compareLabResults(b, a); err == nil {
				t.Fatal("accepted invalid experiment")
			}
		})
	}
}

func TestBenchmarkVersion2RequiresMetricPresenceAndTypes(t *testing.T) {
	b, _ := benchmarkComparisonFixture(t)
	raw, _ := json.Marshal(b["dev-fasta"])
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, mode := range []string{"missing", "null", "type", "counter"} {
		t.Run(mode, func(t *testing.T) {
			copyFields := map[string]json.RawMessage{}
			for k, v := range fields {
				copyFields[k] = v
			}
			switch mode {
			case "missing":
				delete(copyFields, "expected_source_recall")
			case "null":
				copyFields["output_bytes"] = json.RawMessage(`null`)
			case "type":
				copyFields["collection_complete"] = json.RawMessage(`"yes"`)
			case "counter":
				copyFields["counters"] = json.RawMessage(`{}`)
			}
			raw, _ := json.Marshal(copyFields)
			var row labBenchmarkResult
			if err := json.Unmarshal(raw, &row); err == nil {
				t.Fatal("accepted absent or mistyped metric")
			}
		})
	}
}

func TestBenchmarkLatestStateSurvivesTimeoutAndPausedCollection(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "pause"}[paused], func(t *testing.T) {
			cancelled, pauseRequests := false, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/conversations":
					_ = json.NewEncoder(w).Encode(domain.Conversation{ID: "conversation"})
				case "/api/conversations/conversation/messages":
					if r.Method == "POST" {
						_ = json.NewEncoder(w).Encode(domain.SubmitResponse{Run: domain.Run{ID: "run", AssistantMessageID: "answer"}})
					} else {
						_ = json.NewEncoder(w).Encode([]domain.ConversationMessage{{ID: "answer", RunID: "run", Role: "assistant", Content: "checkpoint journal idempotency"}})
					}
				case "/api/runs/run/runtime":
					status := domain.StatusRunning
					if paused {
						status = domain.StatusPaused
					}
					projection := benchmarkRuntimeFixture(status)
					projection["counters"], projection["compactions"], projection["resource_probe_attempts"] = domain.ResearchCounters{LLMCalls: 7, Evidence: 2}, 4, 3
					projection["pause_reason"], projection["resource_reason"], projection["research_stop_reason"] = "manual", "high_model_rss", "resource_limit"
					_ = json.NewEncoder(w).Encode(projection)
				case "/api/runs/run/evidence":
					_, _ = w.Write([]byte(`[]`))
				case "/api/runs/run/cancel":
					cancelled = true
					_, _ = w.Write([]byte(`{}`))
				case "/api/tasks/run/pause":
					pauseRequests++
					_, _ = w.Write([]byte(`{}`))
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cases, _ := labCases()
			row, err := runLabCase(context.Background(), &labClient{base: server.URL, client: server.Client()}, cases[7], "baseline", 100*time.Millisecond, paused)
			if err == nil || row.Counters == nil || row.Counters.LLMCalls != 7 || row.Counters.Evidence != 2 || row.Compactions == nil || *row.Compactions != 4 || row.ProbeAttempts == nil || *row.ProbeAttempts != 3 || row.ResourceReason != "high_model_rss" || row.StopReason != "resource_limit" {
				t.Fatalf("row=%+v err=%v", row, err)
			}
			if !row.CollectionComplete || !row.ScoringComplete || row.Answer == "" || row.OutputSHA256 != labHash([]byte(row.Answer)) || row.Sections != nil || row.UnrepresentedCriteria != nil {
				t.Fatalf("collection=%+v", row)
			}
			if paused && (cancelled || pauseRequests != 0 || row.Status != domain.StatusPaused || row.ErrorCode != "benchmark_paused") {
				t.Fatal("paused checkpoint changed")
			}
			if !paused && (!cancelled || row.Status != domain.StatusRunning || row.ErrorCode != "benchmark_timeout") {
				t.Fatal("timeout lost state or cleanup")
			}
		})
	}
}

func TestBenchmarkOutputExclusiveAndProtocolRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.jsonl")
	if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	root.SetArgs([]string{"lab", "benchmark", "--protocol-sha256", labHash([]byte("protocol")), "--output", path})
	if err := root.Execute(); err == nil {
		t.Fatal("overwrote receipt")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "retained" {
		t.Fatal("changed existing receipt")
	}
	root = NewRoot()
	root.SetArgs([]string{"lab", "benchmark"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "protocol-sha256") {
		t.Fatalf("%v", err)
	}
}

func TestBenchmarkCommandPassivePolicyHoldAndSingleCaseReceipt(t *testing.T) {
	for _, hold := range []bool{false, true} {
		t.Run(map[bool]string{false: "hold_required", true: "single_case"}[hold], func(t *testing.T) {
			submissions, policies := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/lab/policy":
					policies++
					if r.Method != "GET" {
						t.Errorf("nonpassive policy request %s", r.Method)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"hold_resource_pauses": hold, "research_limits": map[string]any{"privacy_mode": domain.PrivacyOffline, "offline_corpus_sha256": labHash(labCorpusJSON), "adaptive_enabled": false, "sectioned_report_enabled": true}})
				case "/api/conversations":
					_ = json.NewEncoder(w).Encode(domain.Conversation{ID: "conversation"})
				case "/api/conversations/conversation/messages":
					if r.Method == "POST" {
						submissions++
						var input map[string]any
						_ = json.NewDecoder(r.Body).Decode(&input)
						if !strings.Contains(input["content"].(string), "checkpoint") {
							t.Error("wrong exact case")
						}
						_ = json.NewEncoder(w).Encode(domain.SubmitResponse{Run: domain.Run{ID: "run", AssistantMessageID: "answer"}})
					} else {
						_ = json.NewEncoder(w).Encode([]domain.ConversationMessage{{ID: "answer", RunID: "run", Role: "assistant", Content: "checkpoint journal idempotency"}})
					}
				case "/api/runs/run/runtime":
					projection := benchmarkRuntimeFixture(domain.StatusPaused)
					projection["pause_reason"], projection["resource_reason"] = "manual", "high_model_rss"
					_ = json.NewEncoder(w).Encode(projection)
				case "/api/runs/run/evidence":
					_, _ = w.Write([]byte(`[]`))
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			t.Setenv("APP_BIND_ADDR", strings.TrimPrefix(server.URL, "http://"))
			t.Setenv("RESEARCH_PRIVACY_MODE", domain.PrivacyExternalSearch)
			t.Setenv("NOTIFICATION_CHANNEL", "desktop")
			path := filepath.Join(t.TempDir(), "receipt.jsonl")
			var stdout bytes.Buffer
			root := NewRoot()
			root.SetOut(&stdout)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"lab", "benchmark", "--case", "dev-recovery", "--protocol-sha256", labHash([]byte("protocol")), "--stop-on-pause", "--output", path})
			err := root.Execute()
			if err == nil || policies != 1 {
				t.Fatalf("err=%v policies=%d", err, policies)
			}
			if !hold {
				if submissions != 0 || !strings.Contains(err.Error(), "hold_resource_pauses") {
					t.Fatalf("err=%v submissions=%d", err, submissions)
				}
				return
			}
			if submissions != 1 {
				t.Fatalf("submissions=%d", submissions)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(raw, stdout.Bytes()) {
				t.Fatalf("stdout/file mismatch: %v", readErr)
			}
			rows, readErr := readLabResults(path)
			if readErr != nil || len(rows) != 1 {
				t.Fatalf("rows=%v err=%v", rows, readErr)
			}
			if err := validateBenchmarkResult(rows["dev-recovery"]); err != nil {
				t.Fatal(err)
			}
			if _, err := compareLabResults(rows, rows); err == nil {
				t.Fatal("single case became full benchmark")
			}
		})
	}
}

func TestBenchmarkRSSOnlyCountsDistinctFreshSamples(t *testing.T) {
	var result labBenchmarkResult
	var r labBenchmarkRuntime
	app, model := int64(100), int64(1000)
	r.Runtime.Resources = resources.Snapshot{SnapshotAt: time.Now().Add(-6 * time.Second), RSSBytes: &app, ModelRSSBytes: &model}
	observeBenchmarkRuntime(&result, r)
	if result.ResourceSamples != 0 || result.AppRSS != nil || result.ModelRSS != nil {
		t.Fatal("counted stale sample")
	}
	r.Runtime.Resources.SnapshotAt = time.Now()
	observeBenchmarkRuntime(&result, r)
	observeBenchmarkRuntime(&result, r)
	if result.ResourceSamples != 1 || *result.AppRSS != app || *result.ModelRSS != model || result.ResourceMaxAgeSeconds != 5 {
		t.Fatal("duplicate or wrong RSS measurement")
	}
}

func TestBenchmarkRuntimeRejectsMissingNullAndInvalidMeasurements(t *testing.T) {
	for _, field := range []string{"budget", "counters", "compactions", "resource_probe_attempts", "budget.max_llm_calls", "budget.wall_time_seconds", "counters.llm_calls"} {
		for _, mode := range []string{"missing", "null", "invalid"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				raw, _ := json.Marshal(benchmarkRuntimeFixture(domain.StatusCompleted))
				var fields map[string]any
				_ = json.Unmarshal(raw, &fields)
				parts := strings.Split(field, ".")
				target := fields
				if len(parts) == 2 {
					target = fields[parts[0]].(map[string]any)
				}
				key := parts[len(parts)-1]
				switch mode {
				case "missing":
					delete(target, key)
				case "null":
					target[key] = nil
				case "invalid":
					target[key] = -1
				}
				raw, _ = json.Marshal(fields)
				var projection labBenchmarkRuntime
				if err := json.Unmarshal(raw, &projection); !errors.Is(err, errBenchmarkRuntimeInvalid) {
					t.Fatalf("accepted %s/%s: %v", field, mode, err)
				}
			})
		}
	}
	// Core v1 keys remain supported; v2-only omitempty fields are not required.
	projection := benchmarkRuntimeFixture(domain.StatusCompleted)
	budget := projection["budget"].(domain.ResearchBudget)
	budget.MaxInitialPlanSteps, budget.MaxRoundSteps, budget.MaxTotalSteps = 0, 0, 0
	budget.MaxReportSections, budget.MaxReportParagraphs, budget.MaxReportBytes = 0, 0, 0
	projection["budget"] = budget
	raw, _ := json.Marshal(projection)
	var decoded labBenchmarkRuntime
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("legacy budget: %v", err)
	}
}

func TestBenchmarkFullComparisonRejectsKnownRuntimeMismatch(t *testing.T) {
	for _, code := range []string{"benchmark_policy_mismatch", "benchmark_snapshot_changed", "runtime_identity_mismatch", "benchmark_runtime_invalid"} {
		t.Run(code, func(t *testing.T) {
			b, a := benchmarkComparisonFixture(t)
			r := a["heldout-protocol"]
			r.ErrorCode = code
			a[r.CaseID] = r
			// Every final model/budget hash matches: known earlier drift still disqualifies it.
			if _, err := compareLabResults(b, a); err == nil {
				t.Fatal("accepted known runtime mismatch")
			}
		})
	}
}

func TestBenchmarkInvalidRuntimeRetainsUnknownMeasurements(t *testing.T) {
	cancelled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/conversations":
			_ = json.NewEncoder(w).Encode(domain.Conversation{ID: "conversation"})
		case "/api/conversations/conversation/messages":
			_ = json.NewEncoder(w).Encode(domain.SubmitResponse{Run: domain.Run{ID: "run", AssistantMessageID: "answer"}})
		case "/api/runs/run/runtime":
			projection := benchmarkRuntimeFixture(domain.StatusCompleted)
			projection["budget"] = nil
			_ = json.NewEncoder(w).Encode(projection)
		case "/api/runs/run/cancel":
			cancelled = true
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cases, _ := labCases()
	row, err := runLabCase(context.Background(), &labClient{base: server.URL, client: server.Client()}, cases[0], "baseline", time.Minute)
	if err == nil || row.ErrorCode != "benchmark_runtime_invalid" || row.Budget != nil || row.Counters != nil || row.Compactions != nil || row.ProbeAttempts != nil || row.CollectionComplete || !cancelled {
		t.Fatalf("%+v %v", row, err)
	}
	raw, _ := json.Marshal(row)
	if !bytes.Contains(raw, []byte(`"counters":null`)) || !bytes.Contains(raw, []byte(`"compactions":null`)) {
		t.Fatalf("unknown became zero: %s", raw)
	}
}

func TestBenchmarkRunLabCaseContextReceiptCompatibility(t *testing.T) {
	for _, status := range []domain.TaskStatus{domain.StatusCompleted, domain.StatusPaused} {
		t.Run(string(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/conversations":
					_ = json.NewEncoder(w).Encode(domain.Conversation{ID: "conversation"})
				case "/api/conversations/conversation/messages":
					if r.Method == "POST" {
						_ = json.NewEncoder(w).Encode(domain.SubmitResponse{Run: domain.Run{ID: "run", AssistantMessageID: "answer"}})
					} else {
						_ = json.NewEncoder(w).Encode([]domain.ConversationMessage{{ID: "answer", RunID: "run", Role: "assistant", Status: string(status), Content: "FASTA"}})
					}
				case "/api/runs/run/runtime":
					_ = json.NewEncoder(w).Encode(benchmarkRuntimeFixture(status))
				case "/api/runs/run/evidence":
					_, _ = w.Write([]byte(`[]`))
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cases, _ := labCases()
			row, err := runLabCase(context.Background(), &labClient{base: server.URL, client: server.Client()}, cases[0], "baseline", time.Minute, true)
			if (err == nil) != (status == domain.StatusCompleted) || row.AssistantMessageStatus != string(status) || row.SourceRecall == nil {
				t.Fatalf("%+v %v", row, err)
			}
			ram, model, vram, temp := int64(4<<30), int64(1<<30), int64(2000), float64(60)
			samples := []contextResourceSample{{RAMFree: &ram, ModelRSS: &model, GPUFree: &vram, GPUTemperature: &temp}, {RAMFree: &ram, ModelRSS: &model, GPUFree: &vram, GPUTemperature: &temp}}
			receipt := map[string]any{"probe": map[string]any{"status": "passed"}, "research": row, "summary_collection_complete": true}
			reasons := contextReceiptReasons(receipt, true, samples)
			if (len(reasons) == 0) != (status == domain.StatusCompleted) {
				t.Fatalf("context acceptance: %v", reasons)
			}
		})
	}
}

func TestBenchmarkReadEscaped512KiBAnswer(t *testing.T) {
	b, _ := benchmarkComparisonFixture(t)
	r := b["dev-fasta"]
	r.Answer = strings.Repeat("\x00", benchmarkAnswerLimit)
	r.OutputBytes, r.OutputSHA256 = len(r.Answer), labHash([]byte(r.Answer))
	raw, err := json.Marshal(r)
	if err != nil || len(raw) < 3<<20 {
		t.Fatalf("size=%d err=%v", len(raw), err)
	}
	path := filepath.Join(t.TempDir(), "escaped.jsonl")
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := readLabResults(path)
	if err != nil || rows["dev-fasta"].Answer != r.Answer {
		t.Fatalf("512 KiB escaped round trip: %v", err)
	}
}

func TestBenchmarkFullReceiptRoundTripAndComparisonCommand(t *testing.T) {
	b, a := benchmarkComparisonFixture(t)
	paths := []string{filepath.Join(t.TempDir(), "baseline.jsonl"), filepath.Join(t.TempDir(), "adaptive.jsonl")}
	for i, rows := range []map[string]labBenchmarkResult{b, a} {
		var out bytes.Buffer
		for _, row := range rows {
			if err := json.NewEncoder(&out).Encode(row); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(paths[i], out.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	root := NewRoot()
	root.SetOut(&out)
	root.SetArgs([]string{"lab", "compare", paths[0], paths[1]})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Cases     []map[string]any `json:"cases"`
		Summaries map[string]any   `json:"summaries"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Cases) != 12 || len(result.Summaries) != 2 {
		t.Fatalf("%v %s", err, out.Bytes())
	}
}

func TestBenchmarkSummariesSeparateOutputProxiesFromSuccessfulOutcomes(t *testing.T) {
	b, a := benchmarkComparisonFixture(t)
	r := a["dev-fasta"]
	r.Answer, r.OutputBytes = "FASTA", len("FASTA")
	r.OutputSHA256 = labHash([]byte(r.Answer))
	r.ScoringComplete = true
	zero, one := 0., 1.
	r.SourceRecall, r.ConceptHitRate = &zero, &one
	r.Status, r.Outcome = domain.StatusCompleted, "partial"
	r.ReportArtifactID, r.MarkdownArtifactID, r.ReportOutcome = "json", "markdown", "partial"
	sections, missing := 1, 2
	r.Sections, r.UnrepresentedCriteria = &sections, &missing
	a[r.CaseID] = r
	if _, err := compareLabResults(b, a); err != nil {
		t.Fatal(err)
	}
	all := benchmarkSummaries(b, a)["adaptive"].(map[string]any)["all"].(map[string]any)
	if all["scored_outputs"] != 1 || all["mean_lexical_concept_hit_rate"] != 1. || all["finished_reports"] != 1 || all["successful_run_outcomes"] != 0 || all["mean_citation_reference_validity"] != nil {
		t.Fatalf("%v", all)
	}
	r.Outcome, r.ReportOutcome = "completed", "completed"
	a[r.CaseID] = r
	all = benchmarkSummaries(b, a)["adaptive"].(map[string]any)["all"].(map[string]any)
	if all["successful_run_outcomes"] != 1 || all["successful_run_outcome_rate"] != 1./12. {
		t.Fatalf("%v", all)
	}
}

func TestBenchmarkCollectionRetainsOwnedEvidenceAndBoundedAnswer(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "oversized"}[oversized], func(t *testing.T) {
			answer := "FASTA [owned]"
			if oversized {
				answer = strings.Repeat("x", benchmarkAnswerLimit+1)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/conversations/conversation/messages":
					_ = json.NewEncoder(w).Encode([]domain.ConversationMessage{{ID: "answer", RunID: "run", Role: "assistant", Content: answer}})
				case "/api/runs/run/evidence":
					_ = json.NewEncoder(w).Encode([]domain.Evidence{{ID: "owned", TaskID: "run", CanonicalURL: "https://docs.example.com/bioinformatics", ContentSHA256: labHash([]byte("source")), TextArtifactID: "text"}, {ID: "foreign", TaskID: "other", ContentSHA256: labHash([]byte("foreign")), TextArtifactID: "other-text"}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cases, _ := labCases()
			row := labBenchmarkResult{RunID: "run", ConversationID: "conversation"}
			err := collectBenchmarkOutput(context.Background(), &labClient{base: server.URL, client: server.Client()}, cases[0], "answer", &row)
			if oversized {
				if err == nil || row.CollectionComplete || row.OutputCollected || row.SourceRecall != nil {
					t.Fatal("oversized answer was truncated/scored")
				}
				return
			}
			if err != nil || !row.CollectionComplete || len(row.Evidence) != 1 || row.Evidence[0].ID != "owned" || row.Answer != answer || row.OutputSHA256 != labHash([]byte(answer)) || row.SourceRecall == nil || *row.SourceRecall != .5 {
				t.Fatalf("%+v %v", row, err)
			}
		})
	}
}
