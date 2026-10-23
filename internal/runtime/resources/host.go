package resources

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
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

	"github.com/slager2/qwenchik/internal/domain"
)

const (
	maxProcEntries       = 16384
	maxHwmonEntries      = 128
	maxHwmonSensorFiles  = 256
	maxThermalZoneFiles  = 128
	maxTelemetryDirLimit = 1 << 16
)

var (
	errTelemetryDirectoryTooLarge = errors.New("telemetry directory exceeds bound")
	errInvalidProcEntry           = errors.New("invalid proc entry")
)

type HostProvider struct {
	policy    Policy
	healthURL string
	client    *http.Client
	readFile  func(string, int) ([]byte, error)
	readDir   func(string, int) ([]string, error)
	procPIDs  func(int) ([]string, error)
	run       func(context.Context, string, ...string) ([]byte, error)
	powerMu   sync.Mutex
	powerAt   time.Time
	power     PowerSource
	cpuMu     sync.Mutex
	cpuBase   *linuxCPUCounters
	procRSSMu sync.Mutex
	procRSSAt time.Time
	procRSS   domain.Metric[int64]
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
	return &HostProvider{
		policy: policy, healthURL: u.String(), client: client, readFile: readBounded,
		readDir: readDirBounded, procPIDs: readProcPIDs, run: runBounded, power: PowerUnknown,
	}, nil
}

func (p *HostProvider) Snapshot(ctx context.Context) (Snapshot, error) {
	updatedAt := time.Now().UTC()
	s := Snapshot{SnapshotAt: updatedAt, PowerSource: PowerUnknown, Warnings: []string{}}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	s.HeapAllocBytes = &mem.HeapAlloc
	metricError := domain.MetricErrorUnsupported
	if runtime.GOOS == "linux" {
		metricError = domain.MetricErrorTemporarilyUnavailable
	}
	system := unavailableSystemTelemetry(updatedAt, metricError)
	capabilities := domain.TelemetryCapabilities{}
	if runtime.GOOS == "linux" {
		p.collectMemory(&s, &system, &capabilities, updatedAt)
		p.collectAgentRSS(&s, &system, updatedAt)
		var cpuUsageSupported bool
		system.CPUUsage, cpuUsageSupported = p.cpuUsage(updatedAt)
		var cpuTemperatureSupported bool
		system.CPUTemperature, cpuTemperatureSupported = p.cpuTemperature(updatedAt)
		capabilities.CPU = cpuUsageSupported || cpuTemperatureSupported
		if !cpuTemperatureSupported {
			appendWarning(&s, "CPU temperature unavailable")
		}
		if !capabilities.CPU {
			appendWarning(&s, "CPU telemetry unavailable")
		}
		p.collectGPUs(ctx, &s, &system, &capabilities, updatedAt)
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		system.LlamaServerRSS = p.llamaServerRSS(updatedAt)
		capabilities.ProcessRSS = system.AgentRSS.Value != nil || system.LlamaServerRSS.Value != nil
		if !capabilities.ProcessRSS {
			appendWarning(&s, "llama-server RSS unavailable")
		}
	}
	system.LlamaSlotsActive = unavailableMetric[int64](domain.MetricUnitSlots, updatedAt, metricError)
	system.LlamaSlotsTotal = unavailableMetric[int64](domain.MetricUnitSlots, updatedAt, metricError)
	system.LlamaHealth = unavailableMetric[string](domain.MetricUnitStatus, updatedAt, metricError)
	staleAfter := int64(p.policy.CheckInterval.Milliseconds()) + int64((30 * time.Second).Milliseconds())
	s.StaleAfterMS = &staleAfter
	s.System = &system
	s.Capabilities = &capabilities

	// Keep the legacy power source probe and cache semantics intact.
	var powerUpdatedAt time.Time
	s.PowerSource, powerUpdatedAt = p.powerSourceAt(ctx)
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if s.PowerSource == PowerAC || s.PowerSource == PowerBattery {
		value := string(s.PowerSource)
		if powerUpdatedAt.IsZero() {
			powerUpdatedAt = updatedAt
		}
		system.PowerSource = domain.NewExactMetric(value, domain.MetricUnitStatus, powerUpdatedAt)
		capabilities.Power = true
	} else {
		code := metricError
		if runtime.GOOS == "linux" {
			code = domain.MetricErrorTemporarilyUnavailable
		}
		system.PowerSource = unavailableMetric[string](domain.MetricUnitStatus, updatedAt, code)
		if runtime.GOOS == "linux" {
			appendWarning(&s, "power source unavailable")
		}
	}
	system.PowerWatts = unavailableMetric[float64](domain.MetricUnitWatts, updatedAt, domain.MetricErrorUnsupported)
	system.BatteryPercent = unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorUnsupported)
	system.BatteryCharging = unavailableMetric[bool](domain.MetricUnitBoolean, updatedAt, domain.MetricErrorUnsupported)
	capabilities.Battery = false
	capabilities.LlamaMetrics = false
	capabilities.LlamaTimings = false
	capabilities.LlamaSlots = false

	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	start := time.Now()
	healthy, reason := p.health(ctx)
	latency := time.Since(start)
	s.LlamaHealthy, s.LlamaHealthReason, s.LlamaHealthLatency = &healthy, reason, &latency
	if healthy {
		system.LlamaHealth = domain.NewExactMetric(domain.LlamaHealthHealthy, domain.MetricUnitStatus, updatedAt)
	} else {
		system.LlamaHealth = domain.NewExactMetric(domain.LlamaHealthUnhealthy, domain.MetricUnitStatus, updatedAt)
	}
	capabilities.LlamaHealth = true
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	s.SnapshotAt = time.Now().UTC()
	thresholdWarnings(&s, p.policy)
	return s, nil
}

