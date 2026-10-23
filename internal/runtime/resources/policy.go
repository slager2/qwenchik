package resources

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type Policy struct {
	Enabled                   bool          `json:"enabled"`
	MinFreeRAMBytes           int64         `json:"min_free_ram_bytes"`
	MinFreeVRAMMB             int64         `json:"min_free_vram_mb"`
	GPUTempMaxC               float64       `json:"gpu_temp_max_c"`
	ProcessRSSMaxBytes        int64         `json:"process_rss_max_bytes"`
	RecoveryRAMMarginPercent  int           `json:"recovery_ram_margin_percent"`
	RecoveryVRAMMarginPercent int           `json:"recovery_vram_margin_percent"`
	RecoveryGPUTempMarginC    float64       `json:"recovery_gpu_temp_margin_c"`
	RecoveryRSSMarginPercent  int           `json:"recovery_rss_margin_percent"`
	LlamaHealthTimeout        time.Duration `json:"llama_health_timeout"`
	CheckInterval             time.Duration `json:"check_interval"`
	MaxResumeAttempts         int           `json:"max_resume_attempts"`
	BackoffBase               time.Duration `json:"backoff_base"`
	BackoffMax                time.Duration `json:"backoff_max"`
}

func DefaultPolicy() Policy {
	return Policy{
		Enabled:                   true,
		MinFreeRAMBytes:           512 << 20,
		MinFreeVRAMMB:             256,
		GPUTempMaxC:               85,
		ProcessRSSMaxBytes:        2 << 30,
		RecoveryRAMMarginPercent:  10,
		RecoveryVRAMMarginPercent: 10,
		RecoveryGPUTempMarginC:    5,
		RecoveryRSSMarginPercent:  10,
		LlamaHealthTimeout:        2 * time.Second,
		CheckInterval:             5 * time.Second,
		MaxResumeAttempts:         5,
		BackoffBase:               5 * time.Second,
		BackoffMax:                60 * time.Second,
	}
}

func (p Policy) Validate() error {
	if p.MinFreeRAMBytes < 0 || p.MinFreeVRAMMB < 0 || p.ProcessRSSMaxBytes < 0 || p.GPUTempMaxC < 0 || math.IsNaN(p.GPUTempMaxC) || math.IsInf(p.GPUTempMaxC, 0) {
		return errors.New("resource thresholds must be finite and nonnegative")
	}
	if p.RecoveryRAMMarginPercent < 0 || p.RecoveryRAMMarginPercent > 100 || p.RecoveryVRAMMarginPercent < 0 || p.RecoveryVRAMMarginPercent > 100 || p.RecoveryRSSMarginPercent < 0 || p.RecoveryRSSMarginPercent > 100 {
		return errors.New("resource recovery percentage margins must be between 0 and 100")
	}
	if p.RecoveryGPUTempMarginC < 0 || p.RecoveryGPUTempMarginC > 150 || math.IsNaN(p.RecoveryGPUTempMarginC) || math.IsInf(p.RecoveryGPUTempMarginC, 0) || p.GPUTempMaxC > 0 && p.RecoveryGPUTempMarginC > p.GPUTempMaxC {
		return errors.New("GPU temperature recovery margin must be finite, nonnegative, and no greater than the configured maximum")
	}
	if p.LlamaHealthTimeout <= 0 || p.CheckInterval <= 0 || p.MaxResumeAttempts <= 0 || p.BackoffBase <= 0 || p.BackoffMax < p.BackoffBase {
		return errors.New("resource timeouts, interval, attempts and backoff must be positive and ordered")
	}
	return nil
}

func (p Policy) Backoff(attempt int) time.Duration { return Backoff(attempt, p) }

func Backoff(attempt int, p Policy) time.Duration {
	if p.BackoffBase <= 0 || p.BackoffMax <= 0 {
		return 0
	}
	d := p.BackoffBase
	if d >= p.BackoffMax {
		return p.BackoffMax
	}
	for attempt > 1 {
		if d > p.BackoffMax/2 {
			return p.BackoffMax
		}
		d *= 2
		attempt--
	}
	return d
}

type PowerSource string

