package orchestrator

import (
	"math"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/resources"
)

const (
	maxRunTelemetryEntries = 128
	maxRunTelemetryGPUs    = 32
	defaultStaleAfterMS    = int64(30_000)
)

type runTelemetryEntry struct {
	inference domain.InferenceTelemetry
	revision  uint64
	snapshot  *domain.Telemetry
}

type runTelemetrySource struct {
	mu           sync.RWMutex
	entries      map[string]runTelemetryEntry
	nextRevision uint64
}

func newRunTelemetrySource() *runTelemetrySource {
	return &runTelemetrySource{entries: make(map[string]runTelemetryEntry)}
}

func (source *runTelemetrySource) update(runID string, inference domain.InferenceTelemetry) {
	if source == nil || runID == "" || len(runID) > 256 || strings.ContainsAny(runID, "\x00\r\n") {
		return
	}
	if !validInferenceTelemetry(inference) {
		return
	}
	runID = strings.Clone(runID)
	inference = cloneInferenceTelemetry(inference)
	if !validInferenceTelemetry(inference) {
		return
	}

	source.mu.Lock()
	defer source.mu.Unlock()
	if source.entries == nil {
		source.entries = make(map[string]runTelemetryEntry)
	}
	previous, exists := source.entries[runID]
	if exists && inference.UpdatedAt.Before(previous.inference.UpdatedAt) {
		return
	}
	if source.nextRevision == math.MaxUint64 {
		return
	}
	source.nextRevision++
	if !exists && len(source.entries) >= maxRunTelemetryEntries {
		oldestRunID := ""
		oldestRevision := uint64(math.MaxUint64)
		for candidate, entry := range source.entries {
			if entry.revision < oldestRevision {
				oldestRunID, oldestRevision = candidate, entry.revision
			}
		}
		delete(source.entries, oldestRunID)
	}
	source.entries[runID] = runTelemetryEntry{inference: inference, revision: source.nextRevision}
}

func (source *runTelemetrySource) get(runID string) (domain.InferenceTelemetry, uint64, bool) {
	if source == nil {
		return domain.InferenceTelemetry{}, 0, false
	}
	source.mu.RLock()
	entry, ok := source.entries[runID]
	source.mu.RUnlock()
	if !ok {
		return domain.InferenceTelemetry{}, 0, false
	}
	return cloneInferenceTelemetry(entry.inference), entry.revision, true
}

func (source *runTelemetrySource) observe(runID string, expectedRevision uint64, snapshot domain.Telemetry) (uint64, bool) {
	source.mu.Lock()
	defer source.mu.Unlock()
	entry, ok := source.entries[runID]
	if !ok || entry.revision != expectedRevision {
		return 0, false
	}
	if entry.snapshot != nil && reflect.DeepEqual(*entry.snapshot, snapshot) {
		return entry.revision, true
	}
	if entry.snapshot != nil {
		if source.nextRevision == math.MaxUint64 {
			return entry.revision, true
		}
		source.nextRevision++
		entry.revision = source.nextRevision
	}
	copy := cloneTelemetrySnapshot(snapshot)
	entry.snapshot = &copy
	source.entries[runID] = entry
	return entry.revision, true
}

// recordRunTelemetry binds an inference update to the run that issued it.
func (w *Worker) recordRunTelemetry(runID string) func(domain.InferenceTelemetry) {
	return func(inference domain.InferenceTelemetry) {
		if w != nil && w.runTelemetry != nil {
			w.runTelemetry.update(runID, inference)
		}
	}
}

// RunTelemetry returns the latest validated process-local telemetry snapshot for a run.
func (w *Worker) RunTelemetry(runID string) (domain.Telemetry, uint64, bool) {
	if w == nil || w.runTelemetry == nil {
		return domain.Telemetry{}, 0, false
	}
	for attempt := 0; attempt < 3; attempt++ {
		inference, revision, ok := w.runTelemetry.get(runID)
		if !ok {
			return domain.Telemetry{}, 0, false
		}
		var resourceSnapshot resources.Snapshot
		if w.telemetryMonitor != nil {
			resourceSnapshot = w.telemetryMonitor.LastSnapshot()
		}
		telemetry, err := composeRunTelemetry(inference, resourceSnapshot)
		if err != nil {
			return domain.Telemetry{}, revision, false
		}
		if observedRevision, current := w.runTelemetry.observe(runID, revision, telemetry); current {
			return telemetry, observedRevision, true
		}
	}
	return domain.Telemetry{}, 0, false
}

