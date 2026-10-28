package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

type contextResourceSample struct {
	At             time.Time `json:"at"`
	RAMFree        *int64    `json:"ram_free_bytes"`
	RAMUsed        *int64    `json:"ram_used_bytes"`
	CollectorRSS   *int64    `json:"collector_rss_bytes"`
	ModelRSS       *int64    `json:"model_rss_bytes"`
	GPUFree        *int64    `json:"gpu_free_mib"`
	GPUTemperature *float64  `json:"gpu_temperature_c"`
}

func contextSample(s resources.Snapshot) contextResourceSample {
	r := contextResourceSample{At: s.SnapshotAt, RAMFree: s.FreeRAMBytes, CollectorRSS: s.RSSBytes, GPUFree: s.GPUFreeVRAMMB, GPUTemperature: s.GPUTempC}
	if s.System != nil {
		r.RAMUsed = s.System.RAMUsed.Value
		r.ModelRSS = s.System.LlamaServerRSS.Value
	}
	return r
}

func writeContextReceipt(path string, receipt any) error {
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("context receipt exceeds bound")
	}
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

func newLabContextCommand() *cobra.Command {
	var window int
	var output, modelFile string
	var research bool
	cmd := &cobra.Command{Use: "context-check", Short: "Measure one configured context rung through the running shared guard and existing worker", RunE: func(cmd *cobra.Command, _ []string) error {
		if window != 32768 && window != 65536 && window != 98304 && window != 131072 {
			return errors.New("--context must be 32768,65536,98304,131072")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		client := newLabClient(cfg)
		client.client.Timeout = 190 * time.Second
		receipt := map[string]any{"type": "context_ladder_rung", "protocol_version": 1, "context": window, "started_at": time.Now().UTC(), "independent_factual_accuracy": nil, "research_requested": research, "model": cfg.LLMModel, "kv_k": "q8_0", "kv_v": "q8_0", "gpu_layers": 99, "launch_flags_provenance": "operator-controlled installed-runtime flags; verify deployment manifest", "sample_period_seconds": 2}
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					receipt["base_git_commit"] = setting.Value
				}
				if setting.Key == "vcs.modified" {
					receipt["working_tree_modified"] = setting.Value
				}
			}
		}
		if modelFile != "" {
			f, err := os.Open(modelFile)
			if err != nil {
				return err
			}
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<30 {
				f.Close()
				return errors.New("model file must be a bounded regular GGUF")
			}
			header := make([]byte, 4)
			if _, err := io.ReadFull(f, header); err != nil || string(header) != "GGUF" {
				f.Close()
				return errors.New("--model-file must be a GGUF")
			}
			if _, err := f.Seek(0, 0); err != nil {
				f.Close()
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			receipt["gguf_sha256"] = hex.EncodeToString(h.Sum(nil))
			receipt["gguf"] = filepath.Base(modelFile)
		}
		provider, err := resources.NewHostProvider(cfg.LLMBaseURL, cfg.ResourcePolicy)
		receipt["resource_policy"] = cfg.ResourcePolicy
		if err != nil {
			return err
		}
		var mu sync.Mutex
		samples := []contextResourceSample{}
		sampleErrors, truncated := 0, false
		sample := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			s, err := provider.Snapshot(ctx)
			if err == nil {
				mu.Lock()
				if len(samples) < 1024 {
					samples = append(samples, contextSample(s))
				} else {
					truncated = true
				}
				mu.Unlock()
			} else {
				mu.Lock()
				sampleErrors++
				mu.Unlock()
			}
		}
		sample()
		sampleCtx, stop := context.WithCancel(cmd.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-sampleCtx.Done():
					return
				case <-ticker.C:
					sample()
				}
			}
		}()
		var probe map[string]any
		probeErr := client.request(cmd.Context(), "POST", "/api/doctor/context-probe", map[string]int{"context": window}, &probe)
		receipt["probe"] = probe
		if probeErr != nil {
			receipt["probe_error"] = probeErr.Error()
		}
		canResearch := probeErr == nil && (probe["status"] == "passed" || probe["error_code"] == "target_or_retention_not_established")
		if research && canResearch {
			var doctor struct {
				Limits struct {
					Mode string `json:"privacy_mode"`
					Hash string `json:"offline_corpus_sha256"`
				} `json:"research_limits"`
			}
			if err := client.request(cmd.Context(), "GET", "/api/doctor", nil, &doctor); err != nil {
				receipt["research_error"] = err.Error()
			} else if doctor.Limits.Mode != domain.PrivacyOffline || doctor.Limits.Hash != labHash(labCorpusJSON) {
				receipt["research_error"] = "requires exact offline laboratory corpus"
			} else {
				cases, _ := labCases()
				var selected labBenchmarkCase
				for _, c := range cases {
					if c.ID == "dev-recovery" {
						selected = c
					}
				}
				row, err := runLabCase(cmd.Context(), client, selected, "baseline", 12*time.Minute, true)
				receipt["research"] = row
				if err != nil {
					receipt["research_error"] = err.Error()
				}
				if row.RunID != "" {
					runtime, err := client.runtime(cmd.Context(), row.RunID)
					if err == nil {
						receipt["research_compactions"] = runtime.Compactions
						receipt["research_pause_reason"] = runtime.PauseReason
						receipt["research_resource_reason"] = runtime.ResourceReason
						receipt["research_context_budget"] = runtime.ContextBudget
						receipt["research_report"] = runtime.Report
						receipt["summary_high_watermark"] = runtime.Cursor
					} else {
						receipt["research_runtime_error"] = err.Error()
					}
					all := []sqlite.InvocationSummaryRecord{}
					after := int64(0)
					complete := false
					collectionCtx, collectionCancel := context.WithTimeout(cmd.Context(), 30*time.Second)
					for page := 0; page < 4; page++ {
						var result struct {
							Items    []sqlite.InvocationSummaryRecord `json:"summaries"`
							Next     int64                            `json:"next_after"`
							Complete bool                             `json:"complete"`
						}
						if err := client.request(collectionCtx, "GET", "/api/runs/"+row.RunID+"/inference-summaries?after="+strconv.FormatInt(after, 10)+"&until="+strconv.FormatInt(runtime.Cursor, 10), nil, &result); err != nil {
							receipt["inference_collection_error"] = err.Error()
							break
						}
						all = append(all, result.Items...)
						if result.Complete {
							complete = true
							break
						}
						if result.Next <= after {
							break
						}
						after = result.Next
					}
					collectionCancel()
					receipt["summary_collection_complete"] = complete
					receipt["summary_final_cursor"] = after
					receipt["research_invocations"] = all
				}
			}
		} else if research {
			receipt["research_not_run_reason"] = "load probe failed or resource-limited"
		}
		stop()
		<-done
		sample()
		mu.Lock()
		receipt["resource_samples"] = samples
		receipt["sample_errors"] = sampleErrors
		receipt["samples_truncated"] = truncated
		mu.Unlock()
		reasons := contextReceiptReasons(receipt, research, samples)
		receipt["accepted"] = len(reasons) == 0
		receipt["blocking_reasons"] = reasons
		receipt["acceptance_scope"] = "diagnostic measurement only, not long-duration or independent factual acceptance"
		receipt["finished_at"] = time.Now().UTC()
		if err := writeContextReceipt(output, receipt); err != nil {
			return err
		}
		printed := any(receipt)
		if output != "" {
			printed = map[string]any{"receipt": output, "context": window, "probe_status": probe["status"], "research": receipt["research"], "research_resource_reason": receipt["research_resource_reason"], "accepted": receipt["accepted"], "blocking_reasons": reasons}
		}
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(printed); err != nil {
			return err
		}
		if probeErr != nil {
			return probeErr
		}
		if len(reasons) > 0 {
			return errors.New("context rung measurement incomplete or resource-limited")
		}
		return nil
	}}
	cmd.Flags().IntVar(&window, "context", 32768, "configured/observed context rung")
	cmd.Flags().StringVar(&output, "output", "", "exclusive-create JSON receipt path")
	cmd.Flags().StringVar(&modelFile, "model-file", "", "local GGUF to fingerprint")
	cmd.Flags().BoolVar(&research, "research", true, "also run one fixed offline research case through the existing queue")
	return cmd
}

