package api

import (
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/slager2/qwenchik/internal/domain"
)

const (
	maxPublicTelemetryBytes = 32 << 10
	maxPublicTelemetryGPUs  = 16
)

func (s *Server) SetRuntimeStatus(status func() map[string]any) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.runtimeStatus = status
}

func (s *Server) runtimeHealth() map[string]any {
	s.healthMu.RLock()
	status := s.runtimeStatus
	s.healthMu.RUnlock()
	if status == nil {
		return map[string]any{"status": "unknown"}
	}
	return SafeRuntimeStatus(status())
}

func runtimeObject(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 16384 {
		return nil
	}
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func safeRuntimeFields(input map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"enabled", "available", "active", "keep_awake", "llama_healthy", "healthy", "cancel_requested"} {
		if value, ok := input[key].(bool); ok {
			result[key] = value
		}
	}
	for key, maximum := range map[string]float64{
		"free_ram_bytes": 1 << 50, "rss_bytes": 1 << 50, "heap_alloc_bytes": 1 << 50,
		"gpu_free_vram_mb": 1 << 30, "gpu_temp_c": 200, "llama_health_latency": float64(time.Hour),
		"resource_probe_attempts": 1000, "max_resume_attempts": 1000, "retry_count": 1000,
	} {
		if value, ok := input[key].(float64); ok && value >= 0 && value <= maximum && !math.IsNaN(value) && !math.IsInf(value, 0) {
			result[key] = value
		} else if value, exists := input[key]; exists && value == nil {
			result[key] = nil
		}
	}
	for key, allowed := range map[string][]string{
		"status":           {"unknown", "ok", "paused", "queued", "running", "planning", "verifying", "failed", "completed", "cancelled", "blocked", "needs_review", "partial", "budget_exhausted"},
		"power_source":     {"unknown", "AC", "battery"},
		"provider":         {"systemd-inhibit", "unavailable", "unknown", "none"},
		"sleep_policy":     {"disabled", "keep_awake", "opt_in", "requested", "enabled", "best_effort", "unknown"},
		"pause_reason":     {"resource_guard", "recovery_required", "manual", ""},
		"terminal_outcome": {"complete", "completed", "partial", "blocked", "needs_review", "budget_exhausted", "cancelled", "failed", ""},
		"resource_reason":  {"low_ram", "low_vram", "high_rss", "gpu_temperature", "llama_unhealthy", "llama_timeout", "model_timeout", "model_oom", "model_resource", "telemetry_unavailable", ""},
		"error_code":       {"resource_guard_exhausted", "deadline_exceeded", "failed_recovery_required", "fetch_outcome_unknown", "generation_interrupted", ""},
	} {
		if value, ok := input[key].(string); ok {
			for _, candidate := range allowed {
				if value == candidate {
					result[key] = value
					break
				}
			}
		}
	}
	for _, key := range []string{"snapshot_at", "resume_after", "deadline_at"} {
		if value, ok := input[key].(string); ok && len(value) <= 64 {
			if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
				result[key] = at.UTC()
			}
		}
	}
	for _, key := range []string{"failure_reason", "llama_health_reason"} {
		if value, ok := input[key].(string); ok && value != "" {
			result[key] = "runtime provider reported unavailable"
		}
	}
	for _, key := range []string{"warnings", "active_warnings"} {
		if values, ok := input[key].([]any); ok {
			warnings := []string{}
			for _, value := range values {
				if len(warnings) == 16 {
					break
				}
				if text, ok := value.(string); ok {
					for _, metric := range []string{"RAM", "process RSS", "GPU VRAM", "GPU temperature", "llama health"} {
						if text == metric+" unknown; configured guard cannot evaluate this metric" {
							warnings = append(warnings, text)
						}
					}
				}
			}
			if len(values) > 0 && len(warnings) == 0 {
				warnings = append(warnings, "runtime warning reported")
			}
			result[key] = warnings
		}
	}
	return result
}

