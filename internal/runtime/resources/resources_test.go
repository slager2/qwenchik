package resources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func ptr[T any](v T) *T { return &v }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestPolicyAndBackoff(t *testing.T) {
	p := DefaultPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.MinFreeRAMBytes != 512<<20 || p.MinFreeVRAMMB != 256 || p.GPUTempMaxC != 85 || p.ProcessRSSMaxBytes != 2<<30 || p.RecoveryRAMMarginPercent != 10 || p.RecoveryVRAMMarginPercent != 10 || p.RecoveryGPUTempMarginC != 5 || p.RecoveryRSSMarginPercent != 10 || p.LlamaHealthTimeout != 2*time.Second || p.CheckInterval != 5*time.Second || p.MaxResumeAttempts != 5 {
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
		func(p *Policy) { p.RecoveryRAMMarginPercent = -1 }, func(p *Policy) { p.RecoveryRAMMarginPercent = 101 },
		func(p *Policy) { p.RecoveryVRAMMarginPercent = 101 }, func(p *Policy) { p.RecoveryRSSMarginPercent = 101 },
		func(p *Policy) { p.RecoveryGPUTempMarginC = math.NaN() }, func(p *Policy) { p.RecoveryGPUTempMarginC = 86 },
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
	if err != nil || !strings.Contains(string(b), `"gpu_temp_c":null`) || !strings.Contains(string(b), `"llama_healthy":null`) || strings.Contains(string(b), `"system"`) || strings.Contains(string(b), `"capabilities"`) || strings.Contains(string(b), `"stale_after_ms"`) {
		t.Fatalf("unknown JSON %s %v", b, err)
	}
}

func TestRecoveryHysteresisBoundaries(t *testing.T) {
	p := DefaultPolicy()
	p.MinFreeRAMBytes = 100
	p.MinFreeVRAMMB = 100
	p.ProcessRSSMaxBytes = 100
	p.GPUTempMaxC = 85
	p.RecoveryRAMMarginPercent = 10
	p.RecoveryVRAMMarginPercent = 10
	p.RecoveryRSSMarginPercent = 10
	p.RecoveryGPUTempMarginC = 5
	good := Snapshot{
		SnapshotAt:    time.Now().UTC(),
		StaleAfterMS:  ptr(int64(60_000)),
		FreeRAMBytes:  ptr(int64(110)),
		RSSBytes:      ptr(int64(90)),
		GPUFreeVRAMMB: ptr(int64(110)),
		GPUTempC:      ptr(80.0),
		LlamaHealthy:  ptr(true),
	}
	if violation := CheckRecovery(good, p, "low_ram"); violation != nil {
		t.Fatalf("recovery thresholds should be inclusive at clearance: %+v", violation)
	}
	if violation := CheckRecovery(good, p, "unknown_pause_reason"); violation == nil || violation.Code != "telemetry_unavailable" {
		t.Fatalf("unknown pause reason was considered recoverable: %+v", violation)
	}
	for _, tc := range []struct {
		name   string
		code   string
		mutate func(*Snapshot)
	}{
		{"RAM", "low_ram", func(s *Snapshot) { s.FreeRAMBytes = ptr(int64(109)) }},
		{"RSS", "high_rss", func(s *Snapshot) { s.RSSBytes = ptr(int64(91)) }},
		{"VRAM", "low_vram", func(s *Snapshot) { s.GPUFreeVRAMMB = ptr(int64(109)) }},
		{"GPU temperature", "gpu_temperature", func(s *Snapshot) { s.GPUTempC = ptr(80.01) }},
		{"llama health", "llama_unhealthy", func(s *Snapshot) { s.LlamaHealthy = ptr(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := clone(good)
			tc.mutate(&s)
			if violation := CheckRecovery(s, p, "low_ram"); violation == nil || violation.Code != tc.code {
				t.Fatalf("recovery violation = %+v, want %s", violation, tc.code)
			}
		})
	}
}

func TestRecoveryRequiresReasonTelemetryAndStrictTelemetryRecovery(t *testing.T) {
	p := DefaultPolicy()
	complete := Snapshot{
		SnapshotAt:    time.Now().UTC(),
		StaleAfterMS:  ptr(int64(60_000)),
		FreeRAMBytes:  ptr(p.MinFreeRAMBytes * 2),
		RSSBytes:      ptr(p.ProcessRSSMaxBytes / 2),
		GPUFreeVRAMMB: ptr(p.MinFreeVRAMMB * 2),
		GPUTempC:      ptr(p.GPUTempMaxC - p.RecoveryGPUTempMarginC),
		LlamaHealthy:  ptr(true),
	}

	for _, tc := range []struct {
		reason string
		remove func(*Snapshot)
	}{
		{"low_ram", func(s *Snapshot) { s.FreeRAMBytes = nil }},
		{"high_rss", func(s *Snapshot) { s.RSSBytes = nil }},
		{"low_vram", func(s *Snapshot) { s.GPUFreeVRAMMB = nil }},
		{"gpu_temperature", func(s *Snapshot) { s.GPUTempC = nil }},
		{"llama_unhealthy", func(s *Snapshot) { s.LlamaHealthy = nil }},
	} {
		s := clone(complete)
		tc.remove(&s)
		if violation := CheckRecovery(s, p, tc.reason); violation == nil || violation.Code != "telemetry_unavailable" {
			t.Errorf("missing metric for %s pause = %+v, want telemetry_unavailable", tc.reason, violation)
		}
	}

	// Unknown unrelated GPU/system telemetry is not a violation during a RAM
	// recovery probe. Only an unknown metric tied to the pause blocks recovery.
	unrelatedUnknown := clone(complete)
	unrelatedUnknown.GPUFreeVRAMMB = nil
	unrelatedUnknown.GPUTempC = nil
	unrelatedUnknown.System = &domain.SystemTelemetry{}
	if violation := CheckRecovery(unrelatedUnknown, p, "low_ram"); violation != nil {
		t.Fatalf("unrelated unknown telemetry blocked RAM recovery: %+v", violation)
	}
	llamaUnknown := clone(complete)
	llamaUnknown.LlamaHealthy = nil
	if violation := CheckRecovery(llamaUnknown, p, "low_ram"); violation == nil || violation.Code != "telemetry_unavailable" {
		t.Fatalf("missing llama health was accepted for recovery: %+v", violation)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"RAM", func(s *Snapshot) { s.FreeRAMBytes = nil }},
		{"RSS", func(s *Snapshot) { s.RSSBytes = nil }},
		{"VRAM", func(s *Snapshot) { s.GPUFreeVRAMMB = nil }},
		{"GPU temperature", func(s *Snapshot) { s.GPUTempC = nil }},
		{"llama health", func(s *Snapshot) { s.LlamaHealthy = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := clone(complete)
			tc.mutate(&s)
			if violation := CheckRecovery(s, p, "telemetry_unavailable"); violation == nil || violation.Code != "telemetry_unavailable" {
				t.Fatalf("missing telemetry recovery metric = %+v", violation)
			}
		})
	}
	if violation := CheckRecovery(complete, p, "telemetry_unavailable"); violation != nil {
		t.Fatalf("fully observable healthy recovery was blocked: %+v", violation)
	}
	stale := clone(complete)
	stale.SnapshotAt = time.Now().Add(-time.Minute - time.Second)
	if violation := CheckRecovery(stale, p, "telemetry_unavailable"); violation == nil || violation.Code != "telemetry_unavailable" {
		t.Fatalf("stale recovery snapshot was accepted: %+v", violation)
	}
	complete.LlamaHealthy = ptr(false)
	if violation := CheckRecovery(complete, p, "telemetry_unavailable"); violation == nil || violation.Code != "llama_unhealthy" {
		t.Fatalf("unhealthy llama recovery = %+v", violation)
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

func TestLinuxMemoryInfoRequiresValidUniqueFields(t *testing.T) {
	total, available := parseLinuxMemoryInfo([]byte("MemTotal: 1024 kB\nMemAvailable: 256 kB\n"))
	if total == nil || *total != 1024<<10 || available == nil || *available != 256<<10 {
		t.Fatalf("memory info = %v, %v", total, available)
	}
	for _, input := range []string{
		"MemTotal: 0 kB\nMemAvailable: 10 kB\n",
		"MemTotal: 10 kB\nMemTotal: 11 kB\nMemAvailable: 5 kB\n",
		"MemTotal: 10 kB\nMemAvailable: 5 MB\n",
	} {
		total, available = parseLinuxMemoryInfo([]byte(input))
		if total != nil && strings.Contains(input, "MemTotal: 0") {
			t.Errorf("accepted zero total from %q", input)
		}
		if strings.Contains(input, "MemTotal: 10 kB\nMemTotal") && total != nil {
			t.Errorf("accepted duplicate total from %q", input)
		}
		if strings.Contains(input, "MemAvailable: 5 MB") && available != nil {
			t.Errorf("accepted invalid available unit from %q", input)
		}
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

func TestHostCPUUsageUsesLockedBaseline(t *testing.T) {
	samples := [][]byte{
		[]byte("cpu 100 100 100 100 100 100 100 100 100 100\n"),
		[]byte("cpu 130 100 110 150 110 100 100 100 200 300\n"),
	}
	reads := 0
	p := &HostProvider{readFile: func(path string, _ int) ([]byte, error) {
		if path != "/proc/stat" {
			t.Fatalf("unexpected path %q", path)
		}
		data := samples[reads]
		reads++
		return data, nil
	}}
	first, supported := p.cpuUsage(linuxTelemetryTestTime)
	if !supported || first.Source != domain.MetricSourceUnavailable || first.Error != domain.MetricErrorTemporarilyUnavailable {
		t.Fatalf("first CPU sample: %+v supported=%v", first, supported)
	}
	second, supported := p.cpuUsage(linuxTelemetryTestTime.Add(time.Second))
	if !supported || second.Source != domain.MetricSourceExact || second.Value == nil || *second.Value != 40 {
		t.Fatalf("CPU delta sample: %+v supported=%v", second, supported)
	}
}

func TestLlamaServerRSSRequiresOneStableExactProcess(t *testing.T) {
	tests := []struct {
		name      string
		pids      []string
		comm      map[string][]byte
		status    map[string][]byte
		commReads map[string]int
		readError map[string]error
		wantRSS   *int64
		wantError domain.MetricErrorCode
	}{
		{
			name:      "unique match",
			pids:      []string{"100"},
			comm:      map[string][]byte{"/proc/100/comm": []byte("llama-server\n")},
			status:    map[string][]byte{"/proc/100/status": []byte("VmRSS: 567 kB\n")},
			commReads: map[string]int{"/proc/100/comm": 2},
			wantRSS:   ptr(int64(567 << 10)),
		},
		{
			name:      "multiple matches",
			pids:      []string{"100", "200"},
			comm:      map[string][]byte{"/proc/100/comm": []byte("llama-server\n"), "/proc/200/comm": []byte("llama-server\n")},
			commReads: map[string]int{"/proc/100/comm": 1, "/proc/200/comm": 1},
			wantError: domain.MetricErrorTemporarilyUnavailable,
		},
		{
			name:      "missing exact name",
			pids:      []string{"100", "200"},
			comm:      map[string][]byte{"/proc/100/comm": []byte("llama-server-helper\n"), "/proc/200/comm": []byte(" llama-server\n")},
			commReads: map[string]int{"/proc/100/comm": 1, "/proc/200/comm": 1},
			wantError: domain.MetricErrorTemporarilyUnavailable,
		},
		{
			name:      "permission denied while scanning",
			pids:      []string{"100"},
			readError: map[string]error{"/proc/100/comm": fs.ErrPermission},
			wantError: domain.MetricErrorPermissionDenied,
		},
		{
			name:      "process disappeared during scan",
			pids:      []string{"100"},
			readError: map[string]error{"/proc/100/comm": fs.ErrNotExist},
			wantError: domain.MetricErrorTemporarilyUnavailable,
		},
		{
			name:      "identity changed before recheck",
			pids:      []string{"100"},
			comm:      map[string][]byte{"/proc/100/comm": []byte("llama-server\n")},
			status:    map[string][]byte{"/proc/100/status": []byte("VmRSS: 567 kB\n")},
			commReads: map[string]int{"/proc/100/comm": 2},
			wantError: domain.MetricErrorTemporarilyUnavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readCounts := map[string]int{}
			p := &HostProvider{
				procPIDs: func(_ int) ([]string, error) { return tc.pids, nil },
				readFile: func(path string, _ int) ([]byte, error) {
					readCounts[path]++
					if err := tc.readError[path]; err != nil {
						return nil, err
					}
					if path == "/proc/100/comm" && tc.name == "identity changed before recheck" && readCounts[path] == 2 {
						return []byte("other-process\n"), nil
					}
					if data, ok := tc.comm[path]; ok {
						return data, nil
					}
					if data, ok := tc.status[path]; ok {
						return data, nil
					}
					return nil, fs.ErrNotExist
				},
			}
			got := p.llamaServerRSS(linuxTelemetryTestTime)
			if tc.wantRSS != nil {
				if got.Value == nil || *got.Value != *tc.wantRSS || got.Source != domain.MetricSourceExact {
					t.Fatalf("RSS metric: %+v", got)
				}
			} else if got.Value != nil || got.Source != domain.MetricSourceUnavailable || got.Error != tc.wantError {
				t.Fatalf("unknown RSS metric: %+v, want error %q", got, tc.wantError)
			}
			for path, want := range tc.commReads {
				if readCounts[path] != want {
					t.Errorf("%s reads = %d, want %d", path, readCounts[path], want)
				}
			}
		})
	}
}

func TestLlamaServerRSSScanIsCachedAndReturnedValuesAreIndependent(t *testing.T) {
	pidScans := 0
	p := &HostProvider{
		policy: DefaultPolicy(),
		procPIDs: func(_ int) ([]string, error) {
			pidScans++
			return []string{"4242"}, nil
		},
		readFile: func(path string, _ int) ([]byte, error) {
			switch path {
			case "/proc/4242/comm":
				return []byte("llama-server\n"), nil
			case "/proc/4242/status":
				return []byte("VmRSS: 400 kB\n"), nil
			default:
				return nil, fs.ErrNotExist
			}
		},
	}
	first := p.llamaServerRSS(linuxTelemetryTestTime)
	second := p.llamaServerRSS(linuxTelemetryTestTime.Add(time.Second))
	if pidScans != 1 {
		t.Fatalf("process scans = %d, want cached single scan", pidScans)
	}
	if first.Value == nil || second.Value == nil || *first.Value != 400<<10 || *second.Value != 400<<10 || !first.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatalf("cached RSS metrics differ: first=%+v second=%+v", first, second)
	}
	*first.Value = 1
	if *second.Value != 400<<10 {
		t.Fatalf("cached metric aliases first return: %+v", second)
	}
}

func TestPowerSourceCachePreservesOriginalSampleTime(t *testing.T) {
	calls := 0
	p := &HostProvider{run: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte("b false\n"), nil
	}}
	first, firstAt := p.powerSourceAt(context.Background())
	second, secondAt := p.powerSourceAt(context.Background())
	if calls != 1 || first != PowerAC || second != PowerAC || firstAt.IsZero() || !firstAt.Equal(secondAt) {
		t.Fatalf("power cache result=%q/%q timestamp=%s/%s calls=%d", first, second, firstAt, secondAt, calls)
	}
}

func TestCPUTemperatureSelectsExplicitCPUOnly(t *testing.T) {
	readDirs := map[string][]string{
		"/sys/class/hwmon":        {"hwmon0"},
		"/sys/class/hwmon/hwmon0": {"temp1_input", "temp2_input", "temp3_input"},
		"/sys/class/thermal":      {"thermal_zone0"},
	}
	files := map[string]string{
		"/sys/class/hwmon/hwmon0/name":          "nvidia",
		"/sys/class/hwmon/hwmon0/temp1_label":   "GPU Core",
		"/sys/class/hwmon/hwmon0/temp1_type":    "3",
		"/sys/class/hwmon/hwmon0/temp1_input":   "99000",
		"/sys/class/hwmon/hwmon0/temp2_label":   "Package id 0",
		"/sys/class/hwmon/hwmon0/temp2_type":    "1",
		"/sys/class/hwmon/hwmon0/temp2_input":   "46000",
		"/sys/class/hwmon/hwmon0/temp3_label":   "System",
		"/sys/class/hwmon/hwmon0/temp3_type":    "2",
		"/sys/class/hwmon/hwmon0/temp3_input":   "80000",
		"/sys/class/thermal/thermal_zone0/type": "cpu-thermal",
		"/sys/class/thermal/thermal_zone0/temp": "55000",
	}
	p := &HostProvider{
		readDir: func(path string, _ int) ([]string, error) {
			if entries, ok := readDirs[path]; ok {
				return entries, nil
			}
			return nil, fs.ErrNotExist
		},
		readFile: func(path string, _ int) ([]byte, error) {
			if value, ok := files[path]; ok {
				return []byte(value), nil
			}
			return nil, fs.ErrNotExist
		},
	}
	got, supported := p.cpuTemperature(linuxTelemetryTestTime)
	if !supported || got.Source != domain.MetricSourceExact || got.Value == nil || *got.Value != 55 {
		t.Fatalf("CPU temperature = %+v supported=%v", got, supported)
	}
}

func TestNonLinuxTelemetryIsExplicitlyUnknownAndLegacySnapshotStillMarshals(t *testing.T) {
	at := linuxTelemetryTestTime
	system := unavailableSystemTelemetry(at, domain.MetricErrorUnsupported)
	if system.CPUUsage.Source != domain.MetricSourceUnavailable || system.CPUUsage.Error != domain.MetricErrorUnsupported || system.RAMTotal.Value != nil || system.LlamaServerRSS.Value != nil || len(system.GPUs) != 0 {
		t.Fatalf("non-Linux unknown telemetry: %+v", system)
	}
	policy := DefaultPolicy()
	legacy := Snapshot{FreeRAMBytes: ptr(policy.MinFreeRAMBytes), RSSBytes: ptr(int64(200)), GPUFreeVRAMMB: ptr(int64(300)), GPUTempC: ptr(float64(70))}
	encoded, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(encoded), `"system"`) {
		t.Fatalf("legacy snapshot compatibility: %s, %v", encoded, err)
	}
	legacy.GPUFreeVRAMMB = ptr(policy.MinFreeVRAMMB - 1)
	if violation := Check(legacy, policy); violation == nil || violation.Code != "low_vram" {
		t.Fatalf("legacy guard semantics changed: %+v", violation)
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
			return []byte("MemTotal: 8388608 kB\nMemAvailable: 999999 kB\n"), nil
		case "/proc/self/status":
			return []byte("VmRSS: 100 kB\n"), nil
		case "/proc/stat":
			return []byte("cpu 100 10 20 200 5 2 3 1 0 0\n"), nil
		case "/proc/uptime":
			return []byte("12345.67 98765.43\n"), nil
		case "/proc/4242/comm":
			return []byte("llama-server\n"), nil
		case "/proc/4242/status":
			return []byte("Name:\tllama-server\nVmRSS: 400 kB\n"), nil
		case "/sys/class/thermal/thermal_zone0/type":
			return []byte("x86_pkg_temp\n"), nil
		case "/sys/class/thermal/thermal_zone0/temp":
			return []byte("42000\n"), nil
		}
		return nil, fs.ErrNotExist
	}
	p.readDir = func(path string, _ int) ([]string, error) {
		switch path {
		case "/sys/class/hwmon":
			return nil, fs.ErrNotExist
		case "/sys/class/thermal":
			return []string{"thermal_zone0"}, nil
		}
		return nil, fs.ErrNotExist
	}
	p.procPIDs = func(_ int) ([]string, error) { return []string{"4242"}, nil }
	p.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "nvidia-smi":
			if !reflect.DeepEqual(args, []string{"--query-gpu=name,utilization.gpu,temperature.gpu,memory.used,memory.free,memory.total,power.draw", "--format=csv,noheader,nounits"}) {
				t.Errorf("unexpected argv: %v", args)
			}
			return []byte("NVIDIA Test A, 20, 50, 1024, 2000, 4096, 75.5\nNVIDIA Test B, 40, 62, 1024, 1024, 4096, 90\n"), nil
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
	if len(s.Warnings) != 0 || s.System == nil || s.Capabilities == nil || s.StaleAfterMS == nil {
		t.Fatalf("warnings: %v", s.Warnings)
	}
	if *s.StaleAfterMS != 43000 {
		t.Fatalf("stale_after_ms = %d, want configured interval + cache and bounded probe windows", *s.StaleAfterMS)
	}
	if s.System.RAMTotal.Value == nil || *s.System.RAMTotal.Value != 8388608<<10 || s.System.RAMFree.Value == nil || *s.System.RAMFree.Value != 999999<<10 || s.System.RAMUsed.Value == nil || *s.System.RAMUsed.Value != (8388608-999999)<<10 {
		t.Fatalf("RAM system metrics: %+v", s.System)
	}
	if s.System.AgentRSS.Value == nil || *s.System.AgentRSS.Value != 100<<10 || s.RSSBytes == nil || *s.RSSBytes != 100<<10 || s.System.LlamaServerRSS.Value == nil || *s.System.LlamaServerRSS.Value != 400<<10 {
		t.Fatalf("process RSS metrics: %+v", s.System)
	}
	if !s.Capabilities.CPU || !s.Capabilities.RAM || !s.Capabilities.GPU || !s.Capabilities.GPUUtilization || !s.Capabilities.GPUTemperature || !s.Capabilities.ProcessRSS || !s.Capabilities.Power || !s.Capabilities.LlamaHealth || s.Capabilities.LlamaMetrics || s.Capabilities.LlamaTimings || s.Capabilities.LlamaSlots {
		t.Fatalf("capabilities: %+v", s.Capabilities)
	}
	if len(s.System.GPUs) != 2 || s.System.GPUs[0].Name.Value == nil || *s.System.GPUs[0].Name.Value != "NVIDIA Test A" || s.GPUFreeVRAMMB == nil || *s.GPUFreeVRAMMB != 1024 || s.GPUTempC == nil || *s.GPUTempC != 62 {
		t.Fatalf("GPU metrics and legacy guards: %+v / %+v", s.System.GPUs, s)
	}
	if s.System.CPUTemperature.Value == nil || *s.System.CPUTemperature.Value != 42 || s.System.LlamaSlotsActive.Value != nil || s.System.LlamaSlotsTotal.Value != nil {
		t.Fatalf("CPU temperature or later-phase metrics: %+v", s.System)
	}
	if s.System.Metadata.OS != "linux" || s.System.Metadata.UptimeSeconds.Value == nil || *s.System.Metadata.UptimeSeconds.Value != 12345.67 {
		t.Fatalf("system metadata: %+v", s.System.Metadata)
	}
	if s.System.PowerSource.Source != domain.MetricSourceExact || !s.System.PowerSource.UpdatedAt.Equal(p.powerAt) {
		t.Fatalf("power timestamp was refreshed instead of retained: %+v cache_at=%s", s.System.PowerSource, p.powerAt)
	}
	if s.System.PowerSource.UpdatedAt.After(s.SnapshotAt) {
		t.Fatalf("snapshot timestamp predates cached power sample: snapshot=%s power=%s", s.SnapshotAt, s.System.PowerSource.UpdatedAt)
	}
	if _, err := json.Marshal(s); err != nil {
		t.Fatalf("snapshot with optional telemetry is not marshable: %v", err)
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
		if err != nil || s.FreeRAMBytes != nil || s.RSSBytes != nil || s.GPUTempC != nil || s.GPUFreeVRAMMB != nil || s.PowerSource != PowerUnknown || s.System == nil || s.System.RAMTotal.Error != domain.MetricErrorCollectionFailed {
			t.Fatalf("unknown %+v %v", s, err)
		}
		for _, warning := range []string{
			"RAM unknown; configured guard cannot evaluate this metric",
			"process RSS unknown; configured guard cannot evaluate this metric",
			"GPU VRAM unknown; configured guard cannot evaluate this metric",
			"GPU temperature unknown; configured guard cannot evaluate this metric",
		} {
			if !containsString(s.Warnings, warning) {
				t.Errorf("missing static warning %q in %v", warning, s.Warnings)
			}
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
	updatedAt := time.Now().UTC()
	system := unavailableSystemTelemetry(updatedAt, domain.MetricErrorUnsupported)
	system.Metadata.UptimeSeconds = domain.NewExactMetric(123.0, domain.MetricUnitSeconds, updatedAt)
	system.RAMTotal = domain.NewExactMetric(int64(10), domain.MetricUnitBytes, updatedAt)
	system.GPUs = []domain.GPUTelemetry{{Name: domain.NewExactMetric("gpu", domain.MetricUnitName, updatedAt)}}
	capabilities := domain.TelemetryCapabilities{GPU: true}
	staleAfter := int64(1000)
	s := Snapshot{FreeRAMBytes: ptr(int64(1)), Warnings: []string{"test"}, System: &system, Capabilities: &capabilities, StaleAfterMS: &staleAfter}
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
	*got.System.RAMTotal.Value = 11
	*got.System.Metadata.UptimeSeconds.Value = 999
	*got.System.GPUs[0].Name.Value = "mutated"
	got.Capabilities.GPU = false
	*got.StaleAfterMS = 2000
	*v.Snapshot.FreeRAMBytes = 43
	last := m.LastSnapshot()
	*last.FreeRAMBytes = 44
	last = m.LastSnapshot()
	if *last.FreeRAMBytes != 1 || s.Warnings[0] != "test" || *last.System.RAMTotal.Value != 10 || *last.System.Metadata.UptimeSeconds.Value != 123 || *last.System.GPUs[0].Name.Value != "gpu" || !last.Capabilities.GPU || *last.StaleAfterMS != 1000 {
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

func TestMonitorSerializesSnapshotCollectionAndCancelsWaiters(t *testing.T) {
	var calls atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	monitor, err := NewMonitor(providerFunc(func(context.Context) (Snapshot, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return Snapshot{}, nil
	}), DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, _, checkErr := monitor.Check(context.Background())
		firstDone <- checkErr
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first provider check did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, _, checkErr := monitor.Check(ctx)
		secondDone <- checkErr
	}()
	<-secondStarted
	cancel()
	select {
	case checkErr := <-secondDone:
		if !errors.Is(checkErr, context.Canceled) {
			t.Fatalf("waiting check error = %v, want context.Canceled", checkErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled check remained blocked behind the active snapshot")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls while first check blocked = %d, want 1", got)
	}
	close(releaseFirst)
	if checkErr := <-firstDone; checkErr != nil {
		t.Fatalf("first check: %v", checkErr)
	}
	if _, _, err := monitor.Check(context.Background()); err != nil {
		t.Fatalf("subsequent check: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("total provider calls = %d, want 2", got)
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
