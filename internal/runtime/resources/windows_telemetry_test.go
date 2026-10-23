package resources

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestFindWindowsNVIDIASMIMissingUsesOnlyFixedLocations(t *testing.T) {
	var checked []string
	path, err := findWindowsNVIDIASMI(func(path string) (fs.FileInfo, error) {
		checked = append(checked, path)
		return nil, fs.ErrNotExist
	})
	if path != "" || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("path=%q err=%v, want missing executable", path, err)
	}
	if !reflect.DeepEqual(checked, windowsNVIDIASMILocations[:]) {
		t.Fatalf("checked locations %q, want fixed allowlist %q", checked, windowsNVIDIASMILocations)
	}
}

func TestWindowsGPUFailuresRemainUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, string, ...string) ([]byte, error)
	}{
		{
			name: "missing executable",
			run: func(context.Context, string, ...string) ([]byte, error) {
				return nil, exec.ErrNotFound
			},
		},
		{
			name: "malformed output",
			run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("private marker, 101, 50, 1, 2, 3, 4\n"), nil
			},
		},
		{
			name: "timed out",
			run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > gpuProbeTimeout {
					t.Error("GPU provider was not given a bounded deadline")
				}
				return nil, context.DeadlineExceeded
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &HostProvider{run: tc.run}
			snapshot := Snapshot{Warnings: []string{}}
			system := unavailableSystemTelemetry(linuxTelemetryTestTime, domain.MetricErrorTemporarilyUnavailable)
			capabilities := domain.TelemetryCapabilities{}
			p.collectGPUs(context.Background(), &snapshot, &system, &capabilities, linuxTelemetryTestTime)
			if len(system.GPUs) != 0 || capabilities.GPU || capabilities.GPUUtilization || capabilities.GPUTemperature || snapshot.GPUFreeVRAMMB != nil || snapshot.GPUTempC != nil {
				t.Fatalf("failed GPU probe produced known telemetry: system=%+v capabilities=%+v snapshot=%+v", system.GPUs, capabilities, snapshot)
			}
			if !containsString(snapshot.Warnings, "GPU telemetry unavailable") || strings.Contains(strings.Join(snapshot.Warnings, " "), "private marker") {
				t.Fatalf("GPU failure warning was missing or unsanitized: %v", snapshot.Warnings)
			}
		})
	}
}

func TestWindowsLlamaServerRSSRejectsMultipleProcesses(t *testing.T) {
	got := mapWindowsLlamaServerRSS([]windowsProcessSample{
		{Name: "llama-server.exe", NameKnown: true, RSS: 300 << 20, RSSKnown: true, Stable: true},
		{Name: `C:\Models\llama-server.exe`, NameKnown: true, RSS: 400 << 20, RSSKnown: true, Stable: true},
	}, linuxTelemetryTestTime)
	if got.Source != domain.MetricSourceUnavailable || got.Value != nil || got.Error != domain.MetricErrorTemporarilyUnavailable {
		t.Fatalf("multiple llama-server processes were treated as unique: %+v", got)
	}
}

func TestWindowsPowerStatusUnknownRemainsUnknown(t *testing.T) {
	status := windowsPowerStatus{ACLineStatus: 255, BatteryFlag: 255, BatteryLifePercent: 255}
	status, statusError, source := sampleWindowsPowerStatus(func() (windowsPowerStatus, error) {
		return status, nil
	})
	got := mapWindowsPowerTelemetry(status, statusError, linuxTelemetryTestTime)
	if source != PowerUnknown || got.powerAvailable || got.batteryAvailable ||
		got.source.Source != domain.MetricSourceUnavailable || got.source.Value != nil || got.source.Error != domain.MetricErrorTemporarilyUnavailable {
		t.Fatalf("unknown Windows power state was fabricated: %+v", got)
	}
	failedStatus, failedCode, failedSource := sampleWindowsPowerStatus(func() (windowsPowerStatus, error) {
		return windowsPowerStatus{}, fs.ErrPermission
	})
	failed := mapWindowsPowerTelemetry(failedStatus, failedCode, linuxTelemetryTestTime)
	if failedSource != PowerUnknown || failed.powerAvailable || failed.source.Source != domain.MetricSourceUnavailable || failed.source.Error != domain.MetricErrorPermissionDenied {
		t.Fatalf("failed Windows power probe was treated as battery power: %+v", failed)
	}
	knownStatus, knownCode, knownSource := sampleWindowsPowerStatus(func() (windowsPowerStatus, error) {
		return windowsPowerStatus{ACLineStatus: 1, BatteryFlag: 8, BatteryLifePercent: 76}, nil
	})
	known := mapWindowsPowerTelemetry(knownStatus, knownCode, linuxTelemetryTestTime)
	if knownSource != PowerAC || !known.powerAvailable || !known.batteryAvailable || known.source.Value == nil || *known.source.Value != string(PowerAC) ||
		known.batteryPercent.Value == nil || *known.batteryPercent.Value != 76 || known.batteryCharging.Value == nil || !*known.batteryCharging.Value {
		t.Fatalf("known Windows power state was not mapped: %+v", known)
	}
	noBattery := mapWindowsPowerTelemetry(windowsPowerStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255}, "", linuxTelemetryTestTime)
	if !noBattery.powerAvailable || noBattery.batteryAvailable || noBattery.batteryPercent.Error != domain.MetricErrorUnsupported || noBattery.batteryCharging.Error != domain.MetricErrorUnsupported {
		t.Fatalf("absent Windows battery was not explicitly unsupported: %+v", noBattery)
	}
	offlineNoBattery := windowsPowerStatus{ACLineStatus: 0, BatteryFlag: 128, BatteryLifePercent: 255}
	if got := windowsPowerSourceFromStatus(offlineNoBattery); got != PowerUnknown {
		t.Fatalf("offline system without a battery reported power source %q", got)
	}
	offlineUnknownBattery := windowsPowerStatus{ACLineStatus: 0, BatteryFlag: 255, BatteryLifePercent: 255}
	if got := windowsPowerSourceFromStatus(offlineUnknownBattery); got != PowerUnknown {
		t.Fatalf("offline system with unknown battery reported power source %q", got)
	}
}