func composeRunTelemetry(inference domain.InferenceTelemetry, snapshot resources.Snapshot) (domain.Telemetry, error) {
	inference = cloneInferenceTelemetry(inference)
	baseAt := inferenceTelemetryTimestamp(time.Time{}, inference)
	system := unavailableSystemTelemetry(baseAt)
	if snapshot.System != nil {
		system = cloneSystemTelemetry(*snapshot.System)
	}
	capabilities := domain.TelemetryCapabilities{}
	if snapshot.Capabilities != nil {
		capabilities = *snapshot.Capabilities
	}
	staleAfter := defaultStaleAfterMS
	if snapshot.StaleAfterMS != nil && *snapshot.StaleAfterMS > 0 {
		staleAfter = *snapshot.StaleAfterMS
	}

	telemetry := domain.Telemetry{
		Timestamp:    composedTelemetryTimestamp(baseAt, snapshot.SnapshotAt, system, inference),
		StaleAfterMS: staleAfter,
		Capabilities: capabilities,
		System:       system,
		Inference:    inference,
	}
	if err := telemetry.Validate(); err == nil {
		return telemetry, nil
	}

	// A monitor may not yet have a typed sample, or may return a partial one.
	// Preserve its capabilities and freshness while representing missing system
	// metrics as explicitly unavailable.
	telemetry.System = unavailableSystemTelemetry(baseAt)
	telemetry.Timestamp = composedTelemetryTimestamp(baseAt, snapshot.SnapshotAt, telemetry.System, inference)
	if err := telemetry.Validate(); err != nil {
		return domain.Telemetry{}, err
	}
	return telemetry, nil
}

func validInferenceTelemetry(inference domain.InferenceTelemetry) bool {
	baseAt := inferenceTelemetryTimestamp(time.Time{}, inference)
	telemetry := domain.Telemetry{
		Timestamp:    baseAt,
		StaleAfterMS: 1,
		System:       unavailableSystemTelemetry(baseAt),
		Inference:    inference,
	}
	return telemetry.Validate() == nil
}

func cloneTelemetrySnapshot(telemetry domain.Telemetry) domain.Telemetry {
	telemetry.System = cloneSystemTelemetry(telemetry.System)
	telemetry.Inference = cloneInferenceTelemetry(telemetry.Inference)
	return telemetry
}

func unavailableSystemTelemetry(at time.Time) domain.SystemTelemetry {
	return domain.SystemTelemetry{
		Metadata:         domain.SystemMetadata{UptimeSeconds: domain.NewUnavailableMetric[float64](domain.MetricUnitSeconds, at)},
		CPUUsage:         domain.NewUnavailableMetric[float64](domain.MetricUnitPercent, at),
		CPUTemperature:   domain.NewUnavailableMetric[float64](domain.MetricUnitCelsius, at),
		RAMUsed:          domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, at),
		RAMFree:          domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, at),
		RAMTotal:         domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, at),
		GPUs:             []domain.GPUTelemetry{},
		AgentRSS:         domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, at),
		LlamaServerRSS:   domain.NewUnavailableMetric[int64](domain.MetricUnitBytes, at),
		PowerSource:      domain.NewUnavailableMetric[string](domain.MetricUnitStatus, at),
		PowerWatts:       domain.NewUnavailableMetric[float64](domain.MetricUnitWatts, at),
		BatteryPercent:   domain.NewUnavailableMetric[float64](domain.MetricUnitPercent, at),
		BatteryCharging:  domain.NewUnavailableMetric[bool](domain.MetricUnitBoolean, at),
		LlamaHealth:      domain.NewUnavailableMetric[string](domain.MetricUnitStatus, at),
		LlamaSlotsActive: domain.NewUnavailableMetric[int64](domain.MetricUnitSlots, at),
		LlamaSlotsTotal:  domain.NewUnavailableMetric[int64](domain.MetricUnitSlots, at),
	}
}