const (
	PowerUnknown PowerSource = "unknown"
	PowerAC      PowerSource = "AC"
	PowerBattery PowerSource = "battery"
)

type Snapshot struct {
	SnapshotAt         time.Time                     `json:"snapshot_at"`
	FreeRAMBytes       *int64                        `json:"free_ram_bytes"`
	RSSBytes           *int64                        `json:"rss_bytes"`
	HeapAllocBytes     *uint64                       `json:"heap_alloc_bytes"`
	GPUFreeVRAMMB      *int64                        `json:"gpu_free_vram_mb"`
	GPUTempC           *float64                      `json:"gpu_temp_c"`
	LlamaHealthy       *bool                         `json:"llama_healthy"`
	LlamaHealthLatency *time.Duration                `json:"llama_health_latency"`
	LlamaHealthReason  string                        `json:"llama_health_reason,omitempty"`
	Warnings           []string                      `json:"warnings"`
	PowerSource        PowerSource                   `json:"power_source"`
	System             *domain.SystemTelemetry       `json:"system,omitempty"`
	Capabilities       *domain.TelemetryCapabilities `json:"capabilities,omitempty"`
	StaleAfterMS       *int64                        `json:"stale_after_ms,omitempty"`
}

type Violation struct {
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Snapshot Snapshot `json:"snapshot"`
}

func (v *Violation) Error() string { return v.Code + ": " + v.Message }

func Check(s Snapshot, p Policy) *Violation {
	if !p.Enabled {
		return nil
	}
	code, message := "", ""
	switch {
	case s.FreeRAMBytes != nil && *s.FreeRAMBytes < p.MinFreeRAMBytes:
		code, message = "low_ram", "available RAM below configured minimum"
	case s.RSSBytes != nil && p.ProcessRSSMaxBytes > 0 && *s.RSSBytes > p.ProcessRSSMaxBytes:
		code, message = "high_rss", "process RSS exceeds configured maximum"
	case s.GPUFreeVRAMMB != nil && *s.GPUFreeVRAMMB < p.MinFreeVRAMMB:
		code, message = "low_vram", "available GPU VRAM below configured minimum"
	case s.GPUTempC != nil && p.GPUTempMaxC > 0 && *s.GPUTempC > p.GPUTempMaxC:
		code, message = "gpu_temperature", "GPU temperature exceeds configured maximum"
	case s.LlamaHealthy != nil && !*s.LlamaHealthy:
		code, message = "llama_unhealthy", "local llama health check failed"
	}
	if code == "" {
		return nil
	}
	return &Violation{code, message, clone(s)}
}

// CheckRecovery checks whether a sampled snapshot has cleared the hysteresis
// thresholds required to resume a resource-paused task. Missing metrics are
// only an obstruction when they explain the pause, or when the pause itself
// was caused by unavailable telemetry.
func CheckRecovery(s Snapshot, p Policy, pauseReason string) *Violation {
	if !p.Enabled {
		return nil
	}
	if s.SnapshotAt.IsZero() || s.StaleAfterMS == nil || *s.StaleAfterMS <= 0 {
		return &Violation{"telemetry_unavailable", "resource recovery snapshot freshness is unknown", clone(s)}
	}
	age := time.Since(s.SnapshotAt)
	if age < 0 || age.Milliseconds() > *s.StaleAfterMS {
		return &Violation{"telemetry_unavailable", "resource recovery snapshot is stale", clone(s)}
	}
	if !recoveryPauseReasonKnown(pauseReason) {
		return &Violation{"telemetry_unavailable", "resource pause reason is not recognized for recovery", clone(s)}
	}
	if missingRecoveryMetric(s, p, pauseReason) {
		return &Violation{"telemetry_unavailable", "required recovery telemetry is unavailable", clone(s)}
	}
	code, message := "", ""
	switch {
	case s.FreeRAMBytes != nil && *s.FreeRAMBytes < recoveryMinimum(p.MinFreeRAMBytes, p.RecoveryRAMMarginPercent):
		code, message = "low_ram", "available RAM has not cleared the recovery margin"
	case s.RSSBytes != nil && p.ProcessRSSMaxBytes > 0 && *s.RSSBytes > recoveryMaximum(p.ProcessRSSMaxBytes, p.RecoveryRSSMarginPercent):
		code, message = "high_rss", "process RSS has not cleared the recovery margin"
	case s.GPUFreeVRAMMB != nil && *s.GPUFreeVRAMMB < recoveryMinimum(p.MinFreeVRAMMB, p.RecoveryVRAMMarginPercent):
		code, message = "low_vram", "available GPU VRAM has not cleared the recovery margin"
	case s.GPUTempC != nil && p.GPUTempMaxC > 0 && *s.GPUTempC > p.GPUTempMaxC-p.RecoveryGPUTempMarginC:
		code, message = "gpu_temperature", "GPU temperature has not cleared the recovery margin"
	case s.LlamaHealthy != nil && !*s.LlamaHealthy:
		code, message = "llama_unhealthy", "local llama health check failed"
	}
	if code == "" {
		return nil
	}
	return &Violation{code, message, clone(s)}
}

