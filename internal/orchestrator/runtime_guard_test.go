package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/llm/openai"
	"github.com/slager2/qwenchik/internal/runtime/keepawake"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

type runtimeGuardMonitor struct {
	mu          sync.Mutex
	policy      resources.Policy
	lowRAM      bool
	probeErr    error
	calls       int
	last        resources.Snapshot
	sample      *resources.Snapshot
	beforeCheck func(context.Context)
}

func TestRuntimeGuardClassifiesOnlyModelOutagesAsResourceFailures(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{errors.Join(errLLMUnavailable, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), "llama_unhealthy"},
		{errors.Join(errLLMUnavailable, &openai.HTTPError{StatusCode: 503}), "llama_unhealthy"},
		{errors.Join(errLLMUnavailable, &openai.HTTPError{StatusCode: 401}), ""},
		{&net.DNSError{Err: "fetch DNS failed"}, ""},
		{errors.Join(errLLMUnavailable, llm.ErrModelTimeout, context.Canceled), ""},
	} {
		if got := modelResourceReason(test.err); got != test.want {
			t.Fatalf("reason=%q want=%q error=%v", got, test.want, test.err)
		}
	}
}

func runtimeGuardPtr[T any](v T) *T { return &v }

func (m *runtimeGuardMonitor) Check(ctx context.Context) (resources.Snapshot, *resources.Violation, error) {
	m.mu.Lock()
	before := m.beforeCheck
	m.mu.Unlock()
	if before != nil {
		before(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return resources.Snapshot{}, nil, err
	}
	m.calls++
	var s resources.Snapshot
	if m.sample != nil {
		s = runtimeGuardCopySnapshot(*m.sample)
	} else {
		ram := int64(2 << 30)
		staleAfter := int64(time.Minute / time.Millisecond)
		if m.lowRAM {
			ram = 1
		}
		s = resources.Snapshot{
			SnapshotAt: time.Now().UTC(), FreeRAMBytes: &ram,
			RSSBytes: runtimeGuardPtr(int64(64 << 20)), HeapAllocBytes: runtimeGuardPtr(uint64(32 << 20)),
			LlamaHealthy: runtimeGuardPtr(true), PowerSource: resources.PowerUnknown,
			StaleAfterMS: &staleAfter,
			Warnings:     []string{"fake GPU telemetry unknown"},
		}
	}
	m.last = s
	return runtimeGuardCopySnapshot(s), resources.Check(s, m.policy), m.probeErr
}

func runtimeGuardCopySnapshot(s resources.Snapshot) resources.Snapshot {
	if s.FreeRAMBytes != nil {
		s.FreeRAMBytes = runtimeGuardPtr(*s.FreeRAMBytes)
	}
	if s.RSSBytes != nil {
		s.RSSBytes = runtimeGuardPtr(*s.RSSBytes)
	}
	if s.GPUFreeVRAMMB != nil {
		s.GPUFreeVRAMMB = runtimeGuardPtr(*s.GPUFreeVRAMMB)
	}
	if s.GPUTempC != nil {
		s.GPUTempC = runtimeGuardPtr(*s.GPUTempC)
	}
	if s.HeapAllocBytes != nil {
		s.HeapAllocBytes = runtimeGuardPtr(*s.HeapAllocBytes)
	}
	if s.LlamaHealthy != nil {
		s.LlamaHealthy = runtimeGuardPtr(*s.LlamaHealthy)
	}
	if s.StaleAfterMS != nil {
		s.StaleAfterMS = runtimeGuardPtr(*s.StaleAfterMS)
	}
	if s.StaleAfterMS != nil {
		s.StaleAfterMS = runtimeGuardPtr(*s.StaleAfterMS)
	}
	s.Warnings = append([]string(nil), s.Warnings...)
	return s
}

func (m *runtimeGuardMonitor) LastSnapshot() resources.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return runtimeGuardCopySnapshot(m.last)
}

func (m *runtimeGuardMonitor) setLowRAM(low bool) { m.mu.Lock(); m.lowRAM = low; m.mu.Unlock() }
func (m *runtimeGuardMonitor) count() int         { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }
func (m *runtimeGuardMonitor) setSample(s resources.Snapshot) {
	m.mu.Lock()
	if s.SnapshotAt.IsZero() {
		s.SnapshotAt = time.Now().UTC()
	}
	if s.StaleAfterMS == nil {
		staleAfter := int64(time.Minute / time.Millisecond)
		s.StaleAfterMS = &staleAfter
	}
	sample := runtimeGuardCopySnapshot(s)
	m.sample = &sample
	m.mu.Unlock()
}