func SafeRuntimeStatus(input map[string]any) map[string]any {
	result := safeRuntimeFields(runtimeObject(input))
	for _, key := range []string{"resources", "last_snapshot", "snapshot", "sleep", "keep_awake", "sleep_policy"} {
		if object := runtimeObject(input[key]); object != nil {
			result[key] = safeRuntimeFields(object)
		}
	}
	return result
}

// SafeTelemetry constructs a bounded public representation from the typed
// worker snapshot. Invalid individual metrics become unavailable readings;
// invalid snapshot-level invariants cause the snapshot to be omitted.
func SafeTelemetry(input domain.Telemetry) (domain.Telemetry, bool) {
	if input.Timestamp.IsZero() || input.Timestamp.Year() < 2000 || input.Timestamp.After(time.Now().Add(5*time.Minute)) {
		return domain.Telemetry{}, false
	}
	if input.StaleAfterMS <= 0 || input.StaleAfterMS > int64((24*time.Hour)/time.Millisecond) {
		return domain.Telemetry{}, false
	}
	at := input.Timestamp.UTC()
	out := domain.Telemetry{
		Timestamp:    at,
		StaleAfterMS: input.StaleAfterMS,
		Capabilities: domain.TelemetryCapabilities{
			CPU: input.Capabilities.CPU, RAM: input.Capabilities.RAM, GPU: input.Capabilities.GPU,
			GPUTemperature: input.Capabilities.GPUTemperature, GPUUtilization: input.Capabilities.GPUUtilization,
			Power: input.Capabilities.Power, Battery: input.Capabilities.Battery, ProcessRSS: input.Capabilities.ProcessRSS,
			LlamaHealth: input.Capabilities.LlamaHealth, LlamaTimings: input.Capabilities.LlamaTimings,
			LlamaMetrics: input.Capabilities.LlamaMetrics, LlamaSlots: input.Capabilities.LlamaSlots,
		},
	}
	out.System.Metadata = domain.SystemMetadata{
		OS:              safeTelemetryOS(input.System.Metadata.OS),
		Platform:        safeTelemetryPlatform(input.System.Metadata.Platform),
		PlatformVersion: safeTelemetryVersion(input.System.Metadata.PlatformVersion, 128),
		KernelVersion:   safeTelemetryVersion(input.System.Metadata.KernelVersion, 128),
		KernelArch:      safeTelemetryArch(input.System.Metadata.KernelArch),
		UptimeSeconds:   safeTelemetryMetric(input.System.Metadata.UptimeSeconds, domain.MetricUnitSeconds, at, func(value float64) bool { return finiteBetween(value, 0, 1e10) }),
	}
	system := input.System
	out.System.CPUUsage = safeTelemetryMetric(system.CPUUsage, domain.MetricUnitPercent, at, func(value float64) bool { return finiteBetween(value, 0, 100) })
	out.System.CPUTemperature = safeTelemetryMetric(system.CPUTemperature, domain.MetricUnitCelsius, at, func(value float64) bool { return finiteBetween(value, -100, 200) })
	out.System.RAMUsed = safeTelemetryMetric(system.RAMUsed, domain.MetricUnitBytes, at, telemetryNonnegativeBytes)
	out.System.RAMFree = safeTelemetryMetric(system.RAMFree, domain.MetricUnitBytes, at, telemetryNonnegativeBytes)
	out.System.RAMTotal = safeTelemetryMetric(system.RAMTotal, domain.MetricUnitBytes, at, telemetryPositiveBytes)
	out.System.AgentRSS = safeTelemetryMetric(system.AgentRSS, domain.MetricUnitBytes, at, telemetryNonnegativeBytes)
	out.System.LlamaServerRSS = safeTelemetryMetric(system.LlamaServerRSS, domain.MetricUnitBytes, at, telemetryNonnegativeBytes)
	out.System.PowerSource = safeTelemetryMetric(system.PowerSource, domain.MetricUnitStatus, at, func(value string) bool {
		return value == "AC" || value == "battery" || value == "unknown"
	})
	out.System.PowerWatts = safeTelemetryMetric(system.PowerWatts, domain.MetricUnitWatts, at, func(value float64) bool { return finiteBetween(value, 0, 1e6) })
	out.System.BatteryPercent = safeTelemetryMetric(system.BatteryPercent, domain.MetricUnitPercent, at, func(value float64) bool { return finiteBetween(value, 0, 100) })
	out.System.BatteryCharging = safeTelemetryMetric(system.BatteryCharging, domain.MetricUnitBoolean, at, func(bool) bool { return true })
	out.System.LlamaHealth = safeTelemetryMetric(system.LlamaHealth, domain.MetricUnitStatus, at, func(value string) bool {
		return value == domain.LlamaHealthHealthy || value == domain.LlamaHealthUnhealthy
	})
	out.System.LlamaSlotsActive = safeTelemetryMetric(system.LlamaSlotsActive, domain.MetricUnitSlots, at, telemetryNonnegativeSlots)
	out.System.LlamaSlotsTotal = safeTelemetryMetric(system.LlamaSlotsTotal, domain.MetricUnitSlots, at, telemetryNonnegativeSlots)
	gpuCount := len(system.GPUs)
	if gpuCount > maxPublicTelemetryGPUs {
		gpuCount = maxPublicTelemetryGPUs
	}
	out.System.GPUs = make([]domain.GPUTelemetry, 0, gpuCount)
	for _, gpu := range system.GPUs[:gpuCount] {
		out.System.GPUs = append(out.System.GPUs, domain.GPUTelemetry{
			Name:        safeTelemetryMetric(gpu.Name, domain.MetricUnitName, at, safeTelemetryGPUName),
			Utilization: safeTelemetryMetric(gpu.Utilization, domain.MetricUnitPercent, at, func(value float64) bool { return finiteBetween(value, 0, 100) }),
			Temperature: safeTelemetryMetric(gpu.Temperature, domain.MetricUnitCelsius, at, func(value float64) bool { return finiteBetween(value, -100, 200) }),
			VRAMUsed:    safeTelemetryMetric(gpu.VRAMUsed, domain.MetricUnitBytes, at, telemetryNonnegativeBytes),
			VRAMFree:    safeTelemetryMetric(gpu.VRAMFree, domain.MetricUnitBytes, at, telemetryNonnegativeBytes),
			VRAMTotal:   safeTelemetryMetric(gpu.VRAMTotal, domain.MetricUnitBytes, at, telemetryPositiveBytes),
			PowerWatts:  safeTelemetryMetric(gpu.PowerWatts, domain.MetricUnitWatts, at, func(value float64) bool { return finiteBetween(value, 0, 1e6) }),
		})
	}
	inference := input.Inference
	if !inference.Status.Valid() || inference.UpdatedAt.IsZero() || inference.UpdatedAt.After(at) {
		return domain.Telemetry{}, false
	}
	out.Inference = domain.InferenceTelemetry{
		Status:                inference.Status,
		UpdatedAt:             inference.UpdatedAt.UTC(),
		RequestedTokens:       safeTelemetryMetric(inference.RequestedTokens, domain.MetricUnitTokens, at, telemetryNonnegativeTokens),
		PromptTokens:          safeTelemetryMetric(inference.PromptTokens, domain.MetricUnitTokens, at, telemetryNonnegativeTokens),
		OutputTokens:          safeTelemetryMetric(inference.OutputTokens, domain.MetricUnitTokens, at, telemetryNonnegativeTokens),
		TotalTokens:           safeTelemetryMetric(inference.TotalTokens, domain.MetricUnitTokens, at, telemetryNonnegativeTokens),
		CachedPromptTokens:    safeTelemetryMetric(inference.CachedPromptTokens, domain.MetricUnitTokens, at, telemetryNonnegativeTokens),
		PromptTokensPerSecond: safeTelemetryMetric(inference.PromptTokensPerSecond, domain.MetricUnitTokensPerSecond, at, func(value float64) bool { return finiteBetween(value, 0, 1e9) }),
		OutputTokensPerSecond: safeTelemetryMetric(inference.OutputTokensPerSecond, domain.MetricUnitTokensPerSecond, at, func(value float64) bool { return finiteBetween(value, 0, 1e9) }),
		TTFTMS:                safeTelemetryMetric(inference.TTFTMS, domain.MetricUnitMilliseconds, at, func(value float64) bool { return finiteBetween(value, 0, 1e10) }),
		PromptMS:              safeTelemetryMetric(inference.PromptMS, domain.MetricUnitMilliseconds, at, func(value float64) bool { return finiteBetween(value, 0, 1e10) }),
		GenerationMS:          safeTelemetryMetric(inference.GenerationMS, domain.MetricUnitMilliseconds, at, func(value float64) bool { return finiteBetween(value, 0, 1e10) }),
		ContextTokens:         safeTelemetryMetric(inference.ContextTokens, domain.MetricUnitTokens, at, telemetryNonnegativeTokens),
		ContextWindow:         safeTelemetryMetric(inference.ContextWindow, domain.MetricUnitTokens, at, telemetryPositiveTokens),
	}
	if inference.Source == "llama.cpp" {
		out.Inference.Source = inference.Source
	}
	if err := out.Validate(); err != nil {
		return domain.Telemetry{}, false
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > maxPublicTelemetryBytes {
		return domain.Telemetry{}, false
	}
	return out, true
}

func telemetrySystemSampleTimestamp(telemetry domain.Telemetry) time.Time {
	system := telemetry.System
	hasSample := telemetry.Capabilities.CPU || telemetry.Capabilities.RAM || telemetry.Capabilities.GPU ||
		telemetry.Capabilities.GPUTemperature || telemetry.Capabilities.GPUUtilization || telemetry.Capabilities.Power ||
		telemetry.Capabilities.Battery || telemetry.Capabilities.ProcessRSS || telemetry.Capabilities.LlamaHealth ||
		telemetry.Capabilities.LlamaTimings || telemetry.Capabilities.LlamaMetrics || telemetry.Capabilities.LlamaSlots ||
		system.Metadata.OS != "" || system.Metadata.Platform != "" || system.Metadata.PlatformVersion != "" ||
		system.Metadata.KernelVersion != "" || system.Metadata.KernelArch != ""
	for _, observed := range []bool{
		telemetryMetricObserved(system.Metadata.UptimeSeconds), telemetryMetricObserved(system.CPUUsage),
		telemetryMetricObserved(system.CPUTemperature), telemetryMetricObserved(system.RAMUsed), telemetryMetricObserved(system.RAMFree),
		telemetryMetricObserved(system.RAMTotal), telemetryMetricObserved(system.AgentRSS), telemetryMetricObserved(system.LlamaServerRSS),
		telemetryMetricObserved(system.PowerSource), telemetryMetricObserved(system.PowerWatts), telemetryMetricObserved(system.BatteryPercent),
		telemetryMetricObserved(system.BatteryCharging), telemetryMetricObserved(system.LlamaHealth),
		telemetryMetricObserved(system.LlamaSlotsActive), telemetryMetricObserved(system.LlamaSlotsTotal),
	} {
		hasSample = hasSample || observed
	}
	for _, gpu := range system.GPUs {
		hasSample = hasSample || telemetryMetricObserved(gpu.Name) || telemetryMetricObserved(gpu.Utilization) ||
			telemetryMetricObserved(gpu.Temperature) || telemetryMetricObserved(gpu.VRAMUsed) || telemetryMetricObserved(gpu.VRAMFree) ||
			telemetryMetricObserved(gpu.VRAMTotal) || telemetryMetricObserved(gpu.PowerWatts)
	}
	if !hasSample {
		return time.Time{}
	}
	timestamps := []time.Time{
		system.Metadata.UptimeSeconds.UpdatedAt,
		system.CPUUsage.UpdatedAt, system.CPUTemperature.UpdatedAt,
		system.RAMUsed.UpdatedAt, system.RAMFree.UpdatedAt, system.RAMTotal.UpdatedAt,
		system.AgentRSS.UpdatedAt, system.LlamaServerRSS.UpdatedAt,
		system.PowerSource.UpdatedAt, system.PowerWatts.UpdatedAt,
		system.BatteryPercent.UpdatedAt, system.BatteryCharging.UpdatedAt,
		system.LlamaHealth.UpdatedAt, system.LlamaSlotsActive.UpdatedAt, system.LlamaSlotsTotal.UpdatedAt,
	}
	for _, gpu := range system.GPUs {
		timestamps = append(timestamps, gpu.Name.UpdatedAt, gpu.Utilization.UpdatedAt, gpu.Temperature.UpdatedAt,
			gpu.VRAMUsed.UpdatedAt, gpu.VRAMFree.UpdatedAt, gpu.VRAMTotal.UpdatedAt, gpu.PowerWatts.UpdatedAt)
	}
	var latest time.Time
	for _, updatedAt := range timestamps {
		if updatedAt.After(latest) {
			latest = updatedAt
		}
	}
	if latest.IsZero() {
		return telemetry.Timestamp
	}
	return latest
}

func telemetryMetricObserved[T domain.MetricValue](metric domain.Metric[T]) bool {
	return metric.Source != domain.MetricSourceUnavailable || metric.Error != ""
}

func safeTelemetryMetric[T domain.MetricValue](input domain.Metric[T], unit string, snapshotAt time.Time, validValue func(T) bool) domain.Metric[T] {
	unavailableAt := snapshotAt
	if !input.UpdatedAt.IsZero() && !input.UpdatedAt.After(snapshotAt) {
		unavailableAt = input.UpdatedAt.UTC()
	}
	unavailable := domain.NewUnavailableMetric[T](unit, unavailableAt)
	if input.Validate() != nil || input.Unit != unit || input.UpdatedAt.After(snapshotAt) {
		return unavailable
	}
	if input.Value != nil && !validValue(*input.Value) {
		return unavailable
	}
	input.UpdatedAt = input.UpdatedAt.UTC()
	if input.Value != nil {
		value := *input.Value
		input.Value = &value
	}
	return input
}

func safeTelemetryLabel(value string, limit int) string {
	if len(value) == 0 || len(value) > limit {
		return ""
	}
	hasLetterOrDigit := false
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			hasLetterOrDigit = true
			continue
		}
		if strings.ContainsRune(" ._+-()", char) {
			continue
		}
		return ""
	}
	if !hasLetterOrDigit {
		return ""
	}
	return value
}

