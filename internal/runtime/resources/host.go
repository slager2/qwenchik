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
	maxWindowsProcesses  = 1024
	maxHwmonEntries      = 128
	maxHwmonSensorFiles  = 256
	maxThermalZoneFiles  = 128
	maxTelemetryDirLimit = 1 << 16
	gpuProbeTimeout      = 2 * time.Second
	windowsProbeTimeout  = 2 * time.Second
)

var (
	errTelemetryDirectoryTooLarge = errors.New("telemetry directory exceeds bound")
	errInvalidProcEntry           = errors.New("invalid proc entry")
)

type HostProvider struct {
	policy             Policy
	healthURL          string
	client             *http.Client
	readFile           func(string, int) ([]byte, error)
	readDir            func(string, int) ([]string, error)
	procPIDs           func(int) ([]string, error)
	run                func(context.Context, string, ...string) ([]byte, error)
	powerMu            sync.Mutex
	powerAt            time.Time
	power              PowerSource
	cpuMu              sync.Mutex
	cpuBase            *linuxCPUCounters
	procRSSMu          sync.Mutex
	procRSSAt          time.Time
	procRSS            domain.Metric[int64]
	windows            windowsTelemetrySource
	windowsMetadataMu  sync.RWMutex
	windowsMetadata    windowsSystemMetadata
	windowsPowerStatus windowsPowerStatus
	windowsPowerError  domain.MetricErrorCode
}

type windowsMemorySample struct {
	Total     uint64
	Available uint64
}

type windowsCPUSample struct {
	CountersAvailable bool
	LoadAvailable     bool
	LoadPercent       float64
	LoadError         domain.MetricErrorCode
}

type windowsProcessSample struct {
	Name      string
	NameKnown bool
	RSS       uint64
	RSSKnown  bool
	Stable    bool
	Error     domain.MetricErrorCode
}

// Windows host metadata supplements the typed uptime metric with platform context.
type windowsSystemMetadata struct {
	OS              string
	Platform        string
	PlatformVersion string
	KernelVersion   string
	KernelArch      string
	UptimeSeconds   uint64
}

type windowsPowerStatus struct {
	ACLineStatus       byte
	BatteryFlag        byte
	BatteryLifePercent byte
}

type windowsTelemetrySource struct {
	memory      func(context.Context) (windowsMemorySample, error)
	cpu         func(context.Context) (windowsCPUSample, error)
	currentRSS  func(context.Context) (uint64, error)
	processes   func(context.Context) ([]windowsProcessSample, error)
	metadata    func(context.Context) (windowsSystemMetadata, error)
	powerStatus func() (windowsPowerStatus, error)
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
		readDir: readDirBounded, procPIDs: readProcPIDs, run: platformTelemetryRunner(), power: PowerUnknown,
		windows: newWindowsTelemetrySource(),
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
	} else if runtime.GOOS == "windows" {
		metricError = domain.MetricErrorTemporarilyUnavailable
	}
	system := unavailableSystemTelemetry(updatedAt, metricError)
	system.Metadata.OS = runtime.GOOS
	capabilities := domain.TelemetryCapabilities{}
	if runtime.GOOS == "linux" {
		p.collectLinuxSystemMetadata(&system, updatedAt)
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
	} else if runtime.GOOS == "windows" {
		p.collectWindowsTelemetry(ctx, &s, &system, &capabilities, updatedAt)
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		capabilities.ProcessRSS = system.AgentRSS.Value != nil || system.LlamaServerRSS.Value != nil
		if !capabilities.ProcessRSS {
			appendWarning(&s, "llama-server RSS unavailable")
		}
	}
	system.LlamaSlotsActive = unavailableMetric[int64](domain.MetricUnitSlots, updatedAt, metricError)
	system.LlamaSlotsTotal = unavailableMetric[int64](domain.MetricUnitSlots, updatedAt, metricError)
	system.LlamaHealth = unavailableMetric[string](domain.MetricUnitStatus, updatedAt, metricError)
	stalenessWindow := p.policy.CheckInterval + 30*time.Second + p.policy.LlamaHealthTimeout + gpuProbeTimeout + 2*windowsProbeTimeout
	staleAfter := stalenessWindow.Milliseconds()
	s.StaleAfterMS = &staleAfter
	s.System = &system
	s.Capabilities = &capabilities

	// Keep the legacy power source probe and cache semantics intact.
	var powerUpdatedAt time.Time
	s.PowerSource, powerUpdatedAt = p.powerSourceAt(ctx)
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if runtime.GOOS == "windows" {
		p.powerMu.Lock()
		status, statusError, statusAt := p.windowsPowerStatus, p.windowsPowerError, p.powerAt
		p.powerMu.Unlock()
		mapped := mapWindowsPowerTelemetry(status, statusError, statusAt)
		system.PowerSource = mapped.source
		system.BatteryPercent = mapped.batteryPercent
		system.BatteryCharging = mapped.batteryCharging
		capabilities.Power = mapped.powerAvailable
		capabilities.Battery = mapped.batteryAvailable
		if !mapped.powerAvailable {
			appendWarning(&s, "power source unavailable")
		}
	} else if s.PowerSource == PowerAC || s.PowerSource == PowerBattery {
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
	if runtime.GOOS != "windows" {
		system.BatteryPercent = unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorUnsupported)
		system.BatteryCharging = unavailableMetric[bool](domain.MetricUnitBoolean, updatedAt, domain.MetricErrorUnsupported)
		capabilities.Battery = false
	}
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
		Metadata:         domain.SystemMetadata{UptimeSeconds: unavailableMetric[float64](domain.MetricUnitSeconds, updatedAt, code)},
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