func recoveryPauseReasonKnown(reason string) bool {
	switch reason {
	case "low_ram", "high_rss", "low_vram", "gpu_temperature", "llama_unhealthy",
		"telemetry_unavailable", "model_oom", "model_resource", "model_timeout":
		return true
	default:
		return false
	}
}

func missingRecoveryMetric(s Snapshot, p Policy, pauseReason string) bool {
	if s.LlamaHealthy == nil {
		return true
	}
	if pauseReason == "telemetry_unavailable" || pauseReason == "model_oom" || pauseReason == "model_resource" || pauseReason == "model_timeout" {
		return p.MinFreeRAMBytes > 0 && s.FreeRAMBytes == nil ||
			p.ProcessRSSMaxBytes > 0 && s.RSSBytes == nil ||
			p.MinFreeVRAMMB > 0 && s.GPUFreeVRAMMB == nil ||
			p.GPUTempMaxC > 0 && s.GPUTempC == nil || s.LlamaHealthy == nil
	}
	switch pauseReason {
	case "low_ram":
		return s.FreeRAMBytes == nil
	case "high_rss":
		return s.RSSBytes == nil
	case "low_vram":
		return s.GPUFreeVRAMMB == nil
	case "gpu_temperature":
		return s.GPUTempC == nil
	case "llama_unhealthy":
		return s.LlamaHealthy == nil
	default:
		return true
	}
}

func recoveryMinimum(minimum int64, marginPercent int) int64 {
	if minimum <= 0 || marginPercent <= 0 {
		return minimum
	}
	whole := minimum / 100 * int64(marginPercent)
	remainder := minimum % 100 * int64(marginPercent)
	extra := whole + remainder/100
	if remainder%100 != 0 {
		extra++
	}
	if extra > math.MaxInt64-minimum {
		return math.MaxInt64
	}
	return minimum + extra
}

func recoveryMaximum(maximum int64, marginPercent int) int64 {
	if maximum <= 0 || marginPercent <= 0 {
		return maximum
	}
	remainingPercent := int64(100 - marginPercent)
	return maximum/100*remainingPercent + maximum%100*remainingPercent/100
}

func thresholdWarnings(s *Snapshot, p Policy) {
	if !p.Enabled {
		return
	}
	for _, item := range []struct {
		missing bool
		name    string
	}{
		{s.FreeRAMBytes == nil && p.MinFreeRAMBytes > 0, "RAM"},
		{s.RSSBytes == nil && p.ProcessRSSMaxBytes > 0, "process RSS"},
		{s.GPUFreeVRAMMB == nil && p.MinFreeVRAMMB > 0, "GPU VRAM"},
		{s.GPUTempC == nil && p.GPUTempMaxC > 0, "GPU temperature"},
		{s.LlamaHealthy == nil, "llama health"},
	} {
		if item.missing {
			warning := fmt.Sprintf("%s unknown; configured guard cannot evaluate this metric", item.name)
			found := false
			for _, existing := range s.Warnings {
				if existing == warning {
					found = true
					break
				}
			}
			if !found {
				s.Warnings = append(s.Warnings, warning)
			}
		}
	}
}