func safeTelemetryOS(value string) string {
	switch strings.ToLower(value) {
	case "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows":
		return strings.ToLower(value)
	default:
		return ""
	}
}

func safeTelemetryPlatform(value string) string {
	if safeTelemetryLabel(value, 128) == "" {
		return ""
	}
	allowed := map[string]struct{}{
		"almalinux": {}, "alpine": {}, "amazon": {}, "arch": {}, "centos": {}, "chrome": {}, "datacenter": {}, "debian": {}, "darwin": {},
		"elementary": {}, "enterprise": {}, "fedora": {}, "freebsd": {}, "gentoo": {}, "gnu": {}, "hat": {}, "illumos": {},
		"kali": {}, "leap": {}, "linux": {}, "mac": {}, "macos": {}, "manjaro": {}, "microsoft": {}, "mint": {}, "netbsd": {},
		"nixos": {}, "openbsd": {}, "opensuse": {}, "oracle": {}, "os": {}, "pop": {}, "pro": {}, "raspbian": {}, "raspberry": {},
		"red": {}, "rocky": {}, "server": {}, "solaris": {}, "standard": {}, "suse": {}, "tumbleweed": {}, "ubuntu": {},
		"windows": {}, "workstation": {}, "zorin": {},
	}
	for _, token := range strings.Fields(strings.ToLower(value)) {
		token = strings.Trim(token, ".()")
		if _, ok := allowed[token]; !ok {
			if !telemetryVersionToken(token) {
				return ""
			}
		}
	}
	return value
}

func telemetryVersionToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !unicode.IsDigit(char) && char != '.' {
			return false
		}
	}
	return true
}

func safeTelemetryVersion(value string, limit int) string {
	if value == "" || len(value) > limit {
		return ""
	}
	first := true
	hasDigit := false
	for _, char := range value {
		if first {
			first = false
			if !unicode.IsDigit(char) {
				return ""
			}
		}
		if unicode.IsDigit(char) {
			hasDigit = true
			continue
		}
		if unicode.IsLetter(char) || strings.ContainsRune("._+-()", char) {
			continue
		}
		return ""
	}
	if !hasDigit {
		return ""
	}
	return value
}

func safeTelemetryArch(value string) string {
	arch := strings.ToLower(value)
	switch arch {
	case "386", "amd64", "arm", "arm64", "armv6l", "armv7l", "armv8l", "aarch64", "i386", "i686", "loong64", "mips", "mips64", "mips64le", "mipsle", "ppc64", "ppc64le", "riscv64", "s390x", "wasm", "x86", "x86_64":
		return arch
	default:
		return ""
	}
}

func safeTelemetryGPUName(value string) bool {
	if safeTelemetryLabel(value, 128) == "" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(value))
	allowed := map[string]struct{}{
		"adapter": {}, "amd": {}, "apple": {}, "arc": {}, "arm": {}, "basic": {}, "display": {}, "geforce": {}, "graphics": {},
		"gpu": {}, "gtx": {}, "hd": {}, "intel": {}, "iris": {}, "llvmpipe": {}, "m1": {}, "m2": {}, "m3": {}, "m4": {},
		"mali": {}, "microsoft": {}, "nvidia": {}, "pro": {}, "qualcomm": {}, "quadro": {}, "r": {}, "radeon": {}, "rtx": {},
		"rx": {}, "software": {}, "tesla": {}, "uhd": {}, "ultra": {}, "vega": {}, "virtio": {}, "virtual": {}, "xe": {},
		"xt": {}, "xtx": {}, "max": {}, "laptop": {},
	}
	hasModelNumber := false
	for _, token := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(name)) {
		if _, ok := allowed[token]; ok {
			if tokenHasDigit(token) {
				hasModelNumber = true
			}
			continue
		}
		if telemetryModelToken(token) {
			hasModelNumber = true
			continue
		}
		return false
	}
	return hasModelNumber || strings.Contains(name, "gpu")
}