type runtimeGuardSleep struct {
	mu          sync.Mutex
	unavailable bool
	runIDs      []string
	releases    int
}

func (p *runtimeGuardSleep) Acquire(ctx context.Context, id string) (func(), keepawake.Status, error) {
	p.mu.Lock()
	p.runIDs = append(p.runIDs, id)
	unavailable := p.unavailable
	p.mu.Unlock()
	status := keepawake.Status{Provider: "fake-inhibitor", Available: !unavailable, Active: !unavailable, PowerSource: keepawake.PowerUnknown}
	var err error
	if unavailable {
		status.FailureReason = "fake acquisition denied"
		err = keepawake.ErrInhibitorUnavailable
	}
	if ctx.Err() != nil {
		status.Active = false
		err = ctx.Err()
	}
	return func() { p.mu.Lock(); p.releases++; p.mu.Unlock() }, status, err
}

func (p *runtimeGuardSleep) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.runIDs), p.releases
}

func runtimeGuardOptions() (Options, *runtimeGuardMonitor, *runtimeGuardSleep) {
	p := resources.DefaultPolicy()
	p.CheckInterval = 10 * time.Millisecond
	p.MaxResumeAttempts = 3
	m := &runtimeGuardMonitor{policy: p}
	s := &runtimeGuardSleep{}
	return Options{ResourcePolicy: p, ResourceMonitor: m, SleepProvider: s}, m, s
}

func runtimeGuardTask(t *testing.T, f *researchRunFixture) domain.Task {
	t.Helper()
	task, err := f.store.GetTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func runtimeGuardLeaseContext(task domain.Task) context.Context {
	ctx := context.Background()
	if task.Lease != nil {
		ctx = domain.WithExecutionLease(ctx, *task.Lease)
	}
	return ctx
}

func runtimeGuardAssertPaused(t *testing.T, f *researchRunFixture, reason string) domain.Task {
	t.Helper()
	task := runtimeGuardTask(t, f)
	run, err := f.store.GetRun(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusPaused || task.PauseReason != "resource_guard" || task.ResourceReason != reason || task.ResumeAfter == nil || task.Lease != nil || task.TerminalOutcome != "" || run.CompletedAt != nil || run.Status != domain.StatusPaused {
		t.Fatalf("resource pause not durable: task=%+v run=%+v", task, run)
	}
	return task
}

func runtimeGuardMakeProbeDue(t *testing.T, f *researchRunFixture) {
	t.Helper()
	db, err := sql.Open("sqlite3", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE tasks SET resume_after=? WHERE id=? AND status='paused' AND pause_reason='resource_guard'`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("make probe due: rows=%d err=%v", n, err)
	}
}

func runtimeGuardPauseDue(t *testing.T, f *researchRunFixture) {
	runtimeGuardPauseForReason(t, f, "low_ram")
}

func runtimeGuardPauseForReason(t *testing.T, f *researchRunFixture, reason string) {
	t.Helper()
	if _, err := f.store.PauseTaskForResource(runtimeGuardLeaseContext(f.task), f.task.ID, reason, time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}
}

func runtimeGuardEvents(t *testing.T, f *researchRunFixture) []domain.Event {
	t.Helper()
	events, err := f.store.EventsAfter(context.Background(), f.task.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func runtimeGuardStartedInvocations(t *testing.T, f *researchRunFixture) []domain.Invocation {
	t.Helper()
	var result []domain.Invocation
	for _, event := range runtimeGuardEvents(t, f) {
		if event.Type != "invocation.started" {
			continue
		}
		var payload struct {
			Invocation domain.Invocation `json:"invocation"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		result = append(result, payload.Invocation)
	}
	return result
}

func runtimeGuardStart(t *testing.T, w *Worker, task domain.Task) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.runTask(ctx, task) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("runtime guard worker leaked after cancellation")
		}
	})
	return done
}

func runtimeGuardWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime guard worker did not stop")
	}
}

func runtimeGuardBlockingModel(partial string) (*researchRunModel, <-chan context.Context, chan<- struct{}) {
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	model := &researchRunModel{stream: func(ctx context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
		events := make(chan llm.ChatEvent, 1)
		if partial != "" {
			events <- llm.ChatEvent{Text: partial}
		}
		started <- ctx
		go func() {
			defer close(events)
			select {
			case <-ctx.Done():
				return
			case <-finish:
			}
			select {
			case events <- llm.ChatEvent{Text: "Hello!"}:
			case <-ctx.Done():
			}
		}()
		return events, nil
	}}
	return model, started, finish
}

func runtimeGuardAwaitStream(t *testing.T, started <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-started:
		return ctx
	case <-time.After(3 * time.Second):
		t.Fatal("model stream not reached")
		return nil
	}
}