func unavailableSystemTelemetry(updatedAt time.Time, code domain.MetricErrorCode) domain.SystemTelemetry {
	return domain.SystemTelemetry{
		CPUUsage:         unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, code),
		CPUTemperature:   unavailableMetric[float64](domain.MetricUnitCelsius, updatedAt, code),
		RAMUsed:          unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code),
		RAMFree:          unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code),
		RAMTotal:         unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code),
		GPUs:             []domain.GPUTelemetry{},
		AgentRSS:         unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code),
		LlamaServerRSS:   unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code),
		PowerSource:      unavailableMetric[string](domain.MetricUnitStatus, updatedAt, code),
		PowerWatts:       unavailableMetric[float64](domain.MetricUnitWatts, updatedAt, code),
		BatteryPercent:   unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, code),
		BatteryCharging:  unavailableMetric[bool](domain.MetricUnitBoolean, updatedAt, code),
		LlamaHealth:      unavailableMetric[string](domain.MetricUnitStatus, updatedAt, code),
		LlamaSlotsActive: unavailableMetric[int64](domain.MetricUnitSlots, updatedAt, code),
		LlamaSlotsTotal:  unavailableMetric[int64](domain.MetricUnitSlots, updatedAt, code),
	}
}

func unavailableMetric[T domain.MetricValue](unit string, updatedAt time.Time, code domain.MetricErrorCode) domain.Metric[T] {
	return domain.NewUnavailableMetricWithError[T](unit, updatedAt, code)
}

func (p *HostProvider) collectMemory(snapshot *Snapshot, system *domain.SystemTelemetry, capabilities *domain.TelemetryCapabilities, updatedAt time.Time) {
	data, err := p.readFile("/proc/meminfo", 64<<10)
	if err != nil {
		code := metricErrorCode(err)
		system.RAMUsed = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		system.RAMFree = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		system.RAMTotal = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		appendWarning(snapshot, "RAM telemetry unavailable")
		return
	}
	total, available := parseLinuxMemoryInfo(data)
	if total != nil {
		system.RAMTotal = domain.NewExactMetric(*total, domain.MetricUnitBytes, updatedAt)
	} else {
		system.RAMTotal = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
	}
	if available != nil {
		snapshot.FreeRAMBytes = copyPtr(available)
		system.RAMFree = domain.NewExactMetric(*available, domain.MetricUnitBytes, updatedAt)
	} else {
		system.RAMFree = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
	}
	if total != nil && available != nil && *available <= *total {
		used := *total - *available
		system.RAMUsed = domain.NewExactMetric(used, domain.MetricUnitBytes, updatedAt)
		capabilities.RAM = true
	} else {
		code := domain.MetricErrorCollectionFailed
		if total == nil || available == nil {
			code = domain.MetricErrorTemporarilyUnavailable
		}
		system.RAMUsed = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		if available != nil && total != nil && *available > *total {
			system.RAMFree = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
			snapshot.FreeRAMBytes = nil
		}
		appendWarning(snapshot, "RAM telemetry unavailable")
	}
}

