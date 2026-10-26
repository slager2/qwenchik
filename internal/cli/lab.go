package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/keepawake"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"github.com/spf13/cobra"
)

type labClient struct {
	base   string
	client *http.Client
}

func newLabClient(cfg config.Config) *labClient {
	return &labClient{base: "http://" + cfg.BindAddr, client: &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *labClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(r)
	if err != nil {
		return errors.New("local laboratory API unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("local laboratory API status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("laboratory API response exceeds limit")
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal(raw, output)
}

type labRuntime struct {
	ModelProfileHash       string                `json:"model_profile_hash"`
	SectionedReportEnabled bool                  `json:"sectioned_report_enabled"`
	Budget                 domain.ResearchBudget `json:"budget"`
	Report                 *struct {
		ArtifactID            string   `json:"artifact_id"`
		Sections              int      `json:"sections"`
		UnrepresentedCriteria []string `json:"unrepresented_criteria"`
	} `json:"research_report"`
	ResearchSessionID string                  `json:"research_session_id"`
	CompletedSteps    int                     `json:"completed_steps"`
	RunID             string                  `json:"task_id"`
	Status            domain.TaskStatus       `json:"status"`
	Cursor            int64                   `json:"cursor"`
	BundleHash        string                  `json:"bundle_hash"`
	PrivacyMode       string                  `json:"privacy_mode"`
	CorpusSHA256      string                  `json:"offline_corpus_sha256"`
	AdaptiveEnabled   bool                    `json:"adaptive_enabled"`
	Counters          domain.ResearchCounters `json:"counters"`
	Outcome           string                  `json:"terminal_outcome"`
	Runtime           struct {
		ActiveRunID string             `json:"active_run_id"`
		Policy      *resources.Policy  `json:"resource_policy"`
		Resources   resources.Snapshot `json:"resources"`
		Sleep       keepawake.Status   `json:"sleep"`
	} `json:"runtime"`
}

func (c *labClient) runtime(ctx context.Context, id string) (labRuntime, error) {
	var r labRuntime
	err := c.request(ctx, "GET", "/api/runs/"+url.PathEscape(id)+"/runtime", nil, &r)
	if err == nil && r.RunID != id {
		err = errors.New("laboratory runtime run ID mismatch")
	}
	return r, err
}

type soakReceipt struct {
	Type              string    `json:"type"`
	Stage             string    `json:"stage"`
	RunID             string    `json:"run_id"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
	ObservedSeconds   float64   `json:"observed_seconds"`
	RequiredSeconds   float64   `json:"required_seconds"`
	Samples           int       `json:"samples"`
	ProgressObserved  bool      `json:"progress_observed"`
	Accepted          bool      `json:"accepted"`
	Reasons           []string  `json:"reasons"`
	LidBehaviorProven bool      `json:"lid_behavior_proven"`
}

func soakDuration(stage string) (time.Duration, error) {
	switch stage {
	case "15m":
		return 15 * time.Minute, nil
	case "60m":
		return time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	}
	return 0, errors.New("stage must be 15m, 60m, or 4h")
}

type soakTracker struct {
	receipt                 soakReceipt
	firstCursor, lastCursor int64
	bundle                  string
	lastSample              time.Time
	lastWorkAt              time.Time
	lastWork                string
	workProgress            bool
	policy                  resources.Policy
}

func (s *soakTracker) sample(now time.Time, r labRuntime) {
	add := func(reason string) {
		for _, x := range s.receipt.Reasons {
			if x == reason {
				return
			}
		}
		s.receipt.Reasons = append(s.receipt.Reasons, reason)
	}
	if s.receipt.Samples == 0 {
		s.receipt.StartedAt = now
		s.firstCursor = r.Cursor
		s.bundle = r.BundleHash
		s.lastWorkAt = now
	} else if now.Sub(s.lastSample) > 45*time.Second {
		add("sampling_gap")
	}
	s.receipt.Samples++
	s.lastSample = now
	if r.ResearchSessionID == "" {
		add("research_session_missing")
	}
	if r.Runtime.ActiveRunID != r.RunID || r.RunID == "" {
		add("selected_run_not_active_in_worker")
	}
	work := fmt.Sprintf("%d:%d:%d:%d:%d", r.CompletedSteps, r.Counters.Evidence, r.Counters.Fetches, r.Counters.SearchQueries, r.Counters.LLMCalls)
	if s.lastWork != "" && s.lastWork != work {
		s.workProgress = true
		s.lastWorkAt = now
	}
	s.lastWork = work
	if now.Sub(s.lastWorkAt) > 10*time.Minute {
		add("no_research_work_for_ten_minutes")
	}
	p := resources.Policy{}
	if r.Runtime.Policy == nil || r.Runtime.Policy.Validate() != nil || !r.Runtime.Policy.Enabled {
		add("server_resource_policy_missing_or_disabled")
	} else {
		p = *r.Runtime.Policy
		if s.receipt.Samples == 1 {
			s.policy = p
		} else if s.policy != p {
			add("server_resource_policy_changed")
		}
	}
	if r.BundleHash == "" || r.BundleHash != s.bundle {
		add("bundle_unknown_or_changed")
	}
	if r.Cursor < s.lastCursor {
		add("event_cursor_regressed")
	}
	s.lastCursor = r.Cursor
	if r.Status.Terminal() {
		add("run_ended_before_observation_finished")
	}
	if r.Status == domain.StatusWaitingApproval {
		add("approval_blocks_unattended_work")
	}
	if r.Status != domain.StatusQueued && r.Status != domain.StatusPlanning && r.Status != domain.StatusRunning && r.Status != domain.StatusVerifying && r.Status != domain.StatusPaused && !r.Status.Terminal() && r.Status != domain.StatusWaitingApproval {
		add("unknown_run_status")
	}
	snapshot := r.Runtime.Resources
	if snapshot.SnapshotAt.IsZero() || now.Sub(snapshot.SnapshotAt) > 45*time.Second || snapshot.SnapshotAt.After(now.Add(5*time.Second)) {
		add("resource_snapshot_missing_or_stale")
	}
	if snapshot.FreeRAMBytes == nil || snapshot.RSSBytes == nil || snapshot.GPUTempC == nil || snapshot.GPUFreeVRAMMB == nil {
		add("resource_metric_unknown")
	}
	if snapshot.PowerSource != resources.PowerAC {
		add("ac_power_unconfirmed")
	}
	if !r.Runtime.Sleep.Active {
		add("sleep_inhibitor_inactive")
	}
	if snapshot.GPUTempC != nil && p.GPUTempMaxC > 0 && *snapshot.GPUTempC > p.GPUTempMaxC {
		add("gpu_temperature_limit_exceeded")
	}
	if snapshot.FreeRAMBytes != nil && *snapshot.FreeRAMBytes < p.MinFreeRAMBytes {
		add("ram_limit_exceeded")
	}
	if snapshot.GPUFreeVRAMMB != nil && *snapshot.GPUFreeVRAMMB < p.MinFreeVRAMMB {
		add("vram_limit_exceeded")
	}
	if snapshot.RSSBytes != nil && p.ProcessRSSMaxBytes > 0 && *snapshot.RSSBytes > p.ProcessRSSMaxBytes {
		add("rss_limit_exceeded")
	}
}
func (s *soakTracker) finish(now time.Time) soakReceipt {
	s.receipt.FinishedAt = now
	elapsed := time.Duration(0)
	if s.receipt.Samples > 0 {
		elapsed = s.lastSample.Sub(s.receipt.StartedAt)
		if now.Sub(s.lastSample) > 45*time.Second {
			s.receipt.Reasons = append(s.receipt.Reasons, "final_sample_stale")
		}
	}
	s.receipt.ObservedSeconds = elapsed.Seconds()
	s.receipt.ProgressObserved = s.workProgress
	if elapsed.Seconds() < s.receipt.RequiredSeconds {
		s.receipt.Reasons = append(s.receipt.Reasons, "observation_too_short")
	}
	if !s.receipt.ProgressObserved {
		s.receipt.Reasons = append(s.receipt.Reasons, "no_durable_progress")
	}
	s.receipt.Accepted = len(s.receipt.Reasons) == 0 && s.receipt.Samples >= 2
	return s.receipt
}

func newLabCommand(jsonMode *bool) *cobra.Command {
	command := &cobra.Command{Use: "lab", Short: "Research laboratory readiness and staged acceptance"}
	var runID string
	ready := &cobra.Command{Use: "readiness", Short: "Inspect current prerequisites; does not certify a soak", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		report := runningAppDoctor(cmd.Context(), cfg)
		blockers := []string{}
		for _, name := range []string{"app", "worker", "database", "artifacts", "llm", "fetch_temp"} {
			if report.Checks[name].Status != "ok" {
				blockers = append(blockers, name+"_not_ready")
			}
		}
		searchReady := report.Checks["search"].Status == "ok"
		// Current probe takes precedence over historical provider observations.
		if probe, ok := report.Checks["search_probe"]; ok {
			searchReady = probe.Status == "ok"
		}
		if !searchReady {
			blockers = append(blockers, "search_not_ready")
		}
		if runID == "" {
			blockers = append(blockers, "active_run_not_selected")
		} else {
			r, err := newLabClient(cfg).runtime(cmd.Context(), runID)
			if err != nil {
				blockers = append(blockers, "runtime_unavailable")
			} else {
				s := soakTracker{receipt: soakReceipt{Reasons: []string{}}}
				s.sample(time.Now(), r)
				blockers = append(blockers, s.receipt.Reasons...)
			}
		}
		printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{"type": "lab_readiness", "prerequisites_ready": len(blockers) == 0, "blockers": blockers, "doctor": report, "staged_acceptance": "not_established_by_readiness", "lid_behavior_proven": false})
		if len(blockers) > 0 {
			return errors.New("laboratory prerequisites are not ready")
		}
		return nil
	}}
	ready.Flags().StringVar(&runID, "run-id", "", "active research run to inspect")
	command.AddCommand(ready, newLabSoakCommand(), newLabBenchmarkCommand(), newLabCompareCommand())
	return command
}

func newLabSoakCommand() *cobra.Command {
	var runID, stage string
	cmd := &cobra.Command{Use: "soak", Short: "Observe an existing run for a real 15m, 60m or 4h interval; emits JSONL", RunE: func(cmd *cobra.Command, _ []string) error {
		duration, err := soakDuration(stage)
		if err != nil {
			return err
		}
		if strings.TrimSpace(runID) == "" {
			return errors.New("--run-id is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		client := newLabClient(cfg)
		var start time.Time
		tracker := soakTracker{receipt: soakReceipt{Type: "lab_soak", Stage: stage, RunID: runID, RequiredSeconds: duration.Seconds(), Reasons: []string{}}}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			r, err := client.runtime(cmd.Context(), runID)
			if err != nil {
				tracker.receipt.Reasons = append(tracker.receipt.Reasons, "runtime_request_failed")
				_ = encoder.Encode(tracker.finish(time.Now()))
				return err
			}
			sampledAt := time.Now()
			if start.IsZero() {
				start = sampledAt
			}
			tracker.sample(sampledAt, r)
			if err := encoder.Encode(map[string]any{"type": "lab_soak_sample", "run_id": runID, "observed_seconds": time.Since(start).Seconds(), "status": r.Status, "cursor": r.Cursor, "resources": r.Runtime.Resources, "sleep": r.Runtime.Sleep}); err != nil {
				return err
			}
			elapsed := time.Since(start)
			if elapsed >= duration || r.Status.Terminal() {
				receipt := tracker.finish(time.Now())
				if err := encoder.Encode(receipt); err != nil {
					return err
				}
				if !receipt.Accepted {
					return errors.New("soak did not satisfy acceptance conditions")
				}
				return nil
			}
			select {
			case <-cmd.Context().Done():
				tracker.receipt.Reasons = append(tracker.receipt.Reasons, "observer_interrupted")
				_ = encoder.Encode(tracker.finish(time.Now()))
				return cmd.Context().Err()
			case <-ticker.C:
			}
		}
	}}
	cmd.Flags().StringVar(&runID, "run-id", "", "existing research run ID")
	cmd.Flags().StringVar(&stage, "stage", "15m", "15m, 60m, or 4h (run in that order)")
	return cmd
}
