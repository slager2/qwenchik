package resources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestPolicyAndBackoff(t *testing.T) {
	p := DefaultPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.MinFreeRAMBytes != 512<<20 || p.MinFreeVRAMMB != 256 || p.GPUTempMaxC != 85 || p.ProcessRSSMaxBytes != 2<<30 || p.LlamaHealthTimeout != 2*time.Second || p.CheckInterval != 5*time.Second || p.MaxResumeAttempts != 5 {
		t.Fatalf("defaults: %+v", p)
	}
	for attempt, want := range map[int]time.Duration{-1: 5 * time.Second, 0: 5 * time.Second, 1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 4: 40 * time.Second, 5: 60 * time.Second, math.MaxInt: 60 * time.Second} {
		if got := p.Backoff(attempt); got != want {
			t.Fatalf("backoff %d: %v != %v", attempt, got, want)
		}
	}
	p.BackoffBase, p.BackoffMax = time.Duration(math.MaxInt64/2+1), time.Duration(math.MaxInt64)
	if p.Backoff(2) != p.BackoffMax {
		t.Fatal("overflow in backoff")
	}
	for _, mutate := range []func(*Policy){
		func(p *Policy) { p.MinFreeRAMBytes = -1 }, func(p *Policy) { p.MinFreeVRAMMB = -1 }, func(p *Policy) { p.ProcessRSSMaxBytes = -1 },
		func(p *Policy) { p.GPUTempMaxC = math.NaN() }, func(p *Policy) { p.GPUTempMaxC = math.Inf(1) }, func(p *Policy) { p.GPUTempMaxC = -1 },
		func(p *Policy) { p.LlamaHealthTimeout = 0 }, func(p *Policy) { p.CheckInterval = -1 }, func(p *Policy) { p.MaxResumeAttempts = 0 },
		func(p *Policy) { p.BackoffBase = -1 }, func(p *Policy) { p.BackoffMax = 1 },
	} {
		p = DefaultPolicy()
		mutate(&p)
		if p.Validate() == nil {
			t.Fatalf("accepted invalid policy %+v", p)
		}
	}
}

func TestCheckThresholdsAndUnknown(t *testing.T) {
	p := DefaultPolicy()
	good := Snapshot{FreeRAMBytes: ptr(p.MinFreeRAMBytes), RSSBytes: ptr(p.ProcessRSSMaxBytes), GPUFreeVRAMMB: ptr(p.MinFreeVRAMMB), GPUTempC: ptr(p.GPUTempMaxC), LlamaHealthy: ptr(true)}
	if Check(good, p) != nil || Check(Snapshot{}, p) != nil {
		t.Fatal("threshold boundary or unknown violated")
	}
	for _, tc := range []struct {
		code   string
		mutate func(*Snapshot)
	}{
		{"low_ram", func(s *Snapshot) { s.FreeRAMBytes = ptr(p.MinFreeRAMBytes - 1) }},
		{"high_rss", func(s *Snapshot) { s.RSSBytes = ptr(p.ProcessRSSMaxBytes + 1) }},
		{"low_vram", func(s *Snapshot) { s.GPUFreeVRAMMB = ptr(p.MinFreeVRAMMB - 1) }},
		{"gpu_temperature", func(s *Snapshot) { s.GPUTempC = ptr(p.GPUTempMaxC + 1) }},
		{"llama_unhealthy", func(s *Snapshot) { s.LlamaHealthy = ptr(false) }},
	} {
		s := clone(good)
		tc.mutate(&s)
		v := Check(s, p)
		if v == nil || v.Code != tc.code {
			t.Fatalf("%s: %+v", tc.code, v)
		}
		p.Enabled = false
		if Check(s, p) != nil {
			t.Fatal("disabled guard violated")
		}
		p.Enabled = true
	}
	b, err := json.Marshal(Snapshot{})
	if err != nil || !strings.Contains(string(b), `"gpu_temp_c":null`) || !strings.Contains(string(b), `"llama_healthy":null`) {
		t.Fatalf("unknown JSON %s %v", b, err)
	}
}

