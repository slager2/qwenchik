package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/keepawake"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

var errResourcePause = errors.New("execution interrupted by resource guard")

type resourcePauseRepository interface {
	PauseTaskForResource(context.Context, string, string, time.Time, any) (domain.Task, error)
	ResourcePausedTasks(context.Context, time.Time, int) ([]domain.Task, error)
	ResolveResourceProbe(context.Context, string, bool, string, time.Time, int) (domain.Task, error)
}

type guardedRunKey struct{}

type guardedRun struct {
	worker    *Worker
	cancel    context.CancelCauseFunc
	mu        sync.Mutex
	checkMu   sync.Mutex
	reason    string
	checkedAt time.Time
}

func (g *guardedRun) stop(reason string) {
	g.mu.Lock()
	if g.reason == "" {
		g.reason = reason
	}
	g.mu.Unlock()
	g.cancel(errResourcePause)
}

func (g *guardedRun) pauseReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason
}

func (g *guardedRun) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.worker.options.ResourceMonitor == nil || !g.worker.options.ResourcePolicy.Enabled {
		return nil
	}
	g.checkMu.Lock()
	defer g.checkMu.Unlock()
	if time.Since(g.checkedAt) < time.Second {
		return nil
	}
	_, violation, err := g.worker.options.ResourceMonitor.Check(ctx)
	g.checkedAt = time.Now()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		g.stop("telemetry_unavailable")
		return errResourcePause
	}
	if violation != nil {
		g.stop(violation.Code)
		return errResourcePause
	}
	return nil
}

func checkRunResources(ctx context.Context) error {
	if guard, ok := ctx.Value(guardedRunKey{}).(*guardedRun); ok {
		return guard.check(ctx)
	}
	return ctx.Err()
}

func (w *Worker) guardRun(ctx context.Context, task domain.Task) (context.Context, *guardedRun, func()) {
	guardCtx, cancel := context.WithCancelCause(ctx)
	guard := &guardedRun{worker: w, cancel: cancel}
	guardCtx = context.WithValue(guardCtx, guardedRunKey{}, guard)
	done := make(chan struct{})
	interval := w.options.ResourcePolicy.CheckInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if task.Lease != nil {
		leaseInterval := time.Until(task.Lease.ExpiresAt) / 3
		if leaseInterval > 0 && leaseInterval < interval {
			interval = leaseInterval
		}
	}
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-guardCtx.Done():
				return
			case <-ticker.C:
				if task.Lease != nil {
					if repo, ok := w.repo.(interface {
						RenewTaskLease(context.Context, string) error
					}); ok {
						if err := repo.RenewTaskLease(guardCtx, task.ID); err != nil {
							cancel(sqlite.ErrLeaseLost)
							return
						}
					}
				}
				if err := guard.check(guardCtx); err != nil {
					return
				}
			}
		}
	}()
	return guardCtx, guard, func() { cancel(nil); <-done }
}

func modelResourceReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return ""
	}
	switch {
	case errors.Is(err, ErrRecoveryRequired), errors.Is(err, sqlite.ErrResearchBudgetExceeded):
		return ""
	case errors.Is(err, resources.ErrModelOOM):
		return "model_oom"
	case errors.Is(err, resources.ErrModelResource):
		return "model_resource"
	case errors.Is(err, llm.ErrModelTimeout):
		return "model_timeout"
	}
	if errors.Is(err, errLLMUnavailable) {
		var network net.Error
		var response interface{ HTTPStatusCode() int }
		if errors.As(err, &network) || errors.As(err, &response) && response.HTTPStatusCode() >= 500 && response.HTTPStatusCode() < 600 {
			return "llama_unhealthy"
		}
	}
	return ""
}