func (p *HostProvider) collectLinuxSystemMetadata(system *domain.SystemTelemetry, updatedAt time.Time) {
	data, err := p.readFile("/proc/uptime", 128)
	if err != nil {
		system.Metadata.UptimeSeconds = unavailableMetric[float64](domain.MetricUnitSeconds, updatedAt, metricErrorCode(err))
		return
	}
	if uptime, ok := parseLinuxUptime(data); ok {
		system.Metadata.UptimeSeconds = domain.NewExactMetric(uptime, domain.MetricUnitSeconds, updatedAt)
		return
	}
	system.Metadata.UptimeSeconds = unavailableMetric[float64](domain.MetricUnitSeconds, updatedAt, domain.MetricErrorCollectionFailed)
}

func unavailableMetric[T domain.MetricValue](unit string, updatedAt time.Time, code domain.MetricErrorCode) domain.Metric[T] {
	return domain.NewUnavailableMetricWithError[T](unit, updatedAt, code)
}

func (p *HostProvider) collectWindowsTelemetry(ctx context.Context, snapshot *Snapshot, system *domain.SystemTelemetry, capabilities *domain.TelemetryCapabilities, updatedAt time.Time) {
	if p.windows.memory == nil {
		system.RAMUsed = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorUnsupported)
		system.RAMFree = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorUnsupported)
		system.RAMTotal = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorUnsupported)
		appendWarning(snapshot, "RAM telemetry unavailable")
	} else if memory, err := p.windows.memory(ctx); err != nil {
		code := metricErrorCode(err)
		system.RAMUsed = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		system.RAMFree = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		system.RAMTotal = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		appendWarning(snapshot, "RAM telemetry unavailable")
	} else {
		mapWindowsMemory(memory, snapshot, system, capabilities, updatedAt)
	}

	system.CPUTemperature = unavailableMetric[float64](domain.MetricUnitCelsius, updatedAt, domain.MetricErrorUnsupported)
	appendWarning(snapshot, "CPU temperature unavailable")
	if p.windows.cpu == nil {
		system.CPUUsage = unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorUnsupported)
		appendWarning(snapshot, "CPU telemetry unavailable")
	} else if sample, err := p.windows.cpu(ctx); err != nil {
		system.CPUUsage = unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, metricErrorCode(err))
		appendWarning(snapshot, "CPU telemetry unavailable")
	} else {
		system.CPUUsage, capabilities.CPU = mapWindowsCPU(sample, updatedAt)
		if !capabilities.CPU {
			appendWarning(snapshot, "CPU telemetry unavailable")
		}
	}

	if p.windows.currentRSS == nil {
		system.AgentRSS = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorUnsupported)
		appendWarning(snapshot, "agent RSS unavailable")
	} else if rss, err := p.windows.currentRSS(ctx); err != nil {
		system.AgentRSS = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, metricErrorCode(err))
		appendWarning(snapshot, "agent RSS unavailable")
	} else if rss > math.MaxInt64 {
		system.AgentRSS = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
		appendWarning(snapshot, "agent RSS unavailable")
	} else {
		value := int64(rss)
		snapshot.RSSBytes = copyPtr(&value)
		system.AgentRSS = domain.NewExactMetric(value, domain.MetricUnitBytes, updatedAt)
	}

	p.collectGPUs(ctx, snapshot, system, capabilities, updatedAt)

	system.LlamaServerRSS = p.windowsLlamaServerRSS(ctx, updatedAt)
	if p.windows.metadata != nil {
		metadataCtx, cancel := context.WithTimeout(ctx, windowsProbeTimeout)
		metadata, err := p.windows.metadata(metadataCtx)
		cancel()
		if err == nil {
			p.windowsMetadataMu.Lock()
			p.windowsMetadata = metadata
			p.windowsMetadataMu.Unlock()
			system.Metadata.OS = boundedTelemetryText(metadata.OS, 32)
			system.Metadata.Platform = boundedTelemetryText(metadata.Platform, 128)
			system.Metadata.PlatformVersion = boundedTelemetryText(metadata.PlatformVersion, 128)
			system.Metadata.KernelVersion = boundedTelemetryText(metadata.KernelVersion, 128)
			system.Metadata.KernelArch = boundedTelemetryText(metadata.KernelArch, 32)
			if metadata.UptimeSeconds <= math.MaxInt64 {
				system.Metadata.UptimeSeconds = domain.NewExactMetric(float64(metadata.UptimeSeconds), domain.MetricUnitSeconds, updatedAt)
			} else {
				system.Metadata.UptimeSeconds = unavailableMetric[float64](domain.MetricUnitSeconds, updatedAt, domain.MetricErrorCollectionFailed)
			}
		} else {
			system.Metadata.UptimeSeconds = unavailableMetric[float64](domain.MetricUnitSeconds, updatedAt, metricErrorCode(err))
		}
	} else {
		system.Metadata.UptimeSeconds = unavailableMetric[float64](domain.MetricUnitSeconds, updatedAt, domain.MetricErrorUnsupported)
	}
}