func TestRuntimeGuardLowRAMStopsBeforeLLMAndRetainsCheckpoint(t *testing.T) {
	f := newResearchRunStore(t)
	if err := f.store.EnableWorkerLeases("low-ram-worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "Hello", nil)
	ctx := runtimeGuardLeaseContext(f.task)
	prior, err := f.store.SaveCheckpoint(ctx, f.task.ID, 1, checkpointState{PlanVersion: 1, Summary: "retained partial work", CompletedSteps: []string{}, ToolResults: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	before := researchRunSession(t, f.store, f.task.ID)
	opts, monitor, sleep := runtimeGuardOptions()
	opts.KeepAwake = true
	monitor.setLowRAM(true)
	model := &researchRunModel{}
	w := New(f.store, model, tools.NewRegistry(), opts)
	w.runTask(context.Background(), f.task)
	runtimeGuardAssertPaused(t, f, "low_ram")
	cp, err := f.store.LatestCheckpoint(context.Background(), f.task.ID)
	if err != nil || cp.ParentID != prior.ID || cp.State.(map[string]any)["summary"] != "retained partial work" {
		t.Fatalf("checkpoint lost: %+v %v", cp, err)
	}
	if len(model.calls()) != 0 || len(runtimeGuardStartedInvocations(t, f)) != 0 || monitor.count() != 1 {
		t.Fatal("low RAM reached or reserved model work")
	}
	if calls, releases := sleep.counts(); calls != 0 || releases != 0 {
		t.Fatalf("sleep acquired before resource clearance: %d/%d", calls, releases)
	}
	after := researchRunSession(t, f.store, f.task.ID)
	if before.Counters != after.Counters || !before.DeadlineAt.Equal(after.DeadlineAt) {
		t.Fatal("pause reset counters or deadline")
	}
	if _, err := f.store.ReserveResearchLLMCall(context.Background(), f.task.ID); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("paused task accepted reservation: %v", err)
	}
}

func TestRuntimeGuardHealthyDueProbeResumesAfterRestartWithoutReset(t *testing.T) {
	f := newResearchRunStore(t)
	f.submit(t, "Hello", nil)
	ctx := context.Background()
	if _, err := f.store.ReserveResearchLLMCall(ctx, f.task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReserveResearchHTTPAttempt(ctx, f.task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveCheckpoint(ctx, f.task.ID, 1, checkpointState{PlanVersion: 1, Summary: "resume pointer", CompletedSteps: []string{}, ToolResults: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	before := researchRunSession(t, f.store, f.task.ID)
	runtimeGuardPauseDue(t, f)
	cp, err := f.store.LatestCheckpoint(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	opts, monitor, _ := runtimeGuardOptions()
	model := &researchRunModel{}
	w := New(f.store, model, tools.NewRegistry(), opts)
	w.probeResourcePauses(ctx)
	task := runtimeGuardTask(t, f)
	if task.Status != domain.StatusQueued || task.PauseReason != "" || task.ResourceProbeAttempts != 1 || task.ResumeAfter != nil || task.Lease != nil {
		t.Fatalf("healthy resume: %+v", task)
	}
	after := researchRunSession(t, f.store, f.task.ID)
	if before.Counters != after.Counters || !before.DeadlineAt.Equal(after.DeadlineAt) || before.Policy != after.Policy || before.ModelSnapshot.Model != after.ModelSnapshot.Model {
		t.Fatal("resume reset immutable state or consumed counters")
	}
	restored, err := f.store.LatestCheckpoint(ctx, f.task.ID)
	if err != nil || restored.ID != cp.ID || !reflect.DeepEqual(restored.State, cp.State) {
		t.Fatalf("resume replaced checkpoint: %+v %v", restored, err)
	}
	w.probeResourcePauses(ctx)
	if monitor.count() != 1 || len(model.calls()) != 0 {
		t.Fatal("queued task was probed or executed again")
	}
}

func TestRuntimeGuardResumesOnlyAfterRecoveryMarginClears(t *testing.T) {
	f := newResearchRunStore(t)
	f.submit(t, "Hello", nil)
	runtimeGuardPauseDue(t, f)
	opts, monitor, _ := runtimeGuardOptions()
	minimum := opts.ResourcePolicy.MinFreeRAMBytes
	margin := minimum/100*10 + (minimum%100*10+99)/100
	monitor.setSample(resources.Snapshot{
		FreeRAMBytes: ptrForRuntimeGuard(minimum + margin - 1),
		RSSBytes:     ptrForRuntimeGuard(int64(64 << 20)),
		LlamaHealthy: ptrForRuntimeGuard(true),
	})
	w := New(f.store, &researchRunModel{}, tools.NewRegistry(), opts)
	w.probeResourcePauses(context.Background())
	task := runtimeGuardTask(t, f)
	if task.Status != domain.StatusPaused || task.ResourceProbeAttempts != 1 || task.ResourceReason != "low_ram" {
		t.Fatalf("task resumed before RAM recovery margin cleared: %+v", task)
	}

	runtimeGuardMakeProbeDue(t, f)
	monitor.setSample(resources.Snapshot{
		FreeRAMBytes: ptrForRuntimeGuard(minimum + margin),
		RSSBytes:     ptrForRuntimeGuard(int64(64 << 20)),
		LlamaHealthy: ptrForRuntimeGuard(true),
	})
	w.probeResourcePauses(context.Background())
	task = runtimeGuardTask(t, f)
	if task.Status != domain.StatusQueued || task.ResourceProbeAttempts != 2 || task.PauseReason != "" {
		t.Fatalf("task did not resume at RAM recovery boundary: %+v", task)
	}
	resumed := 0
	for _, event := range runtimeGuardEvents(t, f) {
		if event.Type == "run.resumed" {
			resumed++
		}
	}
	if resumed != 1 || monitor.count() != 2 {
		t.Fatalf("resume count=%d probes=%d, want one clearance resume after two probes", resumed, monitor.count())
	}
}

func TestRuntimeGuardTelemetryPauseRequiresObservableEnabledMetrics(t *testing.T) {
	f := newResearchRunStore(t)
	f.submit(t, "Hello", nil)
	runtimeGuardPauseForReason(t, f, "telemetry_unavailable")
	opts, monitor, _ := runtimeGuardOptions()
	monitor.setSample(resources.Snapshot{
		FreeRAMBytes: ptrForRuntimeGuard(opts.ResourcePolicy.MinFreeRAMBytes * 2),
		RSSBytes:     ptrForRuntimeGuard(int64(64 << 20)),
		GPUTempC:     ptrForRuntimeGuard(70.0),
		LlamaHealthy: ptrForRuntimeGuard(true),
	})
	w := New(f.store, &researchRunModel{}, tools.NewRegistry(), opts)
	w.probeResourcePauses(context.Background())
	task := runtimeGuardTask(t, f)
	if task.Status != domain.StatusPaused || task.ResourceProbeAttempts != 1 || task.ResourceReason != "telemetry_unavailable" {
		t.Fatalf("telemetry pause resumed without VRAM observation: %+v", task)
	}

	runtimeGuardMakeProbeDue(t, f)
	monitor.setSample(resources.Snapshot{
		FreeRAMBytes:  ptrForRuntimeGuard(opts.ResourcePolicy.MinFreeRAMBytes * 2),
		RSSBytes:      ptrForRuntimeGuard(int64(64 << 20)),
		GPUFreeVRAMMB: ptrForRuntimeGuard(opts.ResourcePolicy.MinFreeVRAMMB * 2),
		GPUTempC:      ptrForRuntimeGuard(70.0),
		LlamaHealthy:  ptrForRuntimeGuard(true),
	})
	w.probeResourcePauses(context.Background())
	if task = runtimeGuardTask(t, f); task.Status != domain.StatusQueued || task.ResourceProbeAttempts != 2 {
		t.Fatalf("fully observable healthy telemetry did not resume: %+v", task)
	}
}

func ptrForRuntimeGuard[T any](value T) *T { return &value }

func TestRuntimeGuardFailedProbesBackOffAndStopAtFiniteCap(t *testing.T) {
	f := newResearchRunStore(t)
	f.submit(t, "Hello", nil)
	runtimeGuardPauseDue(t, f)
	before := researchRunSession(t, f.store, f.task.ID)
	opts, monitor, _ := runtimeGuardOptions()
	monitor.setLowRAM(true)
	opts.ResourcePolicy.BackoffBase, opts.ResourcePolicy.BackoffMax = time.Hour, time.Hour
	if err := opts.ResourcePolicy.Validate(); err != nil {
		t.Fatal(err)
	}
	model := &researchRunModel{}
	w := New(f.store, model, tools.NewRegistry(), opts)
	for attempt := 1; attempt <= opts.ResourcePolicy.MaxResumeAttempts; attempt++ {
		if attempt > 1 {
			runtimeGuardMakeProbeDue(t, f)
		}
		w.probeResourcePauses(context.Background())
		task := runtimeGuardTask(t, f)
		if task.ResourceProbeAttempts != attempt || monitor.count() != attempt {
			t.Fatalf("attempt %d: task=%+v checks=%d", attempt, task, monitor.count())
		}
		if attempt < opts.ResourcePolicy.MaxResumeAttempts {
			if task.Status != domain.StatusPaused || task.ResumeAfter == nil || !task.ResumeAfter.After(time.Now()) {
				t.Fatalf("missing backoff: %+v", task)
			}
			for i := 0; i < 10; i++ {
				w.probeResourcePauses(context.Background())
			}
			if monitor.count() != attempt || runtimeGuardTask(t, f).ResourceProbeAttempts != attempt {
				t.Fatal("future probe was retried in a busy loop")
			}
		}
	}
	task := runtimeGuardTask(t, f)
	run, err := f.store.GetRun(context.Background(), task.ID)
	if err != nil || task.Status != domain.StatusFailed || task.TerminalOutcome != "blocked" || run.ErrorCode != "resource_guard_exhausted" || run.CompletedAt == nil {
		t.Fatalf("cap outcome: task=%+v run=%+v %v", task, run, err)
	}
	for i := 0; i < 10; i++ {
		w.probeResourcePauses(context.Background())
	}
	if monitor.count() != opts.ResourcePolicy.MaxResumeAttempts || len(model.calls()) != 0 || researchRunSession(t, f.store, task.ID).Counters != before.Counters {
		t.Fatal("terminal probe cap allowed further work")
	}
}

func TestRuntimeGuardManualPauseAndCancellationNeverResume(t *testing.T) {
	for _, mode := range []string{"manual_pause", "cancel_before_probe", "canceled_probe_context", "cancel_during_probe"} {
		t.Run(mode, func(t *testing.T) {
			f := newResearchRunStore(t)
			f.submit(t, "Hello", nil)
			opts, monitor, _ := runtimeGuardOptions()
			w := New(f.store, &researchRunModel{}, tools.NewRegistry(), opts)
			ctx := context.Background()
			want := domain.StatusPaused
			if mode == "manual_pause" {
				if _, err := f.store.TransitionTask(ctx, f.task.ID, domain.StatusPaused, nil, "", "user", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				runtimeGuardPauseDue(t, f)
			}
			if mode == "cancel_before_probe" {
				if _, err := f.store.TransitionTask(ctx, f.task.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
					t.Fatal(err)
				}
				want = domain.StatusCancelled
			}
			if mode == "canceled_probe_context" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "cancel_during_probe" {
				entered, proceed := make(chan struct{}), make(chan struct{})
				monitor.beforeCheck = func(ctx context.Context) {
					close(entered)
					select {
					case <-proceed:
					case <-ctx.Done():
					}
				}
				done := make(chan struct{})
				probeCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				go func() { defer close(done); w.probeResourcePauses(probeCtx) }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("due probe never reached monitor")
				}
				if _, err := f.store.TransitionTask(ctx, f.task.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
					t.Fatal(err)
				}
				close(proceed)
				runtimeGuardWait(t, done)
				want = domain.StatusCancelled
			} else {
				w.probeResourcePauses(ctx)
			}
			task := runtimeGuardTask(t, f)
			if task.Status != want || task.ResourceProbeAttempts != 0 {
				t.Fatalf("resurrected lifecycle command: %+v", task)
			}
			if mode != "cancel_during_probe" && monitor.count() != 0 {
				t.Fatal("ineligible task reached monitor")
			}
			for _, event := range runtimeGuardEvents(t, f) {
				if event.Type == "run.resumed" {
					t.Fatal("canceled/manual task emitted resume")
				}
			}
		})
	}
}

func TestRuntimeGuardTypedModelErrorsPauseStructuredCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
	}{
		{"oom", "model_oom", resources.ErrModelOOM},
		{"resource", "model_resource", resources.ErrModelResource},
		{"timeout", "model_timeout", llm.ErrModelTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResearchRunStore(t)
			if err := f.store.EnableWorkerLeases("model-error-worker", 30*time.Second); err != nil {
				t.Fatal(err)
			}
			f.submit(t, "Count words in the supplied text: one two three", nil)
			opts, _, _ := runtimeGuardOptions()
			model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, _ int, streaming bool) (string, error) {
				if streaming || len(request.JSONSchema) == 0 {
					t.Error("test did not reach structured Complete")
				}
				return "", errors.Join(errors.New("fake adapter error"), tc.err)
			}}
			w := New(f.store, model, tools.NewRegistry(), opts)
			w.runTask(context.Background(), f.task)
			runtimeGuardAssertPaused(t, f, tc.reason)
			if len(model.calls()) != 1 {
				t.Fatalf("resource error retried: %d calls", len(model.calls()))
			}
			started := runtimeGuardStartedInvocations(t, f)
			if len(started) != 1 || started[0].Kind != "llm.complete" {
				t.Fatalf("unexpected intent: %+v", started)
			}
			history, err := f.store.InvocationsForOperation(context.Background(), f.task.ID, started[0].OperationID)
			if err != nil || len(history) != 1 || history[0].Status != "failed" || history[0].ErrorCode != tc.reason {
				t.Fatalf("resource invocation outcome lost: %+v %v", history, err)
			}
			after := researchRunSession(t, f.store, f.task.ID)
			if after.Counters.LLMCalls != 1 || after.Counters.HTTPAttempts != 1 {
				t.Fatalf("resource reservation lost: %+v", after.Counters)
			}
		})
	}
}

func TestRuntimeGuardCriticResourceFailureCannotCompleteViaFallback(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
	}{{"oom", "model_oom", resources.ErrModelOOM}, {"timeout", "model_timeout", llm.ErrModelTimeout}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResearchRunStore(t)
			f.submit(t, "Research protocol architecture and limitations.", func(s *domain.ResearchSession) { s.ModelSnapshot.MaxOutputTokens = 4096; s.Budget.MaxLLMCalls = 24 })
			search, fetcher := &researchRunSearch{}, &researchRunFetcher{}
			base := integrationResearchModel()
			model := &researchRunModel{respond: func(_ context.Context, request llm.ChatRequest, index int, streaming bool) (string, error) {
				if index < 6 {
					return base.respond(request, index)
				}
				if index == 6 {
					if !streaming {
						t.Error("finalizer did not stream")
					}
					var input struct {
						ToolResults map[string]struct {
							EvidenceID string `json:"evidence_id"`
							Excerpt    string `json:"excerpt"`
						} `json:"tool_results"`
					}
					if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
						return "", err
					}
					var paragraphs []string
					for _, result := range input.ToolResults {
						paragraphs = append(paragraphs, result.Excerpt+" ["+result.EvidenceID+"]")
					}
					sort.Strings(paragraphs)
					encoded, err := json.Marshal(map[string]string{"answer": strings.Join(paragraphs, "\n\n")})
					return string(encoded), err
				}
				if index == 7 && !streaming && strings.Contains(request.Messages[0].Content, "citation support critic") {
					return "", tc.err
				}
				return "", errors.New("unexpected model call after critic resource error")
			}}
			opts, _, _ := runtimeGuardOptions()
			w := New(f.store, model, researchRunRegistry(t, f.store, search, fetcher), opts)
			w.runTask(context.Background(), f.task)
			runtimeGuardAssertPaused(t, f, tc.reason)
			if len(model.calls()) != 8 {
				t.Fatalf("critic retried or bypassed: %d calls", len(model.calls()))
			}
			for _, event := range runtimeGuardEvents(t, f) {
				if event.Type == "task.completed" || event.Type == "run.completed" || event.Type == "verification.updated" && strings.Contains(string(event.Payload), "fallback_excerpts") {
					t.Fatalf("resource error became fallback completion: %s %s", event.Type, event.Payload)
				}
			}
			if session := researchRunSession(t, f.store, f.task.ID); session.Counters.LLMCalls != 8 || session.Counters.Fetches != 2 || session.Counters.Evidence != 2 {
				t.Fatalf("critic pause lost collected work: %+v", session.Counters)
			}
		})
	}
}