func contextReceiptReasons(receipt map[string]any, research bool, samples []contextResourceSample) []string {
	reasons := []string{}
	probe, _ := receipt["probe"].(map[string]any)
	if probe["status"] != "passed" {
		reasons = append(reasons, "load_probe_not_passed")
	}
	if research {
		row, ok := receipt["research"].(labBenchmarkResult)
		if !ok || !row.ScoringComplete || row.Status != domain.StatusCompleted || row.OutputBytes == 0 {
			reasons = append(reasons, "research_not_completed")
		}
		if receipt["summary_collection_complete"] != true {
			reasons = append(reasons, "summary_collection_incomplete")
		}
	}
	for _, key := range []string{"probe_error", "research_error", "research_runtime_error", "inference_collection_error"} {
		if _, ok := receipt[key]; ok {
			reasons = append(reasons, key)
		}
	}
	if n, _ := receipt["sample_errors"].(int); n > 0 {
		reasons = append(reasons, "sample_errors")
	}
	if receipt["samples_truncated"] == true {
		reasons = append(reasons, "samples_truncated")
	}
	if len(samples) < 2 {
		reasons = append(reasons, "insufficient_resource_samples")
	}
	for _, sample := range samples {
		if sample.ModelRSS == nil || sample.RAMFree == nil || sample.GPUFree == nil || sample.GPUTemperature == nil {
			reasons = append(reasons, "resource_measurement_unknown")
			break
		}
	}
	return reasons
}
