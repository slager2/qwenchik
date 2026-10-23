//go:build windows

package resources

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

var (
	windowsGPUQueryArgs = []string{
		"--query-gpu=name,utilization.gpu,temperature.gpu,memory.used,memory.free,memory.total,power.draw",
		"--format=csv,noheader,nounits",
	}
	windowsGPUCommandUnavailable = errors.New("telemetry command unavailable")
	windowsTelemetryFailed       = errors.New("Windows telemetry collection failed")
	windowsPowerProcedure        = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")
)

func newWindowsTelemetrySource() windowsTelemetrySource {
	return windowsTelemetrySource{
		memory:      readWindowsMemory,
		cpu:         readWindowsCPU,
		currentRSS:  readWindowsCurrentRSS,
		processes:   readWindowsProcesses,
		metadata:    readWindowsSystemMetadata,
		powerStatus: readWindowsPowerStatus,
	}
}

func platformTelemetryRunner() func(context.Context, string, ...string) ([]byte, error) {
	return runWindowsBounded
}

func readWindowsMemory(ctx context.Context) (windowsMemorySample, error) {
	stat, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return windowsMemorySample{}, normalizeWindowsTelemetryError(err)
	}
	return windowsMemorySample{Total: stat.Total, Available: stat.Available}, nil
}

func readWindowsCPU(ctx context.Context) (windowsCPUSample, error) {
	counters, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return windowsCPUSample{}, normalizeWindowsTelemetryError(err)
	}
	if len(counters) == 0 {
		return windowsCPUSample{}, nil
	}
	total := counters[0].Total()
	if math.IsNaN(total) || math.IsInf(total, 0) || total <= 0 {
		return windowsCPUSample{}, nil
	}
	sample := windowsCPUSample{CountersAvailable: true}
	loads, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		sample.LoadError = metricErrorCode(normalizeWindowsTelemetryError(err))
		return sample, nil
	}
	if len(loads) != 0 {
		sample.LoadAvailable = true
		sample.LoadPercent = loads[0]
	}
	return sample, nil
}

func readWindowsCurrentRSS(ctx context.Context) (uint64, error) {
	proc, err := process.NewProcessWithContext(ctx, int32(os.Getpid()))
	if err != nil {
		return 0, normalizeWindowsTelemetryError(err)
	}
	stat, err := proc.MemoryInfoWithContext(ctx)
	if err != nil {
		return 0, normalizeWindowsTelemetryError(err)
	}
	return stat.RSS, nil
}

func readWindowsProcesses(ctx context.Context) ([]windowsProcessSample, error) {
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return nil, normalizeWindowsTelemetryError(err)
	}
	if len(pids) > maxWindowsProcesses {
		return nil, windowsTelemetryFailed
	}
	samples := make([]windowsProcessSample, 0, len(pids))
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, normalizeWindowsTelemetryError(err)
		}
		if pid <= 0 { // PID 0 is the synthetic System Idle Process.
			continue
		}
		proc, err := process.NewProcessWithContext(ctx, pid)
		if err != nil {
			return nil, normalizeWindowsTelemetryError(err)
		}
		name, err := proc.ExeWithContext(ctx)
		if err != nil {
			return nil, normalizeWindowsTelemetryError(err)
		}
		sample := windowsProcessSample{Name: name, NameKnown: true}
		if isWindowsLlamaServerName(name) {
			createdAt, err := proc.CreateTimeWithContext(ctx)
			if err != nil {
				return nil, normalizeWindowsTelemetryError(err)
			}
			memory, err := proc.MemoryInfoWithContext(ctx)
			if err != nil {
				return nil, normalizeWindowsTelemetryError(err)
			}
			// gopsutil caches CreateTime on each Process object; use a fresh
			// object for the post-read identity check so PID reuse is observable.
			procAgain, err := process.NewProcessWithContext(ctx, pid)
			if err != nil {
				return nil, normalizeWindowsTelemetryError(err)
			}
			nameAgain, err := procAgain.ExeWithContext(ctx)
			if err != nil {
				return nil, normalizeWindowsTelemetryError(err)
			}
			createdAgain, err := procAgain.CreateTimeWithContext(ctx)
			if err != nil {
				return nil, normalizeWindowsTelemetryError(err)
			}
			sample.RSS = memory.RSS
			sample.RSSKnown = true
			sample.Stable = strings.EqualFold(name, nameAgain) && createdAt == createdAgain
		}
		samples = append(samples, sample)
	}
	return samples, nil
}

func readWindowsSystemMetadata(ctx context.Context) (windowsSystemMetadata, error) {
	platform, _, platformVersion, err := host.PlatformInformationWithContext(ctx)
	if err != nil {
		return windowsSystemMetadata{}, normalizeWindowsTelemetryError(err)
	}
	kernelVersion, err := host.KernelVersionWithContext(ctx)
	if err != nil {
		return windowsSystemMetadata{}, normalizeWindowsTelemetryError(err)
	}
	kernelArch, err := host.KernelArch()
	if err != nil {
		return windowsSystemMetadata{}, normalizeWindowsTelemetryError(err)
	}
	uptime, err := host.UptimeWithContext(ctx)
	if err != nil {
		return windowsSystemMetadata{}, normalizeWindowsTelemetryError(err)
	}
	return windowsSystemMetadata{
		OS:              "windows",
		Platform:        platform,
		PlatformVersion: platformVersion,
		KernelVersion:   kernelVersion,
		KernelArch:      kernelArch,
		UptimeSeconds:   uptime,
	}, nil
}

type windowsSystemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func readWindowsPowerStatus() (windowsPowerStatus, error) {
	var status windowsSystemPowerStatus
	if err := windowsPowerProcedure.Find(); err != nil {
		return windowsPowerStatus{}, normalizeWindowsTelemetryError(err)
	}
	result, _, callErr := windowsPowerProcedure.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return windowsPowerStatus{}, normalizeWindowsTelemetryError(callErr)
	}
	return windowsPowerStatus{
		ACLineStatus:       status.ACLineStatus,
		BatteryFlag:        status.BatteryFlag,
		BatteryLifePercent: status.BatteryLifePercent,
	}, nil
}

func runWindowsBounded(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "nvidia-smi" || len(args) != len(windowsGPUQueryArgs) || args[0] != windowsGPUQueryArgs[0] || args[1] != windowsGPUQueryArgs[1] {
		return nil, windowsGPUCommandUnavailable
	}
	path, err := findWindowsNVIDIASMI(os.Stat)
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, gpuProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, windowsGPUQueryArgs...)
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, normalizeWindowsTelemetryError(err)
	}
	systemRoot := filepath.Dir(systemDirectory)
	cmd.Env = []string{
		"SystemRoot=" + systemRoot,
		"WINDIR=" + systemRoot,
		"PATH=" + systemDirectory,
		"LC_ALL=C",
	}
	output := &boundedOutput{limit: 8192}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	err = cmd.Run()
	if probeCtx.Err() != nil {
		return nil, probeCtx.Err()
	}
	return output.data, err
}

func normalizeWindowsTelemetryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || os.IsPermission(err) {
		return fs.ErrPermission
	}
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return fs.ErrNotExist
	}
	return windowsTelemetryFailed
}