func boundedTelemetryText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit || strings.ContainsRune(value, '\x00') {
		return ""
	}
	return value
}

func mapWindowsMemory(memory windowsMemorySample, snapshot *Snapshot, system *domain.SystemTelemetry, capabilities *domain.TelemetryCapabilities, updatedAt time.Time) {
	if memory.Total == 0 || memory.Total > math.MaxInt64 {
		code := domain.MetricErrorCollectionFailed
		if memory.Total == 0 {
			code = domain.MetricErrorTemporarilyUnavailable
		}
		system.RAMUsed = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		system.RAMFree = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		system.RAMTotal = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		appendWarning(snapshot, "RAM telemetry unavailable")
		return
	}
	total := int64(memory.Total)
	system.RAMTotal = domain.NewExactMetric(total, domain.MetricUnitBytes, updatedAt)
	if memory.Available > memory.Total || memory.Available > math.MaxInt64 {
		system.RAMFree = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
		system.RAMUsed = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
		appendWarning(snapshot, "RAM telemetry unavailable")
		return
	}
	free := int64(memory.Available)
	used := total - free
	snapshot.FreeRAMBytes = copyPtr(&free)
	system.RAMFree = domain.NewExactMetric(free, domain.MetricUnitBytes, updatedAt)
	system.RAMUsed = domain.NewExactMetric(used, domain.MetricUnitBytes, updatedAt)
	capabilities.RAM = true
}