func (p *HostProvider) collectAgentRSS(snapshot *Snapshot, system *domain.SystemTelemetry, updatedAt time.Time) {
	data, err := p.readFile("/proc/self/status", 64<<10)
	if err != nil {
		system.AgentRSS = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, metricErrorCode(err))
		appendWarning(snapshot, "agent RSS unavailable")
		return
	}
	rss := parseLinuxProcessRSS(data)
	if rss == nil {
		system.AgentRSS = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
		appendWarning(snapshot, "agent RSS unavailable")
		return
	}
	snapshot.RSSBytes = copyPtr(rss)
	system.AgentRSS = domain.NewExactMetric(*rss, domain.MetricUnitBytes, updatedAt)
}

func (p *HostProvider) cpuUsage(updatedAt time.Time) (domain.Metric[float64], bool) {
	p.cpuMu.Lock()
	defer p.cpuMu.Unlock()
	data, err := p.readFile("/proc/stat", 64<<10)
	if err != nil {
		return unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, metricErrorCode(err)), false
	}
	counters, err := parseLinuxCPUStat(data)
	if err != nil {
		p.cpuBase = nil
		return unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorCollectionFailed), false
	}
	metric := linuxCPUUsage(p.cpuBase, counters, updatedAt)
	baseline := counters
	p.cpuBase = &baseline
	return metric, true
}

func (p *HostProvider) collectGPUs(ctx context.Context, snapshot *Snapshot, system *domain.SystemTelemetry, capabilities *domain.TelemetryCapabilities, updatedAt time.Time) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	data, err := p.run(probeCtx, "nvidia-smi", "--query-gpu=name,utilization.gpu,temperature.gpu,memory.used,memory.free,memory.total,power.draw", "--format=csv,noheader,nounits")
	cancel()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		system.GPUs = []domain.GPUTelemetry{}
		appendWarning(snapshot, "GPU telemetry unavailable")
		return
	}
	gpus, err := parseLinuxGPUTelemetry(data, updatedAt)
	if err != nil {
		system.GPUs = []domain.GPUTelemetry{}
		appendWarning(snapshot, "GPU telemetry unavailable")
		return
	}
	system.GPUs = gpus
	if len(gpus) == 0 {
		appendWarning(snapshot, "GPU telemetry unavailable")
		return
	}
	capabilities.GPU = true
	for _, gpu := range gpus {
		capabilities.GPUUtilization = capabilities.GPUUtilization || gpu.Utilization.Value != nil
		capabilities.GPUTemperature = capabilities.GPUTemperature || gpu.Temperature.Value != nil
	}
	snapshot.GPUFreeVRAMMB, snapshot.GPUTempC = legacyLinuxGPUGuards(gpus)
}

func metricErrorCode(err error) domain.MetricErrorCode {
	switch {
	case err == nil:
		return domain.MetricErrorTemporarilyUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return domain.MetricErrorTimeout
	case errors.Is(err, fs.ErrPermission):
		return domain.MetricErrorPermissionDenied
	case errors.Is(err, exec.ErrNotFound):
		return domain.MetricErrorUnsupported
	case errors.Is(err, fs.ErrNotExist):
		return domain.MetricErrorTemporarilyUnavailable
	default:
		return domain.MetricErrorCollectionFailed
	}
}

