package cli

import (
	"context"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSoakAcceptanceRequiresWholeObservedIntervalAndKnownMetrics(t *testing.T) {
	now := time.Now()
	policy := resources.DefaultPolicy()
	for _, mode := range []string{"valid", "short", "unknown", "hot", "bundle_change", "gap", "terminal", "cursor_regression", "chat", "wrong_active_run", "no_work", "policy_change"} {
		t.Run(mode, func(t *testing.T) {
			tracker := soakTracker{receipt: soakReceipt{RequiredSeconds: 900, Reasons: []string{}}}
			ram := int64(8 << 30)
			rss := int64(1 << 30)
			vram := int64(4000)
			temp := float64(60)
			r := labRuntime{RunID: "run", ResearchSessionID: "session", Status: domain.StatusRunning, BundleHash: "fixed"}
			r.Runtime.Policy = &policy
			r.Runtime.ActiveRunID = r.RunID
			r.Runtime.Sleep.Active = true
			for i := 0; i <= 60; i++ {
				at := now.Add(time.Duration(i) * 15 * time.Second)
				if mode == "short" {
					at = now.Add(time.Duration(i) * time.Millisecond)
				}
				r.Cursor = int64(i + 1)
				r.CompletedSteps = i
				r.Runtime.Resources = resources.Snapshot{SnapshotAt: at, FreeRAMBytes: &ram, RSSBytes: &rss, GPUFreeVRAMMB: &vram, GPUTempC: &temp, PowerSource: resources.PowerAC}
				switch mode {
				case "chat":
					r.ResearchSessionID = ""
				case "wrong_active_run":
					r.Runtime.ActiveRunID = "other"
				case "no_work":
					r.CompletedSteps = 0
				case "policy_change":
					if i == 20 {
						changed := policy
						changed.GPUTempMaxC++
						r.Runtime.Policy = &changed
					}
				case "unknown":
					r.Runtime.Resources.GPUTempC = nil
				case "hot":
					temp = 100
				case "bundle_change":
					if i == 20 {
						r.BundleHash = "changed"
					}
				case "gap":
					if i == 20 {
						at = at.Add(60 * time.Second)
					}
				case "terminal":
					r.Status = domain.StatusCompleted
				case "cursor_regression":
					if i == 20 {
						r.Cursor = 0
					}
				}
				tracker.sample(at, r)
			}
			elapsed := 15 * time.Minute
			if mode == "short" {
				elapsed = time.Second
			}
			receipt := tracker.finish(now.Add(elapsed))
			if receipt.Accepted != (mode == "valid") {
				t.Fatalf("receipt=%+v", receipt)
			}
			if receipt.LidBehaviorProven {
				t.Fatal("monitor certified lid behavior")
			}
		})
	}
	if _, err := soakDuration("1s"); err == nil {
		t.Fatal("admitted a fake shortened stage")
	}
}

func TestSoakAcceptanceDoesNotTreatCounterRegressionAsProgress(t *testing.T) {
	now := time.Now()
	policy := resources.DefaultPolicy()
	ram, rss, vram, temp := int64(8<<30), int64(1<<30), int64(4000), float64(60)
	tracker := soakTracker{receipt: soakReceipt{RequiredSeconds: 30, Reasons: []string{}}}
	base := labRuntime{RunID: "run", ResearchSessionID: "session", Status: domain.StatusRunning, BundleHash: "fixed"}
	base.Runtime.Policy, base.Runtime.ActiveRunID, base.Runtime.Sleep.Active = &policy, base.RunID, true
	for i, completed := range []int{5, 3, 2} {
		r := base
		r.CompletedSteps = completed
		r.Cursor = int64(i + 1)
		at := now.Add(time.Duration(i) * 15 * time.Second)
		r.Runtime.Resources = resources.Snapshot{SnapshotAt: at, FreeRAMBytes: &ram, RSSBytes: &rss, GPUFreeVRAMMB: &vram, GPUTempC: &temp, PowerSource: resources.PowerAC}
		tracker.sample(at, r)
	}
	if receipt := tracker.finish(now.Add(30 * time.Second)); receipt.ProgressObserved || receipt.Accepted {
		t.Fatalf("counter regression was accepted as progress: %+v", receipt)
	}
}

func TestSoakAcceptanceRejectsRunThatEndsPaused(t *testing.T) {
	now := time.Now()
	policy := resources.DefaultPolicy()
	ram, rss, vram, temp := int64(8<<30), int64(1<<30), int64(4000), float64(60)
	tracker := soakTracker{receipt: soakReceipt{RequiredSeconds: 15, Reasons: []string{}}}
	for i, status := range []domain.TaskStatus{domain.StatusRunning, domain.StatusPaused} {
		r := labRuntime{RunID: "run", ResearchSessionID: "session", Status: status, BundleHash: "fixed"}
		r.Runtime.Policy, r.Runtime.ActiveRunID, r.Runtime.Sleep.Active = &policy, r.RunID, true
		r.CompletedSteps = i + 1
		at := now.Add(time.Duration(i) * 15 * time.Second)
		r.Runtime.Resources = resources.Snapshot{SnapshotAt: at, FreeRAMBytes: &ram, RSSBytes: &rss, GPUFreeVRAMMB: &vram, GPUTempC: &temp, PowerSource: resources.PowerAC}
		tracker.sample(at, r)
	}
	if receipt := tracker.finish(now.Add(15 * time.Second)); receipt.Accepted {
		t.Fatalf("paused run was accepted: %+v", receipt)
	}
}

func TestLabHTTPRejectsRedirectsAndForeignRuntime(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++ }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, foreign.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"foreign","status":"running"}`))
	}))
	defer server.Close()
	c := labClient{base: server.URL, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err := c.request(context.Background(), "GET", "/redirect", nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if foreignCalls != 0 {
		t.Fatal("followed redirect")
	}
	if _, err := c.runtime(context.Background(), "owned"); err == nil {
		t.Fatal("foreign runtime accepted")
	}
}
