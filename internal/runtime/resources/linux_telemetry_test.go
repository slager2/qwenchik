package resources

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var linuxTelemetryTestTime = time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)

func TestParseLinuxCPUStat(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		want    linuxCPUCounters
		wantErr bool
	}{
		{
			name: "aggregate and per cpu rows",
			data: "cpu 10 20 30 40 50 60 70 80 90 100\ncpu0 1 2 3 4\n",
			want: linuxCPUCounters{values: [linuxCPUCounterCount]uint64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}, count: 10},
		},
		{
			name: "minimum legacy counters",
			data: "cpu 1 2 3 4\n",
			want: linuxCPUCounters{values: [linuxCPUCounterCount]uint64{1, 2, 3, 4}, count: 4},
		},
		{name: "no aggregate", data: "cpu0 1 2 3 4\n", wantErr: true},
		{name: "duplicate aggregate", data: "cpu 1 2 3 4\ncpu 1 2 3 4\n", wantErr: true},
		{name: "missing required counter", data: "cpu 1 2 3\n", wantErr: true},
		{name: "negative counter", data: "cpu 1 2 -3 4\n", wantErr: true},
		{name: "counter overflow", data: "cpu 1 2 3 18446744073709551616\n", wantErr: true},
		{name: "too many counters", data: "cpu 1 2 3 4 5 6 7 8 9 10 11\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLinuxCPUStat([]byte(tc.data))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("counters = %+v, want %+v", got, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), tc.data) {
				t.Fatal("raw proc input leaked through parser error")
			}
		})
	}
}

func TestLinuxCPUUsageDelta(t *testing.T) {
	baseline := linuxCPUCounters{
		values: [linuxCPUCounterCount]uint64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100},
		count:  10,
	}
	current := linuxCPUCounters{
		values: [linuxCPUCounterCount]uint64{130, 100, 110, 150, 110, 100, 100, 100, 200, 300},
		count:  10,
	}
	rolledBack := current
	rolledBack.values[9] = 99 // Guest counters must also be monotonic.
	changedWidth := current
	changedWidth.count = 9
	unchanged := baseline
	overflowing := linuxCPUCounters{
		values: [linuxCPUCounterCount]uint64{math.MaxUint64, math.MaxUint64, 0, 0, 0, 0, 0, 0},
		count:  8,
	}

	for _, tc := range []struct {
		name      string
		previous  *linuxCPUCounters
		current   linuxCPUCounters
		wantValue *float64
		wantError domain.MetricErrorCode
	}{
		{name: "first sample", current: current, wantError: domain.MetricErrorTemporarilyUnavailable},
		{name: "exact counter delta", previous: &baseline, current: current, wantValue: floatPtr(40)},
		{name: "zero total delta", previous: &baseline, current: unchanged, wantError: domain.MetricErrorTemporarilyUnavailable},
		{name: "counter rollback", previous: &baseline, current: rolledBack, wantError: domain.MetricErrorCollectionFailed},
		{name: "counter count changed", previous: &baseline, current: changedWidth, wantError: domain.MetricErrorCollectionFailed},
		{name: "total delta overflow", previous: &linuxCPUCounters{count: 8}, current: overflowing, wantError: domain.MetricErrorCollectionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := linuxCPUUsage(tc.previous, tc.current, linuxTelemetryTestTime)
			if got.Unit != domain.MetricUnitPercent || got.UpdatedAt != linuxTelemetryTestTime {
				t.Fatalf("metric metadata: %+v", got)
			}
			if tc.wantValue != nil {
				if got.Source != domain.MetricSourceExact || got.Value == nil || *got.Value != *tc.wantValue || got.Error != "" {
					t.Fatalf("metric = %+v, want exact %v", got, *tc.wantValue)
				}
			} else if got.Source != domain.MetricSourceUnavailable || got.Value != nil || got.Error != tc.wantError {
				t.Fatalf("metric = %+v, want unavailable error %q", got, tc.wantError)
			}
		})
	}
}