func telemetryModelToken(value string) bool {
	hasDigit := false
	for _, char := range value {
		if unicode.IsDigit(char) {
			hasDigit = true
			continue
		}
		if unicode.IsLetter(char) || strings.ContainsRune("._+-", char) {
			continue
		}
		return false
	}
	return hasDigit
}

func tokenHasDigit(value string) bool {
	for _, char := range value {
		if unicode.IsDigit(char) {
			return true
		}
	}
	return false
}

func finiteBetween(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func telemetryNonnegativeBytes(value int64) bool { return value >= 0 && value <= 1<<60 }
func telemetryPositiveBytes(value int64) bool    { return value > 0 && value <= 1<<60 }
func telemetryNonnegativeSlots(value int64) bool { return value >= 0 && value <= 1_000_000 }
func telemetryNonnegativeTokens(value int64) bool {
	return value >= 0 && value <= 1<<40
}
func telemetryPositiveTokens(value int64) bool { return value > 0 && value <= 1<<40 }

func resourceEventType(kind string) bool {
	switch kind {
	case "run.paused", "run.resumed", "run.resource_probe", "run.blocked", "run.failed":
		return true
	}
	return false
}

func safeResourceEvent(raw json.RawMessage) map[string]any {
	if len(raw) > 16384 {
		return map[string]any{}
	}
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	return safeRuntimeFields(input)
}