func TestRuntimeGuardLeasedGreetingPropagatesContextAndCompletes(t *testing.T) {
	f := newResearchRunStore(t)
	if err := f.store.EnableWorkerLeases("greeting-worker", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "Hello", nil)
	if f.task.Lease == nil {
		t.Fatal("claim did not attach lease")
	}
	opts, _, _ := runtimeGuardOptions()
	opts.UseWorkerLeases = true
	model := &researchRunModel{respond: func(ctx context.Context, _ llm.ChatRequest, _ int, streaming bool) (string, error) {
		lease, ok := domain.ExecutionLeaseFromContext(ctx)
		if !ok || lease.Owner != f.task.Lease.Owner || lease.Epoch != f.task.Lease.Epoch || !streaming {
			t.Errorf("lease context lost: %+v present=%t streaming=%t", lease, ok, streaming)
		}
		return "Hello!", nil
	}}
	w := New(f.store, model, tools.NewRegistry(), opts)
	w.runTask(context.Background(), f.task)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	if len(model.calls()) != 1 || runtimeGuardTask(t, f).Lease != nil {
		t.Fatal("greeting retried or retained lease")
	}
	if got := chatTestMessage(t, f.store, f.turn).Content; got != "Hello!" {
		t.Fatalf("answer not committed: %q", got)
	}
}