func TestParseLinuxCPUTemperatureFiltersAndValidates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		label      string
		sensorType string
		raw        string
		selected   bool
		wantValue  *float64
		wantError  domain.MetricErrorCode
	}{
		{name: "package label", label: "Package id 0", raw: "42000\n", selected: true, wantValue: floatPtr(42)},
		{name: "CPU thermal type", label: "thermal zone 0", sensorType: "cpu-thermal", raw: "65000", selected: true, wantValue: floatPtr(65)},
		{name: "hwmon CPU type", label: "temp1", sensorType: "2", raw: "39000", selected: true, wantValue: floatPtr(39)},
		{name: "core label", label: "Core 0", raw: "200000", selected: true, wantValue: floatPtr(200)},
		{name: "lower bound", label: "Tdie", raw: "-100000", selected: true, wantValue: floatPtr(-100)},
		{name: "GPU label", label: "GPU Core 0", sensorType: "cpu", raw: "60000"},
		{name: "GPU type", label: "temp1", sensorType: "3", raw: "60000"},
		{name: "GPU driver with CPU-like label", label: "Tdie", sensorType: "amdgpu", raw: "60000"},
		{name: "non CPU sensor", label: "System", raw: "30000"},
		{name: "unidentified sensor", label: "temp1", sensorType: "unknown", raw: "30000"},
		{name: "invalid number", label: "CPU", raw: "secret-not-a-number", selected: true, wantError: domain.MetricErrorCollectionFailed},
		{name: "above safe range", label: "CPU", raw: "200001", selected: true, wantError: domain.MetricErrorCollectionFailed},
		{name: "below safe range", label: "CPU", raw: "-100001", selected: true, wantError: domain.MetricErrorCollectionFailed},
		{name: "integer overflow", label: "CPU", raw: "9223372036854775808", selected: true, wantError: domain.MetricErrorCollectionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, selected := parseLinuxCPUTemperature(tc.label, tc.sensorType, []byte(tc.raw), linuxTelemetryTestTime)
			if selected != tc.selected {
				t.Fatalf("selected = %v, want %v", selected, tc.selected)
			}
			if !selected {
				return
			}
			if got.Unit != domain.MetricUnitCelsius || got.UpdatedAt != linuxTelemetryTestTime {
				t.Fatalf("metric metadata: %+v", got)
			}
			if tc.wantValue != nil {
				if got.Source != domain.MetricSourceExact || got.Value == nil || *got.Value != *tc.wantValue {
					t.Fatalf("metric = %+v, want exact %v", got, *tc.wantValue)
				}
			} else if got.Source != domain.MetricSourceUnavailable || got.Value != nil || got.Error != tc.wantError {
				t.Fatalf("metric = %+v, want unavailable error %q", got, tc.wantError)
			}
		})
	}
}

