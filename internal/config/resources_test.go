package config

import (
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/runtime/resources"
)

func TestResourceEnvironmentDefaultsAndBounds(t *testing.T) {
	clearConfigEnv(t)
	var absent Config
	if err := loadResourcePolicy(&absent); err != nil {
		t.Fatal(err)
	}
	if absent.ResourcePolicy != resources.DefaultPolicy() || absent.KeepAwake || absent.WorkerLeaseSeconds != 30 {
		t.Fatal("absent resource environment did not select defaults")
	}
	defaults := map[string]string{
		"RESOURCE_GUARD_ENABLED": "true", "MIN_FREE_RAM_BYTES": "536870912", "MIN_FREE_VRAM_MB": "256",
		"GPU_TEMP_MAX_C": "85", "PROCESS_RSS_MAX_BYTES": "2147483648", "LLAMA_HEALTH_TIMEOUT_MS": "2000",
		"RESOURCE_RECOVERY_RAM_MARGIN_PERCENT": "10", "RESOURCE_RECOVERY_VRAM_MARGIN_PERCENT": "10",
		"RESOURCE_RECOVERY_GPU_TEMP_MARGIN_C": "5", "RESOURCE_RECOVERY_RSS_MARGIN_PERCENT": "10",
		"RESOURCE_CHECK_INTERVAL_SECONDS": "5", "RESOURCE_MAX_RESUME_ATTEMPTS": "5", "RESOURCE_BACKOFF_SECONDS": "5",
		"RESOURCE_BACKOFF_MAX_SECONDS": "60", "WORKER_LEASE_SECONDS": "30", "KEEP_AWAKE": "false",
	}
	for key, value := range defaults {
		t.Setenv(key, value)
	}
	var cfg Config
	if err := loadResourcePolicy(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ResourcePolicy != resources.DefaultPolicy() || cfg.KeepAwake || cfg.WorkerLeaseSeconds != 30 {
		t.Fatalf("unexpected resource defaults: %#v", cfg.ResourcePolicy)
	}
	for _, test := range []struct{ key, value string }{
		{"RESOURCE_GUARD_ENABLED", "perhaps"}, {"KEEP_AWAKE", "perhaps"}, {"MIN_FREE_RAM_BYTES", "-1"},
		{"MIN_FREE_VRAM_MB", "1073741825"}, {"PROCESS_RSS_MAX_BYTES", "9223372036854775808"},
		{"GPU_TEMP_MAX_C", "NaN"}, {"GPU_TEMP_MAX_C", "+Inf"}, {"GPU_TEMP_MAX_C", "151"},
		{"LLAMA_HEALTH_TIMEOUT_MS", "0"}, {"LLAMA_HEALTH_TIMEOUT_MS", "60001"},
		{"RESOURCE_CHECK_INTERVAL_SECONDS", "9223372036854775807"}, {"RESOURCE_MAX_RESUME_ATTEMPTS", "0"},
		{"RESOURCE_MAX_RESUME_ATTEMPTS", "1001"}, {"RESOURCE_BACKOFF_SECONDS", "61"},
		{"RESOURCE_BACKOFF_MAX_SECONDS", "86401"}, {"WORKER_LEASE_SECONDS", "4"}, {"WORKER_LEASE_SECONDS", "301"},
		{"RESOURCE_RECOVERY_RAM_MARGIN_PERCENT", "-1"}, {"RESOURCE_RECOVERY_RAM_MARGIN_PERCENT", "101"},
		{"RESOURCE_RECOVERY_VRAM_MARGIN_PERCENT", "101"}, {"RESOURCE_RECOVERY_RSS_MARGIN_PERCENT", "101"},
		{"RESOURCE_RECOVERY_GPU_TEMP_MARGIN_C", "-1"}, {"RESOURCE_RECOVERY_GPU_TEMP_MARGIN_C", "NaN"},
		{"RESOURCE_RECOVERY_GPU_TEMP_MARGIN_C", "151"}, {"RESOURCE_RECOVERY_GPU_TEMP_MARGIN_C", "86"},
	} {
		t.Run(test.key+"/"+test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if err := loadResourcePolicy(&Config{}); err == nil {
				t.Fatal("invalid resource setting accepted")
			}
		})
	}
	t.Setenv("MIN_FREE_VRAM_MB", "0")
	t.Setenv("GPU_TEMP_MAX_C", "0")
	t.Setenv("KEEP_AWAKE", "true")
	t.Setenv("RESOURCE_GUARD_ENABLED", "false")
	if err := loadResourcePolicy(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ResourcePolicy.Enabled || cfg.ResourcePolicy.MinFreeVRAMMB != 0 || cfg.ResourcePolicy.GPUTempMaxC != 0 || !cfg.KeepAwake || cfg.ResourcePolicy.CheckInterval != 5*time.Second {
		t.Fatal("explicit resource overrides lost")
	}
	t.Setenv("RESOURCE_RECOVERY_RAM_MARGIN_PERCENT", "25")
	t.Setenv("RESOURCE_RECOVERY_VRAM_MARGIN_PERCENT", "30")
	t.Setenv("RESOURCE_RECOVERY_GPU_TEMP_MARGIN_C", "7.5")
	t.Setenv("RESOURCE_RECOVERY_RSS_MARGIN_PERCENT", "15")
	if err := loadResourcePolicy(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ResourcePolicy.RecoveryRAMMarginPercent != 25 || cfg.ResourcePolicy.RecoveryVRAMMarginPercent != 30 || cfg.ResourcePolicy.RecoveryGPUTempMarginC != 7.5 || cfg.ResourcePolicy.RecoveryRSSMarginPercent != 15 {
		t.Fatalf("recovery margin overrides lost: %+v", cfg.ResourcePolicy)
	}
}
