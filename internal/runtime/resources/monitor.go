package resources

import (
	"context"
	"errors"
	"sync"
)

type Provider interface {
	Snapshot(context.Context) (Snapshot, error)
}

type Monitor interface {
	Check(context.Context) (Snapshot, *Violation, error)
	LastSnapshot() Snapshot
}

type ResourceMonitor struct {
	provider Provider
	policy   Policy
	mu       sync.RWMutex
	last     Snapshot
}

func NewMonitor(provider Provider, policy Policy) (*ResourceMonitor, error) {
	if provider == nil {
		return nil, errors.New("resource provider required")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &ResourceMonitor{provider: provider, policy: policy, last: Snapshot{PowerSource: PowerUnknown}}, nil
}

func (m *ResourceMonitor) Check(ctx context.Context) (Snapshot, *Violation, error) {
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
	s.Warnings = append([]string{}, s.Warnings...)
	return s
}