func TestProcParsingAndBounds(t *testing.T) {
	for _, tc := range []struct {
		data string
		want *int64
	}{
		{"MemTotal: 999 kB\nMemAvailable: 123 kB\n", ptr(int64(123 * 1024))},
		{"MemFree: 1 kB\n", nil}, {"MemAvailable: -1 kB", nil}, {"MemAvailable: 9999999999999999999 kB", nil},
		{"MemAvailable: 9007199254740992 kB", nil}, {"MemAvailable: 12 MB", nil}, {"MemAvailable: 12 kB garbage", nil},
		{"MemAvailable: 12 kB\nMemAvailable: 12 kB", nil},
	} {
		if got := parseKB([]byte(tc.data), "MemAvailable:"); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parse %q: %v", tc.data, got)
		}
	}
	if _, err := readAtBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("accepted oversize")
	}
	if b, err := readAtBounded(strings.NewReader("1234"), 4); err != nil || string(b) != "1234" {
		t.Fatalf("exact bound %s %v", b, err)
	}
	if _, err := readAtBounded(strings.NewReader(""), math.MaxInt); err == nil {
		t.Fatal("accepted overflowing bound")
	}
}

func TestGPUParsing(t *testing.T) {
	for _, tc := range []struct {
		data string
		free *int64
		temp *float64
	}{
		{"2455, 52\n", ptr(int64(2455)), ptr(52.0)},
		{"2000, 50\n1000, 60\n", ptr(int64(1000)), ptr(60.0)},
		{"N/A, 50\n", nil, ptr(50.0)}, {"0, [Not Supported]\n", ptr(int64(0)), nil},
		{"2000, NaN\n", ptr(int64(2000)), nil}, {"2000, +Inf\n", ptr(int64(2000)), nil},
		{"-1, -5\n", nil, nil}, {"2000, 900\n", ptr(int64(2000)), nil}, {"", nil, nil}, {"1,2,3", nil, nil},
		{"2000, 50\nN/A, N/A\n", nil, nil},
	} {
		free, temp := parseGPU([]byte(tc.data))
		if !reflect.DeepEqual(free, tc.free) || !reflect.DeepEqual(temp, tc.temp) {
			t.Fatalf("parse %q: %v %v", tc.data, free, temp)
		}
	}
}

func fakeHost(t *testing.T, base string) *HostProvider {
	t.Helper()
	p, err := NewHostProvider(base, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	p.readFile = func(path string, limit int) ([]byte, error) {
		switch path {
		case "/proc/meminfo":
			return []byte("MemAvailable: 999999 kB\n"), nil
		case "/proc/self/status":
			return []byte("VmRSS: 100 kB\n"), nil
		}
		return nil, errors.New("unknown path")
	}
	p.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "nvidia-smi":
			if !reflect.DeepEqual(args, []string{"--query-gpu=memory.free,temperature.gpu", "--format=csv,noheader,nounits"}) {
				t.Errorf("unexpected argv: %v", args)
			}
			return []byte("2000, 50\n"), nil
		case "busctl":
			if !reflect.DeepEqual(args, []string{"--system", "get-property", "org.freedesktop.UPower", "/org/freedesktop/UPower", "org.freedesktop.UPower", "OnBattery"}) {
				t.Errorf("unexpected argv: %v", args)
			}
			return []byte("b false\n"), nil
		}
		return nil, errors.New("unknown command")
	}
	return p
}