func appendWarning(snapshot *Snapshot, warning string) {
	for _, existing := range snapshot.Warnings {
		if existing == warning {
			return
		}
	}
	snapshot.Warnings = append(snapshot.Warnings, warning)
}

func readDirBounded(path string, limit int) ([]string, error) {
	if limit <= 0 || limit > maxTelemetryDirLimit {
		return nil, errors.New("invalid telemetry directory bound")
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errTelemetryDirectoryTooLarge
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func readProcPIDs(limit int) ([]string, error) {
	entries, err := readDirBounded("/proc", limit)
	if err != nil {
		return nil, err
	}
	pids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !isDecimalDigits(entry) {
			continue
		}
		if !validProcPID(entry) {
			return nil, errInvalidProcEntry
		}
		pids = append(pids, entry)
	}
	return pids, nil
}

func validProcPID(pid string) bool {
	if !isDecimalDigits(pid) {
		return false
	}
	value, err := strconv.ParseUint(pid, 10, 31)
	return err == nil && value > 0
}

func (p *HostProvider) llamaServerRSS(updatedAt time.Time) domain.Metric[int64] {
	ttl := p.policy.CheckInterval
	if ttl <= 0 || ttl > 5*time.Second {
		ttl = 5 * time.Second
	}
	p.procRSSMu.Lock()
	defer p.procRSSMu.Unlock()
	if !p.procRSSAt.IsZero() && time.Since(p.procRSSAt) < ttl {
		return cloneMetric(p.procRSS)
	}
	metric := p.scanLlamaServerRSS(updatedAt)
	p.procRSS = cloneMetric(metric)
	p.procRSSAt = time.Now()
	return metric
}

func (p *HostProvider) scanLlamaServerRSS(updatedAt time.Time) domain.Metric[int64] {
	if runtime.GOOS != "linux" {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorUnsupported)
	}
	pids, err := p.procPIDs(maxProcEntries)
	if err != nil {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, metricErrorCode(err))
	}
	if len(pids) > maxProcEntries {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
	}
	var match string
	for _, pid := range pids {
		if !validProcPID(pid) {
			return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
		}
		comm, readErr := p.readFile("/proc/"+pid+"/comm", 256)
		if readErr != nil {
			// Any unreadable or disappearing numeric entry could conceal or be
			// the target, so uniqueness cannot be established safely.
			return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, metricErrorCode(readErr))
		}
		if parseLinuxProcessComm(comm) != "llama-server" {
			continue
		}
		if match != "" {
			return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorTemporarilyUnavailable)
		}
		match = pid
	}
	if match == "" {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorTemporarilyUnavailable)
	}
	status, err := p.readFile("/proc/"+match+"/status", 64<<10)
	if err != nil {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, metricErrorCode(err))
	}
	comm, err := p.readFile("/proc/"+match+"/comm", 256)
	if err != nil {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, metricErrorCode(err))
	}
	if parseLinuxProcessComm(comm) != "llama-server" {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorTemporarilyUnavailable)
	}
	rss := parseLinuxProcessRSS(status)
	if rss == nil {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
	}
	return domain.NewExactMetric(*rss, domain.MetricUnitBytes, updatedAt)
}