func TestRuntimeGuardRenewsFiveSecondLeaseDuringSixSecondStream(t *testing.T) {
	f := newResearchRunStore(t)
	if err := f.store.EnableWorkerLeases("long-stream-worker", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "Hello", nil)
	if f.task.Lease == nil {
		t.Fatal("claim did not attach lease")
	}
	original := *f.task.Lease
	opts, monitor, _ := runtimeGuardOptions()
	opts.UseWorkerLeases = true
	model, started, finish := runtimeGuardBlockingModel("")
	w := New(f.store, model, tools.NewRegistry(), opts)
	done := runtimeGuardStart(t, w, f.task)
	streamCtx := runtimeGuardAwaitStream(t, started)
	if lease, ok := domain.ExecutionLeaseFromContext(streamCtx); !ok || lease.Owner != original.Owner || lease.Epoch != original.Epoch {
		t.Fatalf("model context lease: %+v %t", lease, ok)
	}
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	renewals := 0
	lastExpiry := original.ExpiresAt
	for waiting := true; waiting; {
		select {
		case <-timer.C:
			waiting = false
		case <-done:
			t.Fatal("stream exited before its lease could be renewed")
		case <-streamCtx.Done():
			t.Fatalf("stream lost lease: %v cause=%v", streamCtx.Err(), context.Cause(streamCtx))
		case <-ticker.C:
			task := runtimeGuardTask(t, f)
			if task.Lease == nil || task.Lease.Owner != original.Owner || task.Lease.Epoch != original.Epoch || !task.Lease.ExpiresAt.After(time.Now()) {
				t.Fatalf("live stream lost ownership: %+v", task)
			}
			if task.Lease.ExpiresAt.After(lastExpiry) {
				renewals++
				lastExpiry = task.Lease.ExpiresAt
			}
		}
	}
	if renewals < 2 || monitor.count() < 2 || time.Now().Before(original.ExpiresAt) {
		t.Fatalf("no sustained renewal evidence: renewals=%d checks=%d original=%s", renewals, monitor.count(), original.ExpiresAt)
	}
	intruder, err := sqlite.Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer intruder.Close()
	if err := intruder.EnableWorkerLeases("other-worker", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := intruder.ClaimNext(context.Background()); err != nil || ok {
		t.Fatalf("live lease reclaimed: ok=%t err=%v", ok, err)
	}
	wrong := domain.WithExecutionLease(context.Background(), domain.ExecutionLease{Owner: "other-worker", Epoch: original.Epoch, ExpiresAt: time.Now().Add(time.Minute)})
	if _, err := intruder.ReserveResearchLLMCall(wrong, f.task.ID); !errors.Is(err, sqlite.ErrLeaseLost) {
		t.Fatalf("foreign owner mutated live task: %v", err)
	}
	close(finish)
	runtimeGuardWait(t, done)
	assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
	if len(model.calls()) != 1 || researchRunSession(t, f.store, f.task.ID).Counters.LLMCalls != 1 {
		t.Fatal("renewal consumed inference reservations")
	}
}

func TestRuntimeGuardKeepAwakeOptInAndUnavailableWarning(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		global, persisted, unavailable bool
	}{
		{"disabled", false, false, false}, {"global", true, false, false}, {"persisted", false, true, false}, {"both", true, true, false}, {"unavailable", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResearchRunStore(t)
			f.submit(t, "Hello", func(s *domain.ResearchSession) { s.Policy.KeepAwake = tc.persisted })
			opts, _, sleep := runtimeGuardOptions()
			opts.KeepAwake, sleep.unavailable = tc.global, tc.unavailable
			wanted := tc.global || tc.persisted
			var w *Worker
			model := &researchRunModel{respond: func(context.Context, llm.ChatRequest, int, bool) (string, error) {
				status := w.RuntimeStatus()
				s := status["sleep"].(keepawake.Status)
				if status["keep_awake"] != wanted || s.Active != (wanted && !tc.unavailable) {
					t.Errorf("acquisition status: %+v", status)
				}
				if tc.unavailable && (s.Available || s.FailureReason == "") {
					t.Error("unavailable inhibitor lost warning or pretended available")
				}
				return "Hello!", nil
			}}
			w = New(f.store, model, tools.NewRegistry(), opts)
			w.runTask(context.Background(), f.task)
			assertResearchRunTerminal(t, f, domain.StatusCompleted, "")
			wantCalls := 0
			if wanted {
				wantCalls = 1
			}
			if calls, releases := sleep.counts(); calls != wantCalls || releases != wantCalls {
				t.Fatalf("acquisition/release counts %d/%d, want %d", calls, releases, wantCalls)
			}
			if w.RuntimeStatus()["sleep"].(keepawake.Status).Active {
				t.Fatal("terminal run still reports active inhibitor")
			}
		})
	}
}