func TestWindowsMetricsMapExactEstimatedAndUnavailableValues(t *testing.T) {
	const expectedRSS = uint64(19 << 20)
	const expectedServerRSS = uint64(700 << 20)
	metadata := windowsSystemMetadata{
		OS:              "Windows",
		Platform:        "Microsoft Windows 11 Pro",
		PlatformVersion: "23H2",
		KernelVersion:   "10.0.22631",
		KernelArch:      "x86_64",
		UptimeSeconds:   86400,
	}
	p := &HostProvider{
		windows: windowsTelemetrySource{
			memory: func(context.Context) (windowsMemorySample, error) {
				return windowsMemorySample{Total: 32 << 30, Available: 12 << 30}, nil
			},
			cpu: func(context.Context) (windowsCPUSample, error) {
				return windowsCPUSample{CountersAvailable: true, LoadAvailable: true, LoadPercent: 37.5}, nil
			},
			currentRSS: func(context.Context) (uint64, error) { return expectedRSS, nil },
			processes: func(context.Context) ([]windowsProcessSample, error) {
				return []windowsProcessSample{
					{Name: "worker.exe", NameKnown: true},
					{Name: "llama-server.exe", NameKnown: true, RSS: expectedServerRSS, RSSKnown: true, Stable: true},
				}, nil
			},
			metadata: func(context.Context) (windowsSystemMetadata, error) { return metadata, nil },
		},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("NVIDIA Test, 20, 50, 1024, 2048, 4096, 75.5\n"), nil
		},
	}
	snapshot := Snapshot{Warnings: []string{}}
	system := unavailableSystemTelemetry(linuxTelemetryTestTime, domain.MetricErrorTemporarilyUnavailable)
	capabilities := domain.TelemetryCapabilities{}
	p.collectWindowsTelemetry(context.Background(), &snapshot, &system, &capabilities, linuxTelemetryTestTime)

	if system.RAMTotal.Source != domain.MetricSourceExact || system.RAMTotal.Value == nil || *system.RAMTotal.Value != 32<<30 ||
		system.RAMFree.Source != domain.MetricSourceExact || system.RAMFree.Value == nil || *system.RAMFree.Value != 12<<30 ||
		system.RAMUsed.Source != domain.MetricSourceExact || system.RAMUsed.Value == nil || *system.RAMUsed.Value != 20<<30 {
		t.Fatalf("Windows RAM mapping: total=%+v free=%+v used=%+v", system.RAMTotal, system.RAMFree, system.RAMUsed)
	}
	if system.CPUUsage.Source != domain.MetricSourceEstimated || system.CPUUsage.Value == nil || *system.CPUUsage.Value != 37.5 ||
		system.CPUTemperature.Source != domain.MetricSourceUnavailable || system.CPUTemperature.Error != domain.MetricErrorUnsupported {
		t.Fatalf("Windows CPU mapping: usage=%+v temperature=%+v", system.CPUUsage, system.CPUTemperature)
	}
	if system.AgentRSS.Source != domain.MetricSourceExact || system.AgentRSS.Value == nil || *system.AgentRSS.Value != int64(expectedRSS) ||
		system.LlamaServerRSS.Source != domain.MetricSourceExact || system.LlamaServerRSS.Value == nil || *system.LlamaServerRSS.Value != int64(expectedServerRSS) ||
		snapshot.RSSBytes == nil || *snapshot.RSSBytes != int64(expectedRSS) {
		t.Fatalf("Windows process RSS mapping: agent=%+v llama=%+v legacy=%v", system.AgentRSS, system.LlamaServerRSS, snapshot.RSSBytes)
	}
	if !capabilities.CPU || !capabilities.RAM || !capabilities.GPU || !capabilities.GPUUtilization || !capabilities.GPUTemperature ||
		len(system.GPUs) != 1 || system.GPUs[0].Name.Value == nil || *system.GPUs[0].Name.Value != "NVIDIA Test" {
		t.Fatalf("Windows capabilities/GPU mapping: %+v %+v", capabilities, system.GPUs)
	}
	if !containsString(snapshot.Warnings, "CPU temperature unavailable") {
		t.Fatalf("CPU temperature unavailability was not explicit: %v", snapshot.Warnings)
	}
	if system.Metadata.OS != metadata.OS || system.Metadata.Platform != metadata.Platform || system.Metadata.PlatformVersion != metadata.PlatformVersion || system.Metadata.KernelVersion != metadata.KernelVersion || system.Metadata.KernelArch != metadata.KernelArch || system.Metadata.UptimeSeconds.Value == nil || *system.Metadata.UptimeSeconds.Value != float64(metadata.UptimeSeconds) {
		t.Fatalf("Windows system metadata mapping: %+v, want %+v", system.Metadata, metadata)
	}
	p.windowsMetadataMu.RLock()
	gotMetadata := p.windowsMetadata
	p.windowsMetadataMu.RUnlock()
	if !reflect.DeepEqual(gotMetadata, metadata) {
		t.Fatalf("system metadata was not retained: %+v, want %+v", gotMetadata, metadata)
	}
	if _, err := json.Marshal(system); err != nil {
		t.Fatalf("mapped Windows telemetry is not serializable: %v", err)
	}
}