func cloneInferenceTelemetry(inference domain.InferenceTelemetry) domain.InferenceTelemetry {
	inference.Source = strings.Clone(inference.Source)
	inference.RequestedTokens = cloneTelemetryMetric(inference.RequestedTokens)
	inference.PromptTokens = cloneTelemetryMetric(inference.PromptTokens)
	inference.OutputTokens = cloneTelemetryMetric(inference.OutputTokens)
	inference.TotalTokens = cloneTelemetryMetric(inference.TotalTokens)
	inference.CachedPromptTokens = cloneTelemetryMetric(inference.CachedPromptTokens)
	inference.PromptTokensPerSecond = cloneTelemetryMetric(inference.PromptTokensPerSecond)
	inference.OutputTokensPerSecond = cloneTelemetryMetric(inference.OutputTokensPerSecond)
	inference.TTFTMS = cloneTelemetryMetric(inference.TTFTMS)
	inference.PromptMS = cloneTelemetryMetric(inference.PromptMS)
	inference.GenerationMS = cloneTelemetryMetric(inference.GenerationMS)
	inference.ContextTokens = cloneTelemetryMetric(inference.ContextTokens)
	inference.ContextWindow = cloneTelemetryMetric(inference.ContextWindow)
	return inference
}

func cloneSystemTelemetry(system domain.SystemTelemetry) domain.SystemTelemetry {
	system.Metadata.OS = cloneBoundedString(system.Metadata.OS, 32)
	system.Metadata.Platform = cloneBoundedString(system.Metadata.Platform, 128)
	system.Metadata.PlatformVersion = cloneBoundedString(system.Metadata.PlatformVersion, 128)
	system.Metadata.KernelVersion = cloneBoundedString(system.Metadata.KernelVersion, 128)
	system.Metadata.KernelArch = cloneBoundedString(system.Metadata.KernelArch, 32)
	system.Metadata.UptimeSeconds = cloneTelemetryMetric(system.Metadata.UptimeSeconds)
	system.CPUUsage = cloneTelemetryMetric(system.CPUUsage)
	system.CPUTemperature = cloneTelemetryMetric(system.CPUTemperature)
	system.RAMUsed = cloneTelemetryMetric(system.RAMUsed)
	system.RAMFree = cloneTelemetryMetric(system.RAMFree)
	system.RAMTotal = cloneTelemetryMetric(system.RAMTotal)
	system.AgentRSS = cloneTelemetryMetric(system.AgentRSS)
	system.LlamaServerRSS = cloneTelemetryMetric(system.LlamaServerRSS)
	system.PowerSource = cloneBoundedStringMetric(system.PowerSource, 32)
	system.PowerWatts = cloneTelemetryMetric(system.PowerWatts)
	system.BatteryPercent = cloneTelemetryMetric(system.BatteryPercent)
	system.BatteryCharging = cloneTelemetryMetric(system.BatteryCharging)
	system.LlamaHealth = cloneBoundedStringMetric(system.LlamaHealth, 32)
	system.LlamaSlotsActive = cloneTelemetryMetric(system.LlamaSlotsActive)
	system.LlamaSlotsTotal = cloneTelemetryMetric(system.LlamaSlotsTotal)
	if len(system.GPUs) == 0 {
		system.GPUs = []domain.GPUTelemetry{}
	} else {
		gpuCount := min(len(system.GPUs), maxRunTelemetryGPUs)
		gpus := make([]domain.GPUTelemetry, gpuCount)
		for i, gpu := range system.GPUs[:gpuCount] {
			gpus[i] = domain.GPUTelemetry{
				Name:        cloneBoundedNameMetric(gpu.Name),
				Utilization: cloneTelemetryMetric(gpu.Utilization),
				Temperature: cloneTelemetryMetric(gpu.Temperature),
				VRAMUsed:    cloneTelemetryMetric(gpu.VRAMUsed),
				VRAMFree:    cloneTelemetryMetric(gpu.VRAMFree),
				VRAMTotal:   cloneTelemetryMetric(gpu.VRAMTotal),
				PowerWatts:  cloneTelemetryMetric(gpu.PowerWatts),
			}
		}
		system.GPUs = gpus
	}
	return system
}