func TestRuntimeGuardKeepAwakeReleasedOnceOnCancelAndResourcePause(t *testing.T) {
	for _, mode := range []string{"cancel", "resource_pause"} {
		t.Run(mode, func(t *testing.T) {
			f := newResearchRunStore(t)
			f.submit(t, "Hello", nil)
			opts, monitor, sleep := runtimeGuardOptions()
			opts.KeepAwake = true
			model, started, _ := runtimeGuardBlockingModel("")
			w := New(f.store, model, tools.NewRegistry(), opts)
			done := runtimeGuardStart(t, w, f.task)
			runtimeGuardAwaitStream(t, started)
			if calls, releases := sleep.counts(); calls != 1 || releases != 0 {
				t.Fatalf("inhibitor not held during stream: %d/%d", calls, releases)
			}
			if mode == "cancel" {
				if _, err := f.store.TransitionTask(context.Background(), f.task.ID, domain.StatusCancelled, nil, "", "user", nil); err != nil {
					t.Fatal(err)
				}
				w.Interrupt(f.task.ID)
			} else {
				monitor.setLowRAM(true)
			}
			runtimeGuardWait(t, done)
			if mode == "cancel" {
				if runtimeGuardTask(t, f).Status != domain.StatusCancelled {
					t.Fatal("cancel overwritten")
				}
			} else {
				runtimeGuardAssertPaused(t, f, "low_ram")
			}
			if calls, releases := sleep.counts(); calls != 1 || releases != 1 {
				t.Fatalf("cleanup not exactly once: %d/%d", calls, releases)
			}
			if w.RuntimeStatus()["sleep"].(keepawake.Status).Active || w.Busy() {
				t.Fatal("inactive run retained runtime ownership")
			}
		})
	}
}

