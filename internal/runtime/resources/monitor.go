package resources

import (
	"context"
	"errors"
	"sync"

	"github.com/slager2/qwenchik/internal/domain"
)

type Provider interface {
	Snapshot(context.Context) (Snapshot, error)
}

type Monitor interface {
	Check(context.Context) (Snapshot, *Violation, error)
	LastSnapshot() Snapshot
}

type ResourceMonitor struct {
	provider  Provider
	policy    Policy
	checkGate chan struct{}
	mu        sync.RWMutex
	last      Snapshot
}

func NewMonitor(provider Provider, policy Policy) (*ResourceMonitor, error) {
	if provider == nil {
		return nil, errors.New("resource provider required")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &ResourceMonitor{provider: provider, policy: policy, checkGate: make(chan struct{}, 1), last: Snapshot{PowerSource: PowerUnknown}}, nil
}

func (m *ResourceMonitor) Check(ctx context.Context) (Snapshot, *Violation, error) {
	select {
	case m.checkGate <- struct{}{}:
		defer func() { <-m.checkGate }()
	case <-ctx.Done():
		return Snapshot{PowerSource: PowerUnknown}, nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{PowerSource: PowerUnknown}, nil, err
	}
	s, err := m.provider.Snapshot(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	s = clone(s)
	thresholdWarnings(&s, m.policy)
	m.mu.Lock()
	m.last = clone(s)
	m.mu.Unlock()
	if err != nil {
		return s, nil, err
	}
	return s, Check(s, m.policy), nil
}

func (m *ResourceMonitor) LastSnapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return clone(m.last)
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func clone(s Snapshot) Snapshot {
	if s.PowerSource != PowerAC && s.PowerSource != PowerBattery {
		s.PowerSource = PowerUnknown
	}
	s.FreeRAMBytes = copyPtr(s.FreeRAMBytes)
	s.RSSBytes = copyPtr(s.RSSBytes)
	s.HeapAllocBytes = copyPtr(s.HeapAllocBytes)
	s.GPUFreeVRAMMB = copyPtr(s.GPUFreeVRAMMB)
	s.GPUTempC = copyPtr(s.GPUTempC)
	s.LlamaHealthy = copyPtr(s.LlamaHealthy)
	s.LlamaHealthLatency = copyPtr(s.LlamaHealthLatency)
	s.System = cloneSystemTelemetry(s.System)
	if s.Capabilities != nil {
		capabilities := *s.Capabilities
		s.Capabilities = &capabilities
	}
	s.StaleAfterMS = copyPtr(s.StaleAfterMS)
	s.Warnings = append([]string{}, s.Warnings...)
	return s
}

func cloneSystemTelemetry(system *domain.SystemTelemetry) *domain.SystemTelemetry {
	if system == nil {
		return nil
	}
	copy := *system
	copy.Metadata.UptimeSeconds = cloneMetric(system.Metadata.UptimeSeconds)
	copy.CPUUsage = cloneMetric(system.CPUUsage)
	copy.CPUTemperature = cloneMetric(system.CPUTemperature)
	copy.RAMUsed = cloneMetric(system.RAMUsed)
	copy.RAMFree = cloneMetric(system.RAMFree)
	copy.RAMTotal = cloneMetric(system.RAMTotal)
	copy.AgentRSS = cloneMetric(system.AgentRSS)
	copy.LlamaServerRSS = cloneMetric(system.LlamaServerRSS)
	copy.PowerSource = cloneMetric(system.PowerSource)
	copy.PowerWatts = cloneMetric(system.PowerWatts)
	copy.BatteryPercent = cloneMetric(system.BatteryPercent)
	copy.BatteryCharging = cloneMetric(system.BatteryCharging)
	copy.LlamaHealth = cloneMetric(system.LlamaHealth)
	copy.LlamaSlotsActive = cloneMetric(system.LlamaSlotsActive)
	copy.LlamaSlotsTotal = cloneMetric(system.LlamaSlotsTotal)
	copy.GPUs = make([]domain.GPUTelemetry, len(system.GPUs))
	for i, gpu := range system.GPUs {
		copy.GPUs[i] = domain.GPUTelemetry{
			Name:        cloneMetric(gpu.Name),
			Utilization: cloneMetric(gpu.Utilization),
			Temperature: cloneMetric(gpu.Temperature),
			VRAMUsed:    cloneMetric(gpu.VRAMUsed),
			VRAMFree:    cloneMetric(gpu.VRAMFree),
			VRAMTotal:   cloneMetric(gpu.VRAMTotal),
			PowerWatts:  cloneMetric(gpu.PowerWatts),
		}
	}
	return &copy
}

func cloneMetric[T domain.MetricValue](metric domain.Metric[T]) domain.Metric[T] {
	metric.Value = copyPtr(metric.Value)
	return metric
}
