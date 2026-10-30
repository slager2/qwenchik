package resources

import (
	"sync/atomic"
	"time"
)

// TelemetryStats is a bounded, process-local view of host telemetry collection.
// It contains counters only; no raw telemetry, command output or process names
// are retained. Stats is intended for diagnostics and tests, not admission.
type TelemetryStats struct {
	Snapshots            uint64        `json:"snapshots"`
	SnapshotErrors       uint64        `json:"snapshot_errors"`
	LastSnapshotDuration time.Duration `json:"last_snapshot_duration"`
	PowerCacheHits       uint64        `json:"power_cache_hits"`
	PowerCacheMisses     uint64        `json:"power_cache_misses"`
	ModelRSSCacheHits    uint64        `json:"model_rss_cache_hits"`
	ModelRSSCacheMisses  uint64        `json:"model_rss_cache_misses"`
	ModelRSSScans        uint64        `json:"model_rss_scans"`
	GPUProbeAttempts     uint64        `json:"gpu_probe_attempts"`
	GPUProbeFailures     uint64        `json:"gpu_probe_failures"`
	GPUCacheHits         uint64        `json:"gpu_cache_hits"`
	GPUCacheMisses       uint64        `json:"gpu_cache_misses"`
}

type telemetryCounters struct {
	snapshots            atomic.Uint64
	snapshotErrors       atomic.Uint64
	lastSnapshotDuration atomic.Int64
	powerCacheHits       atomic.Uint64
	powerCacheMisses     atomic.Uint64
	modelRSSCacheHits    atomic.Uint64
	modelRSSCacheMisses  atomic.Uint64
	modelRSSScans        atomic.Uint64
	gpuProbeAttempts     atomic.Uint64
	gpuProbeFailures     atomic.Uint64
	gpuCacheHits         atomic.Uint64
	gpuCacheMisses       atomic.Uint64
}

// Stats returns a consistent-enough point-in-time copy of collection counters.
// Counters are independent atomics, so values can be from adjacent samples;
// this is deliberate because diagnostics must never block telemetry collection.
func (p *HostProvider) Stats() TelemetryStats {
	if p == nil {
		return TelemetryStats{}
	}
	return TelemetryStats{
		Snapshots:            p.stats.snapshots.Load(),
		SnapshotErrors:       p.stats.snapshotErrors.Load(),
		LastSnapshotDuration: time.Duration(p.stats.lastSnapshotDuration.Load()),
		PowerCacheHits:       p.stats.powerCacheHits.Load(),
		PowerCacheMisses:     p.stats.powerCacheMisses.Load(),
		ModelRSSCacheHits:    p.stats.modelRSSCacheHits.Load(),
		ModelRSSCacheMisses:  p.stats.modelRSSCacheMisses.Load(),
		ModelRSSScans:        p.stats.modelRSSScans.Load(),
		GPUProbeAttempts:     p.stats.gpuProbeAttempts.Load(),
		GPUProbeFailures:     p.stats.gpuProbeFailures.Load(),
		GPUCacheHits:         p.stats.gpuCacheHits.Load(),
		GPUCacheMisses:       p.stats.gpuCacheMisses.Load(),
	}
}