func TestRuntimeGuardPartialStreamPauseRequiresRecoveryWithoutReplay(t *testing.T) {
	f := newResearchRunStore(t)
	f.submit(t, "Hello", nil)
	opts, monitor, _ := runtimeGuardOptions()
	model, started, _ := runtimeGuardBlockingModel("Retained partial answer")
	w := New(f.store, model, tools.NewRegistry(), opts)
	done := runtimeGuardStart(t, w, f.task)
	runtimeGuardAwaitStream(t, started)
	chatTestPoll(t, "durable partial answer", func() bool { return chatTestMessage(t, f.store, f.turn).Content == "Retained partial answer" })
	cp, err := f.store.LatestCheckpoint(context.Background(), f.task.ID)
	if err != nil || cp.State.(map[string]any)["pending_invocation_id"] == nil {
		t.Fatalf("stream began without durable pending context: %+v %v", cp, err)
	}
	monitor.setLowRAM(true)
	runtimeGuardWait(t, done)
	runtimeGuardAssertPaused(t, f, "low_ram")
	before := researchRunSession(t, f.store, f.task.ID)
	if chatTestMessage(t, f.store, f.turn).Content != "Retained partial answer" {
		t.Fatal("resource pause discarded streamed partial output")
	}
	monitor.setLowRAM(false)
	runtimeGuardMakeProbeDue(t, f)
	w.probeResourcePauses(context.Background())
	if runtimeGuardTask(t, f).Status != domain.StatusQueued {
		t.Fatal("healthy probe did not release resource pause")
	}
	f.claim(t)
	resumed := runtimeGuardStart(t, w, f.task)
	runtimeGuardWait(t, resumed)
	if len(model.calls()) != 1 {
		t.Fatalf("partial stream blindly replayed: %d calls", len(model.calls()))
	}
	task := runtimeGuardTask(t, f)
	run, err := f.store.GetRun(context.Background(), task.ID)
	if err != nil || task.Status != domain.StatusFailed || task.TerminalOutcome != "blocked" || run.ErrorCode != "failed_recovery_required" {
		t.Errorf("ambiguous stream missing recovery outcome: task=%+v run=%+v %v", task, run, err)
	}
	after := researchRunSession(t, f.store, f.task.ID)
	if before.Counters != after.Counters || !before.DeadlineAt.Equal(after.DeadlineAt) {
		t.Fatal("recovery replay charged work or reset deadline")
	}
	for _, in := range runtimeGuardStartedInvocations(t, f) {
		if strings.HasPrefix(in.Kind, "tool.") {
			t.Fatalf("recovery started a new tool: %+v", in)
		}
	}
}