func (p *HostProvider) cpuTemperature(updatedAt time.Time) (domain.Metric[float64], bool) {
	if runtime.GOOS != "linux" {
		return unavailableMetric[float64](domain.MetricUnitCelsius, updatedAt, domain.MetricErrorUnsupported), false
	}
	var selected, invalid bool
	var highest *float64
	var scanErr error
	consider := func(metric domain.Metric[float64], found bool) {
		if !found {
			return
		}
		selected = true
		if metric.Value == nil {
			invalid = true
			return
		}
		if highest == nil || *metric.Value > *highest {
			value := *metric.Value
			highest = &value
		}
	}

	hwmonNames, err := p.readDir("/sys/class/hwmon", maxHwmonEntries)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		scanErr = err
	}
	for _, hwmon := range hwmonNames {
		if !validNumberedEntry(hwmon, "hwmon") {
			continue
		}
		base := "/sys/class/hwmon/" + hwmon
		deviceName, readErr := p.readOptionalFile(base+"/name", 256)
		if readErr != nil {
			scanErr = readErr
			continue
		}
		files, readErr := p.readDir(base, maxHwmonSensorFiles)
		if readErr != nil {
			scanErr = readErr
			continue
		}
		for _, file := range files {
			if !validTemperatureInput(file) {
				continue
			}
			stem := strings.TrimSuffix(file, "_input")
			label, labelErr := p.readOptionalFile(base+"/"+stem+"_label", 256)
			sensorType, typeErr := p.readOptionalFile(base+"/"+stem+"_type", 256)
			if labelErr != nil || typeErr != nil {
				if labelErr != nil {
					scanErr = labelErr
				} else {
					scanErr = typeErr
				}
				continue
			}
			if strings.TrimSpace(sensorType) == "" {
				sensorType = deviceName
			}
			if !isLinuxCPUSensor(label, sensorType) {
				continue
			}
			raw, readErr := p.readFile(base+"/"+file, 256)
			if readErr != nil {
				scanErr = readErr
				selected = true
				invalid = true
				continue
			}
			metric, found := parseLinuxCPUTemperature(label, sensorType, raw, updatedAt)
			consider(metric, found)
		}
	}

	thermalNames, err := p.readDir("/sys/class/thermal", maxThermalZoneFiles)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		scanErr = err
	}
	for _, zone := range thermalNames {
		if !validNumberedEntry(zone, "thermal_zone") {
			continue
		}
		base := "/sys/class/thermal/" + zone
		typeBytes, typeErr := p.readFile(base+"/type", 256)
		if typeErr != nil {
			scanErr = typeErr
			continue
		}
		sensorType := strings.TrimSpace(string(typeBytes))
		if !isLinuxCPUSensor(sensorType, sensorType) {
			continue
		}
		raw, readErr := p.readFile(base+"/temp", 256)
		if readErr != nil {
			scanErr = readErr
			selected = true
			invalid = true
			continue
		}
		metric, found := parseLinuxCPUTemperature(sensorType, sensorType, raw, updatedAt)
		consider(metric, found)
	}
	if scanErr != nil {
		return unavailableMetric[float64](domain.MetricUnitCelsius, updatedAt, metricErrorCode(scanErr)), selected
	}
	if !selected {
		return unavailableMetric[float64](domain.MetricUnitCelsius, updatedAt, domain.MetricErrorUnsupported), false
	}
	if invalid || highest == nil {
		return unavailableMetric[float64](domain.MetricUnitCelsius, updatedAt, domain.MetricErrorCollectionFailed), true
	}
	return domain.NewExactMetric(*highest, domain.MetricUnitCelsius, updatedAt), true
}

func (p *HostProvider) readOptionalFile(path string, limit int) (string, error) {
	data, err := p.readFile(path, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func validNumberedEntry(entry, prefix string) bool {
	if !strings.HasPrefix(entry, prefix) {
		return false
	}
	return isDecimalDigits(strings.TrimPrefix(entry, prefix))
}

func validTemperatureInput(file string) bool {
	if !strings.HasPrefix(file, "temp") || !strings.HasSuffix(file, "_input") {
		return false
	}
	channel := strings.TrimSuffix(strings.TrimPrefix(file, "temp"), "_input")
	return isDecimalDigits(channel)
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
	source, _ := p.powerSourceAt(ctx)
	return source
}

func (p *HostProvider) powerSourceAt(ctx context.Context) (PowerSource, time.Time) {
	if runtime.GOOS != "linux" {
		return PowerUnknown, time.Time{}
	}
	p.powerMu.Lock()
	defer p.powerMu.Unlock()
	if time.Since(p.powerAt) < 30*time.Second {
		return p.power, p.powerAt
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, err := p.run(probeCtx, "busctl", "--system", "get-property", "org.freedesktop.UPower", "/org/freedesktop/UPower", "org.freedesktop.UPower", "OnBattery")
	if ctx.Err() != nil {
		return PowerUnknown, time.Time{}
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
	return p.power, p.powerAt
}