func mapWindowsCPU(sample windowsCPUSample, updatedAt time.Time) (domain.Metric[float64], bool) {
	if !sample.CountersAvailable {
		return unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, windowsCPUError(sample)), false
	}
	if !sample.LoadAvailable {
		return unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, windowsCPUError(sample)), true
	}
	if math.IsNaN(sample.LoadPercent) || math.IsInf(sample.LoadPercent, 0) || sample.LoadPercent < 0 || sample.LoadPercent > 100 {
		return unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorCollectionFailed), true
	}
	return domain.NewEstimatedMetric(sample.LoadPercent, domain.MetricUnitPercent, updatedAt), true
}

func windowsCPUError(sample windowsCPUSample) domain.MetricErrorCode {
	if sample.LoadError.Valid() {
		return sample.LoadError
	}
	return domain.MetricErrorTemporarilyUnavailable
}

func (p *HostProvider) windowsLlamaServerRSS(ctx context.Context, updatedAt time.Time) domain.Metric[int64] {
	if p.windows.processes == nil {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorUnsupported)
	}
	ttl := p.policy.CheckInterval
	if ttl <= 0 || ttl > 5*time.Second {
		ttl = 5 * time.Second
	}
	p.procRSSMu.Lock()
	defer p.procRSSMu.Unlock()
	if !p.procRSSAt.IsZero() && time.Since(p.procRSSAt) < ttl {
		return cloneMetric(p.procRSS)
	}
	probeCtx, cancel := context.WithTimeout(ctx, windowsProbeTimeout)
	processes, err := p.windows.processes(probeCtx)
	cancel()
	if ctx.Err() != nil {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorTemporarilyUnavailable)
	}
	var metric domain.Metric[int64]
	if err != nil {
		metric = unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, metricErrorCode(err))
	} else {
		metric = mapWindowsLlamaServerRSS(processes, updatedAt)
	}
	p.procRSS = cloneMetric(metric)
	p.procRSSAt = time.Now()
	return metric
}

func mapWindowsLlamaServerRSS(processes []windowsProcessSample, updatedAt time.Time) domain.Metric[int64] {
	var match *windowsProcessSample
	for i := range processes {
		process := &processes[i]
		if !process.NameKnown {
			code := process.Error
			if !code.Valid() {
				code = domain.MetricErrorTemporarilyUnavailable
			}
			return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
		}
		if !isWindowsLlamaServerName(process.Name) {
			continue
		}
		if match != nil {
			return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorTemporarilyUnavailable)
		}
		match = process
	}
	if match == nil {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorTemporarilyUnavailable)
	}
	if match.Error.Valid() {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, match.Error)
	}
	if !match.Stable || !match.RSSKnown {
		code := domain.MetricErrorTemporarilyUnavailable
		if match.Stable && !match.RSSKnown {
			code = domain.MetricErrorCollectionFailed
		}
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, code)
	}
	if match.RSS > math.MaxInt64 {
		return unavailableMetric[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorCollectionFailed)
	}
	return domain.NewExactMetric(int64(match.RSS), domain.MetricUnitBytes, updatedAt)
}

func isWindowsLlamaServerName(name string) bool {
	base := name
	if index := strings.LastIndexAny(base, `/\\`); index >= 0 {
		base = base[index+1:]
	}
	return strings.EqualFold(base, "llama-server.exe") || strings.EqualFold(base, "llama-server")
}

type windowsPowerTelemetry struct {
	source           domain.Metric[string]
	batteryPercent   domain.Metric[float64]
	batteryCharging  domain.Metric[bool]
	powerAvailable   bool
	batteryAvailable bool
}