func cloneTelemetryMetric[T domain.MetricValue](metric domain.Metric[T]) domain.Metric[T] {
	if metric.Value != nil {
		value := *metric.Value
		if text, ok := any(value).(string); ok {
			value = any(strings.Clone(text)).(T)
		}
		metric.Value = &value
	}
	return metric
}

func cloneBoundedNameMetric(metric domain.Metric[string]) domain.Metric[string] {
	return cloneBoundedStringMetric(metric, 128)
}

func cloneBoundedStringMetric(metric domain.Metric[string], maxBytes int) domain.Metric[string] {
	if metric.Value == nil {
		return metric
	}
	value := cloneBoundedString(*metric.Value, maxBytes)
	metric.Value = &value
	return metric
}

func cloneBoundedString(value string, maxBytes int) string {
	if len(value) > maxBytes {
		value = value[:maxBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return strings.Clone(value)
}

func composedTelemetryTimestamp(now, snapshotAt time.Time, system domain.SystemTelemetry, inference domain.InferenceTelemetry) time.Time {
	timestamp := laterTelemetryTime(now, snapshotAt, inference.UpdatedAt)
	for _, at := range systemTelemetryMetricTimes(system) {
		timestamp = laterTelemetryTime(timestamp, at)
	}
	for _, at := range inferenceTelemetryMetricTimes(inference) {
		timestamp = laterTelemetryTime(timestamp, at)
	}
	return timestamp
}

func inferenceTelemetryTimestamp(now time.Time, inference domain.InferenceTelemetry) time.Time {
	timestamp := laterTelemetryTime(now, inference.UpdatedAt)
	for _, at := range inferenceTelemetryMetricTimes(inference) {
		timestamp = laterTelemetryTime(timestamp, at)
	}
	return timestamp
}

func inferenceTelemetryMetricTimes(inference domain.InferenceTelemetry) []time.Time {
	return []time.Time{
		inference.RequestedTokens.UpdatedAt, inference.PromptTokens.UpdatedAt,
		inference.OutputTokens.UpdatedAt, inference.TotalTokens.UpdatedAt,
		inference.CachedPromptTokens.UpdatedAt, inference.PromptTokensPerSecond.UpdatedAt,
		inference.OutputTokensPerSecond.UpdatedAt, inference.TTFTMS.UpdatedAt,
		inference.PromptMS.UpdatedAt, inference.GenerationMS.UpdatedAt,
		inference.ContextTokens.UpdatedAt, inference.ContextWindow.UpdatedAt,
	}
}

func systemTelemetryMetricTimes(system domain.SystemTelemetry) []time.Time {
	result := []time.Time{
		system.Metadata.UptimeSeconds.UpdatedAt, system.CPUUsage.UpdatedAt,
		system.CPUTemperature.UpdatedAt, system.RAMUsed.UpdatedAt,
		system.RAMFree.UpdatedAt, system.RAMTotal.UpdatedAt,
		system.AgentRSS.UpdatedAt, system.LlamaServerRSS.UpdatedAt,
		system.PowerSource.UpdatedAt, system.PowerWatts.UpdatedAt,
		system.BatteryPercent.UpdatedAt, system.BatteryCharging.UpdatedAt,
		system.LlamaHealth.UpdatedAt, system.LlamaSlotsActive.UpdatedAt,
		system.LlamaSlotsTotal.UpdatedAt,
	}
	for _, gpu := range system.GPUs {
		result = append(result, gpu.Name.UpdatedAt, gpu.Utilization.UpdatedAt, gpu.Temperature.UpdatedAt,
			gpu.VRAMUsed.UpdatedAt, gpu.VRAMFree.UpdatedAt, gpu.VRAMTotal.UpdatedAt, gpu.PowerWatts.UpdatedAt)
	}
	return result
}

func laterTelemetryTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}

type legacyResourceStatusMonitor struct {
	resources.Monitor
}

func (monitor legacyResourceStatusMonitor) LastSnapshot() resources.Snapshot {
	snapshot := monitor.Monitor.LastSnapshot()
	snapshot.System = nil
	snapshot.Capabilities = nil
	snapshot.StaleAfterMS = nil
	return snapshot
}
