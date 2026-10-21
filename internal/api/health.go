package api

import (
	"context"
	"time"
)

func (s *Server) SetWorkerHeartbeat(probe func(context.Context) (time.Time, error)) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.workerHeartbeat = probe
}

func (s *Server) workerHealth(ctx context.Context) map[string]any {
	result := map[string]any{"status": "absent", "busy": false}
	if s.worker == nil {
		return result
	}
	result["status"] = "unknown"
	if worker, ok := s.worker.(interface{ Busy() bool }); ok {
		result["busy"] = worker.Busy()
	}
	s.healthMu.RLock()
	probe := s.workerHeartbeat
	s.healthMu.RUnlock()
	if probe == nil {
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	at, err := probe(probeCtx)
	if err != nil || at.IsZero() {
		return result
	}
	age := time.Since(at)
	if age < 0 {
		return result
	}
	result["heartbeat_at"] = at.UTC()
	result["heartbeat_age_seconds"] = age.Seconds()
	result["status"] = "stale"
	if age <= 30*time.Second {
		result["status"] = "fresh"
	}
	return result
}
