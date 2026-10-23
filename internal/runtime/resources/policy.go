package resources

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type Policy struct {
	Enabled            bool          `json:"enabled"`
	MinFreeRAMBytes    int64         `json:"min_free_ram_bytes"`
	MinFreeVRAMMB      int64         `json:"min_free_vram_mb"`
	GPUTempMaxC        float64       `json:"gpu_temp_max_c"`
	ProcessRSSMaxBytes int64         `json:"process_rss_max_bytes"`
	LlamaHealthTimeout time.Duration `json:"llama_health_timeout"`
	CheckInterval      time.Duration `json:"check_interval"`
	MaxResumeAttempts  int           `json:"max_resume_attempts"`
	BackoffBase        time.Duration `json:"backoff_base"`
	BackoffMax         time.Duration `json:"backoff_max"`
}

func DefaultPolicy() Policy {
	return Policy{true, 512 << 20, 256, 85, 2 << 30, 2 * time.Second, 5 * time.Second, 5, 5 * time.Second, 60 * time.Second}
}

func (p Policy) Validate() error {
	if p.MinFreeRAMBytes < 0 || p.MinFreeVRAMMB < 0 || p.ProcessRSSMaxBytes < 0 || p.GPUTempMaxC < 0 || math.IsNaN(p.GPUTempMaxC) || math.IsInf(p.GPUTempMaxC, 0) {
		return errors.New("resource thresholds must be finite and nonnegative")
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