func mapWindowsPowerTelemetry(status windowsPowerStatus, errorCode domain.MetricErrorCode, updatedAt time.Time) windowsPowerTelemetry {
	statusAvailable := errorCode == ""
	if !statusAvailable && !errorCode.Valid() {
		errorCode = domain.MetricErrorTemporarilyUnavailable
	}
	result := windowsPowerTelemetry{
		source:          unavailableMetric[string](domain.MetricUnitStatus, updatedAt, errorCode),
		batteryPercent:  unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, errorCode),
		batteryCharging: unavailableMetric[bool](domain.MetricUnitBoolean, updatedAt, errorCode),
	}
	if !statusAvailable {
		return result
	}
	errorCode = domain.MetricErrorTemporarilyUnavailable
	result.source = unavailableMetric[string](domain.MetricUnitStatus, updatedAt, errorCode)
	result.batteryPercent = unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, errorCode)
	result.batteryCharging = unavailableMetric[bool](domain.MetricUnitBoolean, updatedAt, errorCode)
	switch status.ACLineStatus {
	case 0:
		if windowsBatteryPresent(status.BatteryFlag) {
			result.source = domain.NewExactMetric(string(PowerBattery), domain.MetricUnitStatus, updatedAt)
			result.powerAvailable = true
		}
	case 1:
		result.source = domain.NewExactMetric(string(PowerAC), domain.MetricUnitStatus, updatedAt)
		result.powerAvailable = true
	}
	if status.BatteryFlag == 128 {
		result.batteryPercent = unavailableMetric[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorUnsupported)
		result.batteryCharging = unavailableMetric[bool](domain.MetricUnitBoolean, updatedAt, domain.MetricErrorUnsupported)
		return result
	}
	if windowsBatteryPresent(status.BatteryFlag) {
		if status.BatteryLifePercent <= 100 {
			result.batteryPercent = domain.NewExactMetric(float64(status.BatteryLifePercent), domain.MetricUnitPercent, updatedAt)
			result.batteryAvailable = true
		}
		result.batteryCharging = domain.NewExactMetric(status.BatteryFlag&8 != 0, domain.MetricUnitBoolean, updatedAt)
		result.batteryAvailable = true
	}
	return result
}

func windowsPowerSourceFromStatus(status windowsPowerStatus) PowerSource {
	switch status.ACLineStatus {
	case 0:
		if windowsBatteryPresent(status.BatteryFlag) {
			return PowerBattery
		}
		return PowerUnknown
	case 1:
		return PowerAC
	default:
		return PowerUnknown
	}
}

func windowsBatteryPresent(flag byte) bool {
	return flag != 0 && flag != 255 && flag != 128 && flag&^byte(15) == 0
}

func sampleWindowsPowerStatus(read func() (windowsPowerStatus, error)) (windowsPowerStatus, domain.MetricErrorCode, PowerSource) {
	if read == nil {
		return windowsPowerStatus{}, domain.MetricErrorUnsupported, PowerUnknown
	}
	status, err := read()
	if err != nil {
		return windowsPowerStatus{}, metricErrorCode(err), PowerUnknown
	}
	return status, "", windowsPowerSourceFromStatus(status)
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
	probeCtx, cancel := context.WithTimeout(ctx, gpuProbeTimeout)
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
	gpus, err := parseNVIDIAGPUTelemetry(data, updatedAt)
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

var windowsNVIDIASMILocations = [...]string{
	`C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
	`C:\Program Files (x86)\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
	`C:\Windows\System32\nvidia-smi.exe`,
}

func findWindowsNVIDIASMI(stat func(string) (fs.FileInfo, error)) (string, error) {
	if stat == nil {
		return "", exec.ErrNotFound
	}
	permissionDenied := false
	for _, path := range windowsNVIDIASMILocations {
		info, err := stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				permissionDenied = true
			}
			continue
		}
		if info != nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	if permissionDenied {
		return "", fs.ErrPermission
	}
	return "", exec.ErrNotFound
}

func (p *HostProvider) powerSource(ctx context.Context) PowerSource {
	source, _ := p.powerSourceAt(ctx)
	return source
}

func (p *HostProvider) powerSourceAt(ctx context.Context) (PowerSource, time.Time) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return PowerUnknown, time.Time{}
	}
	p.powerMu.Lock()
	defer p.powerMu.Unlock()
	if time.Since(p.powerAt) < 30*time.Second {
		return p.power, p.powerAt
	}
	if runtime.GOOS == "windows" {
		p.power = PowerUnknown
		p.windowsPowerStatus = windowsPowerStatus{}
		p.windowsPowerError = domain.MetricErrorUnsupported
		p.windowsPowerStatus, p.windowsPowerError, p.power = sampleWindowsPowerStatus(p.windows.powerStatus)
		p.powerAt = time.Now()
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