func TestHostHealthLocalOnly(t *testing.T) {
	for _, raw := range []string{"https://example.com/v1", "http://192.168.1.1", "http://user:secret@127.0.0.1", "http://127.0.0.1?x=1", "http://127.0.0.1?", "http://127.0.0.1/#fragment", "ftp://127.0.0.1", "http://localhost.evil", "http://127.0.0.1/other", "http://127.0.0.1/%76%31", "http://127.0.0.1:99999"} {
		if _, err := NewHostProvider(raw, DefaultPolicy()); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"http://localhost:8080/v1", "http://[::1]:8080/v1/", "http://127.0.0.1"} {
		if _, err := NewHostProvider(raw, DefaultPolicy()); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" || r.Method != "GET" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	p := fakeHost(t, server.URL+"/v1")
	s, err := p.Snapshot(context.Background())
	if err != nil || s.LlamaHealthy == nil || !*s.LlamaHealthy || s.HeapAllocBytes == nil || s.SnapshotAt.Location() != time.UTC {
		t.Fatalf("snapshot %+v %v", s, err)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("warnings: %v", s.Warnings)
	}
}

func TestHealthFailuresAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"loading", 503, `{"status":"loading model"}`}, {"bad_json", 200, "garbage"}, {"wrong_status", 200, `{"status":"loading"}`}, {"oversize", 200, strings.Repeat(" ", 4097)}, {"redirect", 302, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.com")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			p := fakeHost(t, server.URL)
			s, err := p.Snapshot(context.Background())
			if err != nil || s.LlamaHealthy == nil || *s.LlamaHealthy || s.LlamaHealthReason == "" || Check(s, p.policy) == nil {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	p := fakeHost(t, server.URL)
	p.policy.LlamaHealthTimeout = 20 * time.Millisecond
	s, err := p.Snapshot(context.Background())
	if err != nil || s.LlamaHealthy == nil || *s.LlamaHealthy {
		t.Fatalf("timeout %+v %v", s, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) { cancel(); return nil, ctx.Err() }
	if _, err := p.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation swallowed: %v", err)
	}
}

func TestUnknownTelemetryAndPowerCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"status":"ok"}`) }))
	defer server.Close()
	p := fakeHost(t, server.URL)
	p.readFile = func(string, int) ([]byte, error) { return nil, errors.New("unavailable") }
	calls := 0
	p.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "busctl" {
			calls++
		}
		return nil, errors.New("unavailable")
	}
	for i := 0; i < 2; i++ {
		s, err := p.Snapshot(context.Background())
		if err != nil || s.FreeRAMBytes != nil || s.RSSBytes != nil || s.GPUTempC != nil || s.GPUFreeVRAMMB != nil || s.PowerSource != PowerUnknown || len(s.Warnings) != 4 {
			t.Fatalf("unknown %+v %v", s, err)
		}
	}
	if calls != 1 {
		t.Fatalf("power calls: %d", calls)
	}
}

func TestParentCancelDuringHealth(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	p := fakeHost(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.Snapshot(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("health did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("health cancellation became unknown success: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("health cancellation did not return")
	}
}

func TestTelemetryDeadlineAndUnknownOnOptionalFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	p := fakeHost(t, server.URL)
	p.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Errorf("unbounded optional telemetry: %s", name)
		}
		return nil, context.DeadlineExceeded
	}
	s, err := p.Snapshot(context.Background())
	if err != nil || s.GPUTempC != nil || s.GPUFreeVRAMMB != nil || s.PowerSource != PowerUnknown || s.LlamaHealthy == nil || !*s.LlamaHealthy {
		t.Fatalf("optional timeout: %+v %v", s, err)
	}
}

type providerFunc func(context.Context) (Snapshot, error)

func (f providerFunc) Snapshot(ctx context.Context) (Snapshot, error) { return f(ctx) }

func TestMonitorCopiesAndConcurrency(t *testing.T) {
	s := Snapshot{FreeRAMBytes: ptr(int64(1)), Warnings: []string{"test"}}
	m, err := NewMonitor(providerFunc(func(context.Context) (Snapshot, error) { return s, nil }), DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	got, v, err := m.Check(context.Background())
	if err != nil || v == nil || v.Code != "low_ram" {
		t.Fatalf("%+v %v", v, err)
	}
	*got.FreeRAMBytes = 42
	got.Warnings[0] = "mutated"
	*v.Snapshot.FreeRAMBytes = 43
	last := m.LastSnapshot()
	*last.FreeRAMBytes = 44
	if *m.LastSnapshot().FreeRAMBytes != 1 || s.Warnings[0] != "test" {
		t.Fatal("snapshot aliases mutable data")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _, _ = m.Check(context.Background())
				_ = m.LastSnapshot()
			}
		}()
	}
	wg.Wait()
	errBoom := errors.New("telemetry failed")
	m, _ = NewMonitor(providerFunc(func(context.Context) (Snapshot, error) { return Snapshot{}, errBoom }), DefaultPolicy())
	if _, v, err := m.Check(context.Background()); v != nil || !errors.Is(err, errBoom) {
		t.Fatalf("provider error: %v %v", v, err)
	}
}

func TestModelOOMClassifier(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		oom    bool
	}{
		{503, `{"error":{"code":"out_of_memory","message":"sensitive details"}}`, true},
		{500, `{"error":{"message":"CUDA out of memory"}}`, true},
		{503, `{"error":{"code":503,"type":"server_error","message":"CUDA error: out of memory"}}`, true},
		{503, `{"error":{"message":"loading model"}}`, false},
		{429, `{"error":{"code":"oom"}}`, false},
		{503, "out of memory", false}, {503, strings.Repeat("x", 4097), false},
	} {
		err := ClassifyModelHTTPError(tc.status, []byte(tc.body))
		if errors.Is(err, ErrModelOOM) != tc.oom || errors.Is(err, ErrModelResource) != tc.oom {
			t.Fatalf("classification %d %s: %v", tc.status, tc.body, err)
		}
		if err != nil && strings.Contains(err.Error(), "sensitive") {
			t.Fatal("leaked response body")
		}
	}
}