func (w *Worker) persistResourcePause(ctx context.Context, task domain.Task, reason string, causes ...error) error {
	repo, ok := w.repo.(resourcePauseRepository)
	if !ok {
		return errResourcePause
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if len(causes) > 0 {
		var failure *research.FetchFailure
		if errors.As(causes[0], &failure) {
			state, err := w.currentCheckpoint(writeCtx, task.ID)
			if err != nil {
				return err
			}
			if state.PendingTool == "fetch_url" && state.CurrentStep != "" {
				var args struct {
					URL string `json:"url"`
				}
				if json.Unmarshal(state.PendingArguments, &args) == nil && args.URL != "" {
					code := failure.Code
					if !sourceFallbackCode(code) {
						code = "fetch_timeout"
					}
					if err := w.recordSourceFailure(writeCtx, task.ID, task.Stage, state.CurrentStep, args.URL, failure, code, false, false, 0, &state); err != nil {
						return err
					}
				}
			}
		}
	}
	_, err := repo.PauseTaskForResource(writeCtx, task.ID, reason, time.Now().Add(w.options.ResourcePolicy.Backoff(task.ResourceProbeAttempts+1)), nil)
	return err
}

func (w *Worker) probeResourcePauses(ctx context.Context) {
	repo, ok := w.repo.(resourcePauseRepository)
	if !ok || w.options.ResourceMonitor == nil || !w.options.ResourcePolicy.Enabled {
		return
	}
	tasks, err := repo.ResourcePausedTasks(ctx, time.Now(), 10)
	if err != nil {
		return
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		_, violation, probeErr := w.options.ResourceMonitor.Check(ctx)
		healthy := probeErr == nil && violation == nil
		reason := "telemetry_unavailable"
		if violation != nil {
			reason = violation.Code
		}
		next := time.Now().Add(w.options.ResourcePolicy.Backoff(task.ResourceProbeAttempts + 2))
		if _, err := repo.ResolveResourceProbe(ctx, task.ID, healthy, reason, next, w.options.ResourcePolicy.MaxResumeAttempts); err == nil && healthy {
			w.Wake()
		}
	}
}

func (w *Worker) acquireSleep(ctx context.Context, task domain.Task) func() {
	wanted := w.options.KeepAwake
	if repo, ok := w.repo.(interface {
		GetResearchSession(context.Context, string) (domain.ResearchSession, error)
	}); ok {
		if session, err := repo.GetResearchSession(ctx, task.ID); err == nil {
			wanted = wanted || session.Policy.KeepAwake
		}
	}
	w.runtimeMu.Lock()
	w.sleepWanted = wanted
	w.sleepStatus = keepawake.Status{Provider: "none", PowerSource: keepawake.PowerUnknown}
	w.runtimeMu.Unlock()
	if !wanted {
		return func() {}
	}
	provider := w.options.SleepProvider
	if provider == nil {
		provider = keepawake.NewProvider()
	}
	release, status, err := provider.Acquire(ctx, task.ID)
	if err != nil {
		status.Active = false
	}
	w.runtimeMu.Lock()
	w.sleepStatus = status
	w.runtimeMu.Unlock()
	return func() {
		if release != nil {
			release()
		}
		w.runtimeMu.Lock()
		w.sleepStatus.Active = false
		w.runtimeMu.Unlock()
	}
}

func (w *Worker) RuntimeStatus() map[string]any {
	w.runtimeMu.RLock()
	status, wanted := w.sleepStatus, w.sleepWanted
	w.runtimeMu.RUnlock()
	result := map[string]any{"status": "ok", "enabled": w.options.ResourcePolicy.Enabled, "sleep_policy": "disabled", "keep_awake": wanted, "sleep": status, "max_resume_attempts": w.options.ResourcePolicy.MaxResumeAttempts}
	if wanted {
		result["sleep_policy"] = "keep_awake"
	}
	if w.options.ResourceMonitor != nil {
		result["resources"] = w.options.ResourceMonitor.LastSnapshot()
	} else {
		result["status"] = "unknown"
	}
	return result
}