func TestParseLinuxGPUTelemetryMultipleDevicesAndUnits(t *testing.T) {
	data := "NVIDIA A, 37, 52, 1024, 3072, 4096, 75.5\n\"NVIDIA, B\", 0, 0, N/A, 8192, 8192, N/A\n"
	gpus, err := parseLinuxGPUTelemetry([]byte(data), linuxTelemetryTestTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 2 {
		t.Fatalf("GPU count = %d, want 2", len(gpus))
	}
	first := gpus[0]
	if first.Name.Value == nil || *first.Name.Value != "NVIDIA A" || first.Utilization.Value == nil || *first.Utilization.Value != 37 || first.Temperature.Value == nil || *first.Temperature.Value != 52 || first.PowerWatts.Value == nil || *first.PowerWatts.Value != 75.5 {
		t.Fatalf("first GPU metrics: %+v", first)
	}
	if first.VRAMUsed.Value == nil || *first.VRAMUsed.Value != 1024*(1<<20) || first.VRAMFree.Value == nil || *first.VRAMFree.Value != 3072*(1<<20) || first.VRAMTotal.Value == nil || *first.VRAMTotal.Value != 4096*(1<<20) {
		t.Fatalf("first GPU memory conversion: %+v", first)
	}
	second := gpus[1]
	if second.Name.Value == nil || *second.Name.Value != "NVIDIA, B" || second.Utilization.Value == nil || *second.Utilization.Value != 0 || second.Temperature.Value == nil || *second.Temperature.Value != 0 {
		t.Fatalf("second GPU metrics: %+v", second)
	}
	if second.VRAMUsed.Source != domain.MetricSourceUnavailable || second.VRAMUsed.Error != domain.MetricErrorTemporarilyUnavailable || second.VRAMUsed.Value != nil || second.PowerWatts.Source != domain.MetricSourceUnavailable || second.PowerWatts.Error != domain.MetricErrorTemporarilyUnavailable || second.PowerWatts.Value != nil {
		t.Fatalf("N/A fields not represented as unavailable metrics: %+v", second)
	}
	if second.VRAMFree.Value == nil || *second.VRAMFree.Value != 8192*(1<<20) || second.VRAMTotal.Value == nil || *second.VRAMTotal.Value != 8192*(1<<20) {
		t.Fatalf("second GPU memory conversion: %+v", second)
	}
}

func TestParseLinuxGPUAllNAAreExplicitUnavailable(t *testing.T) {
	gpus, err := parseLinuxGPUTelemetry([]byte("N/A, N/A, N/A, N/A, N/A, N/A, N/A\n"), linuxTelemetryTestTime)
	if err != nil || len(gpus) != 1 {
		t.Fatalf("parse result: %v, %v", gpus, err)
	}
	metrics := []struct {
		name   string
		source domain.MetricSource
		error  domain.MetricErrorCode
		value  bool
	}{
		{"name", gpus[0].Name.Source, gpus[0].Name.Error, gpus[0].Name.Value == nil},
		{"utilization", gpus[0].Utilization.Source, gpus[0].Utilization.Error, gpus[0].Utilization.Value == nil},
		{"temperature", gpus[0].Temperature.Source, gpus[0].Temperature.Error, gpus[0].Temperature.Value == nil},
		{"used", gpus[0].VRAMUsed.Source, gpus[0].VRAMUsed.Error, gpus[0].VRAMUsed.Value == nil},
		{"free", gpus[0].VRAMFree.Source, gpus[0].VRAMFree.Error, gpus[0].VRAMFree.Value == nil},
		{"total", gpus[0].VRAMTotal.Source, gpus[0].VRAMTotal.Error, gpus[0].VRAMTotal.Value == nil},
		{"power", gpus[0].PowerWatts.Source, gpus[0].PowerWatts.Error, gpus[0].PowerWatts.Value == nil},
	}
	for _, metric := range metrics {
		if metric.source != domain.MetricSourceUnavailable || metric.error != domain.MetricErrorTemporarilyUnavailable || !metric.value {
			t.Errorf("%s metric is not explicitly unavailable: %+v", metric.name, metric)
		}
	}
}

func TestParseLinuxGPURejectsMalformedWholeResult(t *testing.T) {
	tooLargeMiB := strconv.FormatUint(uint64(math.MaxInt64)/(1<<20)+1, 10)
	for _, tc := range []struct {
		name string
		row  string
	}{
		{name: "too few columns", row: "GPU, 10, 50, 1, 2, 3"},
		{name: "too many columns", row: "GPU, 10, 50, 1, 2, 3, 4, extra"},
		{name: "invalid utilization", row: "GPU, 101, 50, 1, 2, 3, 4"},
		{name: "nonfinite utilization", row: "GPU, NaN, 50, 1, 2, 3, 4"},
		{name: "negative utilization", row: "GPU, -1, 50, 1, 2, 3, 4"},
		{name: "temperature high", row: "GPU, 10, 201, 1, 2, 3, 4"},
		{name: "temperature low", row: "GPU, 10, -101, 1, 2, 3, 4"},
		{name: "nonfinite temperature", row: "GPU, 10, +Inf, 1, 2, 3, 4"},
		{name: "invalid memory", row: "GPU, 10, 50, -1, 2, 3, 4"},
		{name: "memory overflow", row: "GPU, 10, 50, " + tooLargeMiB + ", 2, 3, 4"},
		{name: "invalid power", row: "GPU, 10, 50, 1, 2, 3, -4"},
		{name: "nonfinite power", row: "GPU, 10, 50, 1, 2, 3, NaN"},
		{name: "zero total memory", row: "GPU, 10, 50, 0, 0, 0, 4"},
		{name: "memory over total", row: "GPU, 10, 50, 3, 2, 4, 4"},
		{name: "negative free memory", row: "GPU, 10, 50, 1, -2, 3, 4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := "GPU sensitive marker, 10, 50, 1, 2, 3, 4\n" + tc.row + "\n"
			gpus, err := parseLinuxGPUTelemetry([]byte(input), linuxTelemetryTestTime)
			if err == nil || gpus != nil {
				t.Fatalf("accepted malformed result: %+v, %v", gpus, err)
			}
			if errors.Is(err, errInvalidLinuxGPUCSV) == false || strings.Contains(err.Error(), "sensitive marker") || strings.Contains(err.Error(), tc.row) {
				t.Fatalf("parser error was not sanitized: %v", err)
			}
		})
	}
}

func TestParseLinuxGPUEmptyAndCSVErrors(t *testing.T) {
	for _, data := range []string{"", " \n\t"} {
		gpus, err := parseLinuxGPUTelemetry([]byte(data), linuxTelemetryTestTime)
		if err != nil || gpus == nil || len(gpus) != 0 {
			t.Fatalf("empty input %q: %#v, %v", data, gpus, err)
		}
	}
	if _, err := parseLinuxGPUTelemetry([]byte("\"unterminated, 1, 2, 3, 4, 5, 6"), linuxTelemetryTestTime); err == nil || err != errInvalidLinuxGPUCSV {
		t.Fatalf("CSV parse error was not sanitized: %v", err)
	}
	if _, err := parseLinuxGPUTelemetry([]byte("GPU, 10, 50, 1, 2, 3, 4"), time.Time{}); err == nil {
		t.Fatal("accepted zero timestamp")
	}
}

func floatPtr(value float64) *float64 { return &value }
