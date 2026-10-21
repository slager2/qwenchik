package resources

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HostProvider struct {
	policy    Policy
	healthURL string
	client    *http.Client
	readFile  func(string, int) ([]byte, error)
	run       func(context.Context, string, ...string) ([]byte, error)
	powerMu   sync.Mutex
	powerAt   time.Time
	power     PowerSource
}

func NewHostProvider(llamaBaseURL string, policy Policy) (*HostProvider, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	u, err := url.Parse(llamaBaseURL)
	if err != nil || u == nil {
		return nil, errors.New("invalid local llama URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return nil, errors.New("llama health requires a plain loopback HTTP URL without credentials, query or fragment")
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("llama health URL must be loopback")
	}
	if u.Path != "" && u.Path != "/" && u.Path != "/v1" && u.Path != "/v1/" {
		return nil, errors.New("unsupported llama base path")
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid llama port")
		}
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	u.Path = "/health"
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: policy.LlamaHealthTimeout}).DialContext, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192, TLSHandshakeTimeout: policy.LlamaHealthTimeout, ResponseHeaderTimeout: policy.LlamaHealthTimeout}
	client := &http.Client{Transport: transport, Timeout: policy.LlamaHealthTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &HostProvider{policy: policy, healthURL: u.String(), client: client, readFile: readBounded, run: runBounded, power: PowerUnknown}, nil
}

func (p *HostProvider) Snapshot(ctx context.Context) (Snapshot, error) {
	s := Snapshot{SnapshotAt: time.Now().UTC(), PowerSource: PowerUnknown, Warnings: []string{}}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	s.HeapAllocBytes = &mem.HeapAlloc
	if runtime.GOOS == "linux" {
		if data, err := p.readFile("/proc/meminfo", 64<<10); err == nil {
			s.FreeRAMBytes = parseKB(data, "MemAvailable:")
		}
		if data, err := p.readFile("/proc/self/status", 64<<10); err == nil {
			s.RSSBytes = parseKB(data, "VmRSS:")
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	data, err := p.run(probeCtx, "nvidia-smi", "--query-gpu=memory.free,temperature.gpu", "--format=csv,noheader,nounits")
	cancel()
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if err == nil {
		s.GPUFreeVRAMMB, s.GPUTempC = parseGPU(data)
	}
	s.PowerSource = p.powerSource(ctx)
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	start := time.Now()
	healthy, reason := p.health(ctx)
	latency := time.Since(start)
	s.LlamaHealthy, s.LlamaHealthReason, s.LlamaHealthLatency = &healthy, reason, &latency
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	thresholdWarnings(&s, p.policy)
	return s, nil
}

func (p *HostProvider) health(ctx context.Context) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, p.policy.LlamaHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.healthURL, nil)
	if err != nil {
		return false, "invalid health request"
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return false, "local health request failed or timed out"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, "local health returned non-200 status"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return false, "invalid or oversized health response"
	}
	var payload struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Status != "ok" {
		return false, "local health status is not ok"
	}
	return true, ""
}

func readBounded(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAtBounded(f, limit)
}

func readAtBounded(r io.ReaderAt, limit int) ([]byte, error) {
	if limit <= 0 || limit > 1<<20 {
		return nil, errors.New("invalid proc read bound")
	}
	buf := make([]byte, limit+1)
	n, err := r.ReadAt(buf, 0)
	if n > limit {
		return nil, errors.New("proc data exceeds limit")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

func parseKB(data []byte, key string) *int64 {
	var result *int64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != key {
			continue
		}
		if result != nil || len(fields) != 3 || fields[2] != "kB" {
			return nil
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n < 0 || n > math.MaxInt64/1024 {
			return nil
		}
		n *= 1024
		result = &n
	}
	return result
}

func parseGPU(data []byte) (*int64, *float64) {
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(rows) == 0 {
		return nil, nil
	}
	var free *int64
	var temp *float64
	freeKnown, tempKnown := true, true
	for _, row := range rows {
		if len(row) != 2 {
			return nil, nil
		}
		n, e := strconv.ParseInt(strings.TrimSpace(row[0]), 10, 64)
		if e != nil || n < 0 {
			freeKnown = false
		} else if free == nil || n < *free {
			free = &n
		}
		t, e := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if e != nil || math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t > 200 {
			tempKnown = false
		} else if temp == nil || t > *temp {
			temp = &t
		}
	}
	if !freeKnown {
		free = nil
	}
	if !tempKnown {
		temp = nil
	}
	return free, temp
}

type boundedOutput struct {
	data  []byte
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - len(b.data)
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		return remaining, errors.New("telemetry output exceeds bound")
	}
	b.data = append(b.data, p...)
	return n, nil
}

func runBounded(ctx context.Context, name string, args ...string) ([]byte, error) {
	if runtime.GOOS != "linux" || (name != "nvidia-smi" && name != "busctl") {
		return nil, errors.New("telemetry command unavailable")
	}
	path, err := exec.LookPath("/usr/bin/" + name)
	if err != nil {
		path, err = exec.LookPath("/bin/" + name)
	}
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	out := &boundedOutput{limit: 8192}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out.data, err
}

func (p *HostProvider) powerSource(ctx context.Context) PowerSource {
	if runtime.GOOS != "linux" {
		return PowerUnknown
	}
	p.powerMu.Lock()
	defer p.powerMu.Unlock()
	if time.Since(p.powerAt) < 30*time.Second {
		return p.power
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, err := p.run(probeCtx, "busctl", "--system", "get-property", "org.freedesktop.UPower", "/org/freedesktop/UPower", "org.freedesktop.UPower", "OnBattery")
	if ctx.Err() != nil {
		return PowerUnknown
	}
	p.power = PowerUnknown
	if err == nil {
		switch strings.TrimSpace(string(data)) {
		case "b false":
			p.power = PowerAC
		case "b true":
			p.power = PowerBattery
		}
	}
	p.powerAt = time.Now()
	return p.power
}
