package config

import (
	"fmt"
	"time"

	"github.com/slager2/qwenchik/internal/runtime/resources"
)

func loadResourcePolicy(c *Config) error {
	p := resources.DefaultPolicy()
	var err error
	if p.Enabled, err = boolEnv("RESOURCE_GUARD_ENABLED", true); err != nil {
		return err
	}
	if c.KeepAwake, err = boolEnv("KEEP_AWAKE", false); err != nil {
		return err
	}
	for _, item := range []struct {
		name     string
		target   *int64
		fallback int64
	}{
		{"MIN_FREE_RAM_BYTES", &p.MinFreeRAMBytes, 536870912},
		{"MIN_FREE_VRAM_MB", &p.MinFreeVRAMMB, 256},
		{"PROCESS_RSS_MAX_BYTES", &p.ProcessRSSMaxBytes, 2147483648},
	} {
		if *item.target, err = int64Env(item.name, item.fallback); err != nil {
			return err
		}
	}
	if p.GPUTempMaxC, err = floatEnv("GPU_TEMP_MAX_C", 85); err != nil {
		return err
	}
	for _, item := range []struct {
		name              string
		target            *time.Duration
		fallback, maximum int
		unit              time.Duration
	}{
		{"LLAMA_HEALTH_TIMEOUT_MS", &p.LlamaHealthTimeout, 2000, 60000, time.Millisecond},
		{"RESOURCE_CHECK_INTERVAL_SECONDS", &p.CheckInterval, 5, 3600, time.Second},
		{"RESOURCE_BACKOFF_SECONDS", &p.BackoffBase, 5, 3600, time.Second},
		{"RESOURCE_BACKOFF_MAX_SECONDS", &p.BackoffMax, 60, 86400, time.Second},
	} {
		value, e := intEnv(item.name, item.fallback)
		if e != nil {
			return e
		}
		if value < 1 || value > item.maximum {
			return fmt.Errorf("%s must be between 1 and %d", item.name, item.maximum)
		}
		*item.target = time.Duration(value) * item.unit
	}
	if p.MaxResumeAttempts, err = intEnv("RESOURCE_MAX_RESUME_ATTEMPTS", 5); err != nil {
		return err
	}
	if c.WorkerLeaseSeconds, err = intEnv("WORKER_LEASE_SECONDS", 30); err != nil {
		return err
	}
	c.ResourcePolicy = p
	return c.validateResourcePolicy()
}

func (c Config) validateResourcePolicy() error {
	p := c.ResourcePolicy
	if err := p.Validate(); err != nil {
		return err
	}
	if p.MinFreeRAMBytes > 1<<50 || p.ProcessRSSMaxBytes > 1<<50 || p.MinFreeVRAMMB > 1<<30 || p.GPUTempMaxC > 150 {
		return fmt.Errorf("resource thresholds exceed supported bounds")
	}
	if p.LlamaHealthTimeout > time.Minute || p.CheckInterval > time.Hour || p.BackoffBase > time.Hour || p.BackoffMax > 24*time.Hour || p.MaxResumeAttempts > 1000 {
		return fmt.Errorf("resource timing or attempt limits exceed supported bounds")
	}
	if c.WorkerLeaseSeconds < 5 || c.WorkerLeaseSeconds > 300 {
		return fmt.Errorf("WORKER_LEASE_SECONDS must be between 5 and 300")
	}
	return nil
}
